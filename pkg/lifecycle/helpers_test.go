package lifecycle_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/go-logr/logr/funcr"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/craftainer-labs/lib-operator-go/pkg/apis/common"
	"github.com/craftainer-labs/lib-operator-go/pkg/conditions"
	"github.com/craftainer-labs/lib-operator-go/pkg/internal/testapi"
	"github.com/craftainer-labs/lib-operator-go/pkg/lifecycle"
	"github.com/craftainer-labs/lib-operator-go/pkg/lifecycle/lifecycletest"
	"github.com/craftainer-labs/lib-operator-go/pkg/testenv"
)

// spec is the S of the Externals under test; obj is their O.
type (
	spec struct {
		Size string `json:"size,omitempty"`
	}
	obj string
)

// now is the clock of every test.
var now = time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)

const poll = 10 * time.Minute

func testScheme(t testing.TB) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	if err := corev1.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	if err := testapi.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	return s
}

// harness is a fake API server, a recording Events recorder and a captured log.
type harness struct {
	t   *testing.T
	c   client.Client
	rec *testenv.Recorder
	ctx context.Context

	mu   sync.Mutex
	logs []string
}

func newHarness(t *testing.T, funcs *interceptor.Funcs, objs ...client.Object) *harness {
	t.Helper()
	b := fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(objs...).
		WithStatusSubresource(&testapi.Widget{}, &testapi.Gadget{})
	if funcs != nil {
		b = b.WithInterceptorFuncs(*funcs)
	}
	h := &harness{t: t, c: b.Build(), rec: &testenv.Recorder{}}
	logger := funcr.NewJSON(func(line string) {
		h.mu.Lock()
		defer h.mu.Unlock()
		h.logs = append(h.logs, line)
	}, funcr.Options{})
	h.ctx = log.IntoContext(context.Background(), logger)
	return h
}

func (h *harness) logLines() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.logs...)
}

func (h *harness) dataOpts() lifecycle.Options {
	return lifecycle.Options{
		Class: lifecycle.ClassData, Recorder: h.rec, ManagedBy: "operator-test",
		Now: func() time.Time { return now }, Jitter: func() float64 { return 0 },
		Audit: nil,
	}
}

func (h *harness) credOpts() lifecycle.Options {
	o := h.dataOpts()
	o.Class = lifecycle.ClassCredential
	o.BindingType, o.Provider = "postgresql", "craftainer.dev"
	return o
}

func widget(mut ...func(*testapi.Widget)) *testapi.Widget {
	w := &testapi.Widget{
		ObjectMeta: metav1.ObjectMeta{Name: "w", Namespace: "ns", UID: "uid-w", Generation: 1},
		Spec: testapi.WidgetSpec{ClaimCommon: common.ClaimCommon{
			ServerRef: common.ServerRef{Kind: "ClusterWidgetServer", Name: "shared"}}},
	}
	for _, m := range mut {
		m(w)
	}
	return w
}

func gadget(mut ...func(*testapi.Gadget)) *testapi.Gadget {
	g := &testapi.Gadget{
		ObjectMeta: metav1.ObjectMeta{Name: "g", Namespace: "ns", UID: "uid-g", Generation: 1,
			CreationTimestamp: metav1.NewTime(now.Add(-24 * time.Hour))},
		Spec: testapi.GadgetSpec{ClaimCommon: common.ClaimCommon{
			ServerRef: common.ServerRef{Kind: "ClusterWidgetServer", Name: "shared"}}},
	}
	for _, m := range mut {
		m(g)
	}
	return g
}

func policies(p ...common.ManagementPolicy) []common.ManagementPolicy { return p }

func withFinalizer(w *testapi.Widget) { w.Finalizers = []string{lifecycle.DefaultFinalizer} }

func pinned(p common.DeletionPolicy, s common.DeletionPolicySource) func(*testapi.Widget) {
	return func(w *testapi.Widget) { w.Status.EffectiveDeletionPolicy, w.Status.DeletionPolicySource = p, s }
}

// terminating marks the claim as deleted; the fake client requires a finalizer
// on an object created with a deletion timestamp.
func terminating(w *testapi.Widget) {
	ts := metav1.NewTime(now)
	w.DeletionTimestamp = &ts
	if len(w.Finalizers) == 0 {
		w.Finalizers = []string{lifecycle.DefaultFinalizer}
	}
}

func terminatingGadget(g *testapi.Gadget) {
	ts := metav1.NewTime(now)
	g.DeletionTimestamp = &ts
	g.Finalizers = []string{lifecycle.DefaultFinalizer}
}

func (h *harness) widget(name string) *testapi.Widget {
	h.t.Helper()
	var w testapi.Widget
	if err := h.c.Get(h.ctx, client.ObjectKey{Namespace: "ns", Name: name}, &w); err != nil {
		h.t.Fatal(err)
	}
	return &w
}

func (h *harness) gadget(name string) *testapi.Gadget {
	h.t.Helper()
	var g testapi.Gadget
	if err := h.c.Get(h.ctx, client.ObjectKey{Namespace: "ns", Name: name}, &g); err != nil {
		h.t.Fatal(err)
	}
	return &g
}

func (h *harness) secret(name string) *corev1.Secret {
	h.t.Helper()
	var s corev1.Secret
	if err := h.c.Get(h.ctx, client.ObjectKey{Namespace: "ns", Name: name}, &s); err != nil {
		h.t.Fatal(err)
	}
	return &s
}

// reconcileWidget reads the claim afresh, as a controller does, and reconciles it.
func (h *harness) reconcileWidget(ext lifecycle.External[spec, obj], opts lifecycle.Options) (ctrl.Result, error) {
	h.t.Helper()
	return lifecycle.Reconcile(h.ctx, h.c, h.widget("w"), spec{Size: "s"}, ext, opts)
}

func (h *harness) reconcileGadget(ext lifecycle.External[spec, obj], opts lifecycle.Options) (ctrl.Result, error) {
	h.t.Helper()
	return lifecycle.Reconcile(h.ctx, h.c, h.gadget("g"), spec{Size: "s"}, ext, opts)
}

func cond(conds []metav1.Condition, typ string) metav1.Condition {
	if c := meta.FindStatusCondition(conds, typ); c != nil {
		return *c
	}
	return metav1.Condition{Type: typ, Reason: "<missing>"}
}

// wantCond fails unless the condition has the status and reason.
func wantCond(t *testing.T, conds []metav1.Condition, typ string, status metav1.ConditionStatus, reason string) {
	t.Helper()
	if c := cond(conds, typ); c.Status != status || c.Reason != reason {
		t.Errorf("condition %s = %s/%s (%q), want %s/%s", typ, c.Status, c.Reason, c.Message, status, reason)
	}
}

type (
	testWidget = testapi.Widget
	testGadget = testapi.Gadget
	fakeExt    = lifecycletest.Fake[spec, obj]
	fakeRot    = lifecycletest.FakeRotator[spec, obj]
	fakeLate   = lifecycletest.LateInitializing[spec, obj]
)

// existing is a Fake whose external object exists and matches the spec.
func existing() *fakeExt {
	return &fakeExt{Obs: lifecycle.Observation[obj]{Exists: true, UpToDate: true, Current: "cur"}}
}

// drifted is a Fake whose external object exists and differs from the spec.
func drifted() *fakeExt {
	return &fakeExt{Obs: lifecycle.Observation[obj]{Exists: true, UpToDate: false, Current: "cur"}}
}

// missing is a Fake with no external object.
func missing() *fakeExt { return &fakeExt{} }

func wantResult(t *testing.T, res ctrl.Result, err error, requeue time.Duration) {
	t.Helper()
	if err != nil || res.RequeueAfter != requeue {
		t.Errorf("result = %+v, %v; want requeue %v and no error", res, err, requeue)
	}
}

func hasFinalizer(o client.Object) bool {
	return controllerutil.ContainsFinalizer(o, lifecycle.DefaultFinalizer)
}

// Reason constants, to keep the tables short.
const (
	ready  = conditions.Ready
	synced = conditions.Synced
	reach  = conditions.ServerReachable
)
