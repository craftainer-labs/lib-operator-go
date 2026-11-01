package testenv_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/tools/events"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/craftainer-labs/lib-operator-go/pkg/internal/testapi"
	"github.com/craftainer-labs/lib-operator-go/pkg/testenv"
)

// env is the one API server of this package's tests.
var env *testenv.Env

func TestMain(m *testing.M) {
	ctx := context.Background()
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{corev1.AddToScheme, testapi.AddToScheme} {
		if err := add(scheme); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	}
	var err error
	if env, err = testenv.Start(ctx, testenv.Options{Scheme: scheme, CRDs: testapi.CRDs()}); err != nil {
		fmt.Fprintln(os.Stderr, "cannot start envtest:", err)
		os.Exit(1)
	}
	code := m.Run()
	if err := env.Stop(); err != nil {
		fmt.Fprintln(os.Stderr, "cannot stop envtest:", err)
	}
	os.Exit(code)
}

// fakeTB records what a helper reports instead of failing the real test.
type fakeTB struct {
	testing.TB
	errs     []string
	fatals   []string
	cleanups []func()
}

func (f *fakeTB) Helper() {}
func (f *fakeTB) Errorf(format string, args ...any) {
	f.errs = append(f.errs, fmt.Sprintf(format, args...))
}
func (f *fakeTB) Fatalf(format string, args ...any) {
	f.fatals = append(f.fatals, fmt.Sprintf(format, args...))
}
func (f *fakeTB) Cleanup(fn func()) { f.cleanups = append(f.cleanups, fn) }

func TestStartInstallsTheCRDs(t *testing.T) {
	if env.Config == nil || env.Client == nil || env.Scheme == nil {
		t.Fatal("the environment is not wired")
	}
	ns := env.Namespace(t, nil)
	w := &testapi.Widget{}
	w.Name, w.Namespace = "installed", ns
	w.Spec.ServerRef.Kind, w.Spec.ServerRef.Name = "ClusterWidgetServer", "shared"
	if err := env.Client.Create(context.Background(), w); err != nil {
		t.Fatalf("the Widget CRD is not served: %v", err)
	}
}

func TestNamespaceCarriesTheLabels(t *testing.T) {
	ns := env.Namespace(t, map[string]string{"team": "a"})
	var got corev1.Namespace
	if err := env.Client.Get(context.Background(), client.ObjectKey{Name: ns}, &got); err != nil {
		t.Fatal(err)
	}
	if got.Labels["team"] != "a" || !strings.HasPrefix(got.Name, "t-") {
		t.Errorf("namespace = %+v", got.ObjectMeta)
	}
}

func TestNamespaceIsDeletedWithTheTest(t *testing.T) {
	var name string
	t.Run("fixture", func(t *testing.T) { name = env.Namespace(t, nil) })
	var got corev1.Namespace
	err := env.Client.Get(context.Background(), client.ObjectKey{Name: name}, &got)
	// envtest has no namespace controller, so the namespace stays Terminating,
	// but the delete was issued.
	if err != nil && !apierrors.IsNotFound(err) {
		t.Fatal(err)
	}
	if err == nil && got.DeletionTimestamp.IsZero() && got.Status.Phase != corev1.NamespaceTerminating {
		t.Errorf("namespace %s was not deleted: %+v", name, got.Status)
	}
}

func TestNamespaceReportsAPIFailures(t *testing.T) {
	boom := errors.New("boom")
	s := runtime.NewScheme()
	if err := corev1.AddToScheme(s); err != nil {
		t.Fatal(err)
	}

	failCreate := fake.NewClientBuilder().WithScheme(s).WithInterceptorFuncs(interceptor.Funcs{
		Create: func(context.Context, client.WithWatch, client.Object, ...client.CreateOption) error { return boom },
	}).Build()
	tb := &fakeTB{}
	(&testenv.Env{Client: failCreate}).Namespace(tb, nil)
	if len(tb.fatals) != 1 || !strings.Contains(tb.fatals[0], "boom") {
		t.Errorf("fatals = %v", tb.fatals)
	}

	failDelete := fake.NewClientBuilder().WithScheme(s).WithInterceptorFuncs(interceptor.Funcs{
		Delete: func(context.Context, client.WithWatch, client.Object, ...client.DeleteOption) error { return boom },
	}).Build()
	tb = &fakeTB{}
	(&testenv.Env{Client: failDelete}).Namespace(tb, nil)
	for _, fn := range tb.cleanups {
		fn()
	}
	if len(tb.errs) != 1 || !strings.Contains(tb.errs[0], "boom") {
		t.Errorf("errs = %v", tb.errs)
	}
}

func TestRecorder(t *testing.T) {
	r := &testenv.Recorder{}
	w := &testapi.Widget{}
	r.Eventf(w, nil, corev1.EventTypeNormal, "Created", "Create", "made %s #%d", "thing", 1)
	r.Eventf(w, nil, corev1.EventTypeWarning, "ReconcileError", "Observe", "failed: %s", "boom")

	// A note without arguments is not a format string: a literal % stays.
	var rec events.EventRecorder = r
	rec.Eventf(w, nil, corev1.EventTypeNormal, "Literal", "Observe", "disk at 100% and 50%d")
	if got := r.Events()[2].Note; got != "disk at 100% and 50%d" {
		t.Errorf("note = %q", got)
	}
	r.Reset()
	r.Eventf(w, nil, corev1.EventTypeNormal, "Created", "Create", "made %s #%d", "thing", 1)
	r.Eventf(w, nil, corev1.EventTypeWarning, "ReconcileError", "Observe", "failed: %s", "boom")

	events := r.Events()
	if len(events) != 2 || events[0].Note != "made thing #1" || events[0].Action != "Create" || events[0].Regarding != w ||
		events[1].Type != corev1.EventTypeWarning {
		t.Fatalf("events = %+v", events)
	}
	events[0].Reason = "mutated"
	if r.Events()[0].Reason != "Created" {
		t.Error("Events returns the internal slice")
	}
	if got := strings.Join(r.Reasons(), ","); got != "Created,ReconcileError" {
		t.Errorf("Reasons = %s", got)
	}

	ok := &fakeTB{}
	r.AssertEvent(ok, corev1.EventTypeWarning, "ReconcileError", "boom")
	r.AssertEvent(ok, corev1.EventTypeNormal, "Created", "")
	r.AssertNoEvent(ok, "Deleted")
	r.AssertReasons(ok, "Created", "ReconcileError")
	r.AssertNoSecret(ok, "hunter2", "")
	if len(ok.errs) != 0 {
		t.Errorf("assertions that hold reported: %v", ok.errs)
	}

	bad := &fakeTB{}
	r.AssertEvent(bad, corev1.EventTypeNormal, "ReconcileError", "boom") // wrong type
	r.AssertEvent(bad, corev1.EventTypeWarning, "ReconcileError", "nope")
	r.AssertNoEvent(bad, "Created")
	r.AssertReasons(bad, "Created")
	r.AssertNoSecret(bad, "thing")
	if len(bad.errs) != 5 {
		t.Errorf("failing assertions reported %d times: %v", len(bad.errs), bad.errs)
	}

	r.Reset()
	if len(r.Events()) != 0 {
		t.Error("Reset kept the events")
	}
}

// brokenCopy has deep-copy methods that lose the data.
type brokenCopy struct{ N int64 }

func (*brokenCopy) DeepCopy() *brokenCopy            { return &brokenCopy{} }
func (*brokenCopy) DeepCopyInto(*brokenCopy)         {}
func (*brokenCopy) DeepCopyObject() runtime.Object   { return &brokenCopy{} }
func (*brokenCopy) GetObjectKind() schema.ObjectKind { return schema.EmptyObjectKind }

type goodCopy struct{ N int64 }

func (g *goodCopy) DeepCopyInto(out *goodCopy) { *out = *g }
func (g *goodCopy) DeepCopy() *goodCopy {
	if g == nil {
		return nil
	}
	c := *g
	return &c
}

func TestVerifyDeepCopy(t *testing.T) {
	good := &fakeTB{}
	testenv.VerifyDeepCopy(good, &goodCopy{})
	if len(good.errs) != 0 {
		t.Errorf("a correct deep copy was reported: %v", good.errs)
	}
	bad := &fakeTB{}
	testenv.VerifyDeepCopy(bad, &brokenCopy{})
	if len(bad.errs) != 5 {
		t.Errorf("a broken deep copy reported %d problems: %v", len(bad.errs), bad.errs)
	}
}
