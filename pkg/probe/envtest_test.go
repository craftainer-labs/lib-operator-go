package probe_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync/atomic"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/config"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	"github.com/craftainer-labs/lib-operator-go/pkg/apis/common"
	"github.com/craftainer-labs/lib-operator-go/pkg/conditions"
	"github.com/craftainer-labs/lib-operator-go/pkg/internal/testapi"
	"github.com/craftainer-labs/lib-operator-go/pkg/probe"
	"github.com/craftainer-labs/lib-operator-go/pkg/server"
	"github.com/craftainer-labs/lib-operator-go/pkg/testenv"
)

// env is the one API server of this package's envtest tests.
var env *testenv.Env

func TestMain(m *testing.M) {
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{corev1.AddToScheme, testapi.AddToScheme} {
		if err := add(scheme); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	}
	var err error
	if env, err = testenv.Start(context.Background(), testenv.Options{Scheme: scheme, CRDs: testapi.CRDs()}); err != nil {
		fmt.Fprintln(os.Stderr, "cannot start envtest:", err)
		os.Exit(1)
	}
	code := m.Run()
	if err := env.Stop(); err != nil {
		fmt.Fprintln(os.Stderr, "cannot stop envtest:", err)
	}
	os.Exit(code)
}

// eventually polls cond for up to 30 seconds.
func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// Both server tiers against a real API server and a running controller: the
// status passes the CRD schema, a healthy server settles without status
// churn, an outage and a spec change are recorded.
func TestControllersAgainstAnAPIServer(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	operatorNS, teamNS := env.Namespace(t, nil), env.Namespace(t, nil)
	for _, ns := range []string{operatorNS, teamNS} {
		s := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "admin"},
			Data:       map[string][]byte{"username": []byte("root"), "password": []byte("pw")},
		}
		if err := env.Client.Create(ctx, s); err != nil {
			t.Fatal(err)
		}
	}
	shared := &testapi.ClusterWidgetServer{ObjectMeta: metav1.ObjectMeta{GenerateName: "probe-"}}
	shared.Spec.Endpoint = "https://shared.example"
	shared.Spec.AllowedNamespaces = common.AllowedNamespaces{From: common.NamespacesAll}
	shared.Spec.AdminCredentialsSecretRef = common.AdminSecretRef{Name: "admin", Namespace: operatorNS}
	team := &testapi.WidgetServer{ObjectMeta: metav1.ObjectMeta{Name: "team", Namespace: teamNS}}
	team.Spec.Endpoint = "https://team.example"
	team.Spec.AdminCredentialsSecretRef = common.AdminSecretRef{Name: "admin"}
	for _, o := range []client.Object{shared, team} {
		if err := env.Client.Create(ctx, o); err != nil {
			t.Fatal(err)
		}
	}

	var down atomic.Bool
	prober := probe.ProberFunc(func(context.Context, server.Server, server.Connection) (string, error) {
		if down.Load() {
			return "", errors.New("backend is down")
		}
		return "9.9", nil
	})
	o := probe.Options{OperatorNamespace: operatorNS, DefaultInterval: 200 * time.Millisecond}
	skip := true
	mgr, err := ctrl.NewManager(env.Config, ctrl.Options{
		Scheme:     env.Scheme,
		Metrics:    metricsserver.Options{BindAddress: "0"},
		Controller: config.Controller{SkipNameValidation: &skip},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range []*probe.Reconciler{
		{Client: mgr.GetClient(), New: func() probe.Server { return &testapi.ClusterWidgetServer{} }, Prober: prober, Options: o},
		{Client: mgr.GetClient(), New: func() probe.Server { return &testapi.WidgetServer{} }, Prober: prober, Options: o},
	} {
		if err := r.SetupWithManager(mgr); err != nil {
			t.Fatal(err)
		}
	}
	stopped := make(chan error, 1)
	go func() { stopped <- mgr.Start(ctx) }()
	t.Cleanup(func() {
		cancel()
		if err := <-stopped; err != nil {
			t.Errorf("manager: %v", err)
		}
	})

	statuses := func() (common.ServerStatus, common.ServerStatus, string) {
		var cs testapi.ClusterWidgetServer
		var ts testapi.WidgetServer
		if err := env.Client.Get(ctx, client.ObjectKeyFromObject(shared), &cs); err != nil {
			t.Fatal(err)
		}
		if err := env.Client.Get(ctx, client.ObjectKeyFromObject(team), &ts); err != nil {
			t.Fatal(err)
		}
		return cs.Status, ts.Status, cs.ResourceVersion + "/" + ts.ResourceVersion
	}
	is := func(st common.ServerStatus, typ string, want metav1.ConditionStatus) bool {
		c := meta.FindStatusCondition(st.Conditions, typ)
		return c != nil && c.Status == want
	}

	eventually(t, "both servers to be Ready", func() bool {
		cs, ts, _ := statuses()
		return is(cs, conditions.Ready, metav1.ConditionTrue) && is(ts, conditions.Ready, metav1.ConditionTrue) &&
			cs.ServerVersion == "9.9" && ts.ServerVersion == "9.9" && cs.LastProbeTime != nil && ts.LastProbeTime != nil
	})
	// Several probes later nothing was written: status writes neither churn nor loop.
	_, _, settled := statuses()
	time.Sleep(time.Second)
	if _, _, again := statuses(); again != settled {
		t.Errorf("resourceVersions moved from %s to %s while the servers were healthy", settled, again)
	}

	down.Store(true)
	eventually(t, "the outage to be recorded", func() bool {
		cs, ts, _ := statuses()
		return is(cs, conditions.ServerReachable, metav1.ConditionFalse) && is(ts, conditions.ServerReachable, metav1.ConditionFalse) &&
			is(cs, conditions.Ready, metav1.ConditionFalse)
	})

	// A spec change probes at once and records the new generation.
	down.Store(false)
	var cur testapi.ClusterWidgetServer
	if err := env.Client.Get(ctx, client.ObjectKeyFromObject(shared), &cur); err != nil {
		t.Fatal(err)
	}
	orig := cur.DeepCopy()
	cur.Spec.Endpoint = "https://moved.example"
	if err := env.Client.Patch(ctx, &cur, client.MergeFrom(orig)); err != nil { // a patch: the controller writes the status meanwhile
		t.Fatal(err)
	}
	eventually(t, "the new generation to be observed", func() bool {
		var got testapi.ClusterWidgetServer
		if err := env.Client.Get(ctx, client.ObjectKeyFromObject(shared), &got); err != nil {
			t.Fatal(err)
		}
		c := meta.FindStatusCondition(got.Status.Conditions, conditions.Ready)
		return got.Generation > 1 && got.Status.ObservedGeneration == got.Generation &&
			c != nil && c.Status == metav1.ConditionTrue && c.ObservedGeneration == got.Generation
	})
}

// A server whose admin Secret is missing is InvalidServer, and creating the
// Secret probes it at once: the interval here is an hour, so only the watch on
// the Secret can explain a quick recovery.
func TestSecretWatchProbesAtOnce(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	ns := env.Namespace(t, nil)
	team := &testapi.WidgetServer{ObjectMeta: metav1.ObjectMeta{Name: "late", Namespace: ns}}
	team.Spec.Endpoint = "https://late.example"
	team.Spec.AdminCredentialsSecretRef = common.AdminSecretRef{Name: "admin"}
	if err := env.Client.Create(ctx, team); err != nil {
		t.Fatal(err)
	}

	skip := true
	mgr, err := ctrl.NewManager(env.Config, ctrl.Options{
		Scheme:     env.Scheme,
		Metrics:    metricsserver.Options{BindAddress: "0"},
		Controller: config.Controller{SkipNameValidation: &skip},
	})
	if err != nil {
		t.Fatal(err)
	}
	r := &probe.Reconciler{
		Client: mgr.GetClient(),
		New:    func() probe.Server { return &testapi.WidgetServer{} },
		Prober: probe.ProberFunc(func(context.Context, server.Server, server.Connection) (string, error) {
			return "1.0", nil
		}),
		Options:    probe.Options{DefaultInterval: time.Hour},
		Controller: controller.Options{MaxConcurrentReconciles: 2},
	}
	if err := r.SetupWithManager(mgr); err != nil {
		t.Fatal(err)
	}
	stopped := make(chan error, 1)
	go func() { stopped <- mgr.Start(ctx) }()
	t.Cleanup(func() {
		cancel()
		if err := <-stopped; err != nil {
			t.Errorf("manager: %v", err)
		}
	})

	ready := func(want metav1.ConditionStatus, reason string) func() bool {
		return func() bool {
			var got testapi.WidgetServer
			if err := env.Client.Get(ctx, client.ObjectKeyFromObject(team), &got); err != nil {
				t.Fatal(err)
			}
			c := meta.FindStatusCondition(got.Status.Conditions, conditions.Ready)
			return c != nil && c.Status == want && c.Reason == reason
		}
	}
	eventually(t, "the missing Secret to be recorded", ready(metav1.ConditionFalse, conditions.InvalidServer))
	s := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "admin"},
		Data:       map[string][]byte{"username": []byte("root")},
	}
	if err := env.Client.Create(ctx, s); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the new Secret to probe the server", ready(metav1.ConditionTrue, conditions.Available))
}
