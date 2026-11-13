package probe_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/event"

	"github.com/craftainer-labs/lib-operator-go/pkg/apis/common"
	"github.com/craftainer-labs/lib-operator-go/pkg/conditions"
	"github.com/craftainer-labs/lib-operator-go/pkg/internal/testapi"
	"github.com/craftainer-labs/lib-operator-go/pkg/probe"
	"github.com/craftainer-labs/lib-operator-go/pkg/server"
)

const opNS = "craftainer-system"

var t0 = time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)

// clock is a settable time source.
type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }

// stub is a Prober that records what it was given.
type stub struct {
	version     string
	err         error
	calls       int
	conn        server.Connection
	hasDeadline bool
}

func (s *stub) Probe(ctx context.Context, _ server.Server, conn server.Connection) (string, error) {
	s.calls++
	s.conn = conn
	_, s.hasDeadline = ctx.Deadline()
	return s.version, s.err
}

func scheme(t *testing.T) *runtime.Scheme {
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

func mapper() meta.RESTMapper {
	m := meta.NewDefaultRESTMapper([]schema.GroupVersion{testapi.GroupVersion})
	m.Add(testapi.GroupVersion.WithKind("ClusterWidgetServer"), meta.RESTScopeRoot)
	m.Add(testapi.GroupVersion.WithKind("WidgetServer"), meta.RESTScopeNamespace)
	return m
}

func newClient(t *testing.T, funcs interceptor.Funcs, objs ...client.Object) client.Client {
	t.Helper()
	return fake.NewClientBuilder().WithScheme(scheme(t)).WithRESTMapper(mapper()).
		WithStatusSubresource(&testapi.ClusterWidgetServer{}, &testapi.WidgetServer{}).
		WithInterceptorFuncs(funcs).WithObjects(objs...).Build()
}

// counting returns interceptor funcs that count status writes.
func counting(n *int) interceptor.Funcs {
	return interceptor.Funcs{SubResourceUpdate: func(ctx context.Context, c client.Client, sub string,
		obj client.Object, opts ...client.SubResourceUpdateOption) error {
		*n++
		return c.SubResource(sub).Update(ctx, obj, opts...)
	}}
}

// failing returns interceptor funcs whose status writes fail with err.
func failing(err error) interceptor.Funcs {
	return interceptor.Funcs{SubResourceUpdate: func(context.Context, client.Client, string,
		client.Object, ...client.SubResourceUpdateOption) error {
		return err
	}}
}

func admin(namespace string) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: "admin"},
		Data:       map[string][]byte{"username": []byte("root"), "password": []byte("pw")},
	}
}

func teamServer(mut func(*testapi.WidgetServer)) *testapi.WidgetServer {
	s := &testapi.WidgetServer{
		ObjectMeta: metav1.ObjectMeta{Name: "team", Namespace: "team-a", Generation: 1},
		Spec:       testapi.WidgetServerSpec{Endpoint: "https://team.example"},
	}
	s.Spec.AdminCredentialsSecretRef = common.AdminSecretRef{Name: "admin"}
	if mut != nil {
		mut(s)
	}
	return s
}

func sharedServer(mut func(*testapi.ClusterWidgetServer)) *testapi.ClusterWidgetServer {
	s := &testapi.ClusterWidgetServer{
		ObjectMeta: metav1.ObjectMeta{Name: "shared", Generation: 1},
		Spec: testapi.ClusterWidgetServerSpec{
			Endpoint:          "https://shared.example",
			AllowedNamespaces: common.AllowedNamespaces{From: common.NamespacesAll},
		},
	}
	s.Spec.AdminCredentialsSecretRef = common.AdminSecretRef{Name: "admin", Namespace: opNS}
	if mut != nil {
		mut(s)
	}
	return s
}

var (
	teamKey   = types.NamespacedName{Namespace: "team-a", Name: "team"}
	sharedKey = types.NamespacedName{Name: "shared"}
)

func getTeam(t *testing.T, c client.Client) *testapi.WidgetServer {
	t.Helper()
	var s testapi.WidgetServer
	if err := c.Get(context.Background(), teamKey, &s); err != nil {
		t.Fatal(err)
	}
	return &s
}

func opts(clk *clock) probe.Options {
	return probe.Options{OperatorNamespace: opNS, Now: clk.now, Jitter: func() float64 { return 0.5 }}
}

func cond(t *testing.T, st common.ServerStatus, typ string, status metav1.ConditionStatus, reason string) *metav1.Condition {
	t.Helper()
	c := meta.FindStatusCondition(st.Conditions, typ)
	if c == nil || c.Status != status || c.Reason != reason {
		t.Fatalf("condition %s = %+v, want %s/%s", typ, c, status, reason)
	}
	return c
}

func TestReconcileRecordsAServerThatAnswers(t *testing.T) {
	c := newClient(t, interceptor.Funcs{}, teamServer(nil), admin("team-a"))
	clk := &clock{t0}
	p := &stub{version: "16.2"}
	res, err := probe.Reconcile(context.Background(), c, getTeam(t, c), p, opts(clk))
	if err != nil {
		t.Fatal(err)
	}
	if res.RequeueAfter != time.Minute+3*time.Second { // the default plus 0.5 of 10% jitter
		t.Errorf("RequeueAfter = %v", res.RequeueAfter)
	}
	if p.calls != 1 || !p.hasDeadline || p.conn.Endpoint != "https://team.example" || string(p.conn.Admin["password"]) != "pw" {
		t.Errorf("probe saw calls=%d deadline=%v conn=%+v", p.calls, p.hasDeadline, p.conn)
	}
	st := getTeam(t, c).Status
	if st.ServerVersion != "16.2" || st.ObservedGeneration != 1 || st.LastProbeTime == nil || !st.LastProbeTime.Time.Equal(t0) {
		t.Errorf("status = %+v", st)
	}
	for _, typ := range []string{conditions.Ready, conditions.ServerReachable} {
		if c := cond(t, st, typ, metav1.ConditionTrue, conditions.Available); c.ObservedGeneration != 1 {
			t.Errorf("%s observedGeneration = %d", typ, c.ObservedGeneration)
		}
	}
}

func TestReconcileClusterServerAndIntervals(t *testing.T) {
	tests := []struct {
		name string
		mut  func(*testapi.ClusterWidgetServer)
		opt  func(*probe.Options)
		want time.Duration
	}{
		{"default", nil, nil, time.Minute + 3*time.Second},
		{"operator default", nil, func(o *probe.Options) { o.DefaultInterval = 2 * time.Minute }, 2*time.Minute + 6*time.Second},
		{"spec", func(s *testapi.ClusterWidgetServer) {
			s.Spec.HealthCheckInterval = &metav1.Duration{Duration: 5 * time.Minute}
		}, nil, 5*time.Minute + 15*time.Second},
		{"spec below the bound", func(s *testapi.ClusterWidgetServer) {
			s.Spec.HealthCheckInterval = &metav1.Duration{Duration: time.Second}
		}, nil, 10*time.Second + 500*time.Millisecond},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := newClient(t, interceptor.Funcs{}, sharedServer(tt.mut), admin(opNS))
			o := opts(&clock{t0})
			if tt.opt != nil {
				tt.opt(&o)
			}
			var s testapi.ClusterWidgetServer
			if err := c.Get(context.Background(), sharedKey, &s); err != nil {
				t.Fatal(err)
			}
			res, err := probe.Reconcile(context.Background(), c, &s, &stub{}, o)
			if err != nil || res.RequeueAfter != tt.want {
				t.Fatalf("res = %+v, err = %v, want %v", res, err, tt.want)
			}
			if err := c.Get(context.Background(), sharedKey, &s); err != nil {
				t.Fatal(err)
			}
			cond(t, s.Status, conditions.Ready, metav1.ConditionTrue, conditions.Available)
		})
	}
}

func TestStatusIsWrittenOnlyOnChange(t *testing.T) {
	var writes int
	c := newClient(t, counting(&writes), teamServer(nil), admin("team-a"))
	clk := &clock{t0}
	p := &stub{version: "16.2"}
	run := func() {
		t.Helper()
		if _, err := probe.Reconcile(context.Background(), c, getTeam(t, c), p, opts(clk)); err != nil {
			t.Fatal(err)
		}
	}
	run()
	clk.t = t0.Add(time.Minute)
	run()
	if writes != 1 || !getTeam(t, c).Status.LastProbeTime.Time.Equal(t0) {
		t.Errorf("writes = %d, lastProbeTime = %v after an unchanged probe", writes, getTeam(t, c).Status.LastProbeTime)
	}
	clk.t = t0.Add(probe.DefaultRefreshInterval)
	run()
	if writes != 2 || !getTeam(t, c).Status.LastProbeTime.Time.Equal(clk.t) {
		t.Errorf("writes = %d, lastProbeTime = %v after the refresh interval", writes, getTeam(t, c).Status.LastProbeTime)
	}
	p.version = "16.3"
	run()
	if writes != 3 || getTeam(t, c).Status.ServerVersion != "16.3" {
		t.Errorf("writes = %d, version = %q after a version change", writes, getTeam(t, c).Status.ServerVersion)
	}
}

func TestGenerationChangeIsRecorded(t *testing.T) {
	var writes int
	c := newClient(t, counting(&writes), teamServer(nil), admin("team-a"))
	clk := &clock{t0}
	p := &stub{}
	if _, err := probe.Reconcile(context.Background(), c, getTeam(t, c), p, opts(clk)); err != nil {
		t.Fatal(err)
	}
	s := getTeam(t, c)
	s.Generation = 2 // a spec change
	if _, err := probe.Reconcile(context.Background(), c, s, p, opts(clk)); err != nil {
		t.Fatal(err)
	}
	st := getTeam(t, c).Status
	if writes != 2 || st.ObservedGeneration != 2 {
		t.Errorf("writes = %d, observedGeneration = %d", writes, st.ObservedGeneration)
	}
	if c := cond(t, st, conditions.Ready, metav1.ConditionTrue, conditions.Available); c.ObservedGeneration != 2 {
		t.Errorf("condition observedGeneration = %d", c.ObservedGeneration)
	}
}

func TestProbeFailure(t *testing.T) {
	var writes int
	c := newClient(t, counting(&writes), teamServer(nil), admin("team-a"))
	clk := &clock{t0}
	p := &stub{version: "16.2"}
	if _, err := probe.Reconcile(context.Background(), c, getTeam(t, c), p, opts(clk)); err != nil {
		t.Fatal(err)
	}
	boom := errors.New("dial tcp 10.0.0.7:5432: password authentication failed for root")
	p.err = boom
	res, err := probe.Reconcile(context.Background(), c, getTeam(t, c), p, opts(clk))
	if !errors.Is(err, boom) || res != (ctrl.Result{}) { // an error, so controller-runtime backs off
		t.Fatalf("res = %+v, err = %v", res, err)
	}
	st := getTeam(t, c).Status
	reach := cond(t, st, conditions.ServerReachable, metav1.ConditionFalse, conditions.ServerUnreachable)
	ready := cond(t, st, conditions.Ready, metav1.ConditionFalse, conditions.ServerUnreachable)
	for _, c := range []*metav1.Condition{reach, ready} {
		if strings.Contains(c.Message, "10.0.0.7") || strings.Contains(c.Message, "password") {
			t.Errorf("the raw error is in the status: %q", c.Message)
		}
	}
	if st.ServerVersion != "16.2" {
		t.Errorf("the last known version was dropped: %q", st.ServerVersion)
	}
	before := writes
	if _, err := probe.Reconcile(context.Background(), c, getTeam(t, c), p, opts(clk)); !errors.Is(err, boom) {
		t.Fatal(err)
	}
	if writes != before {
		t.Error("a repeated failure wrote the status again")
	}
	// Recovery flips both conditions back.
	p.err = nil
	if _, err := probe.Reconcile(context.Background(), c, getTeam(t, c), p, opts(clk)); err != nil {
		t.Fatal(err)
	}
	cond(t, getTeam(t, c).Status, conditions.Ready, metav1.ConditionTrue, conditions.Available)
}

func TestVersionFitsTheSchema(t *testing.T) {
	tests := []struct{ name, in, want string }{
		{"short", "1.2.3", "1.2.3"},
		{"invalid UTF-8 is dropped", "1.0\xff", "1.0"},
		{"cut at a character boundary", "a" + strings.Repeat("é", 40), "a" + strings.Repeat("é", 31)},
		{"cut at exactly the limit", strings.Repeat("v", 80), strings.Repeat("v", probe.MaxVersionLength)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var writes int
			c := newClient(t, counting(&writes), teamServer(nil), admin("team-a"))
			clk := &clock{t0}
			p := &stub{version: tt.in}
			for range 2 {
				if _, err := probe.Reconcile(context.Background(), c, getTeam(t, c), p, opts(clk)); err != nil {
					t.Fatal(err)
				}
			}
			got := getTeam(t, c).Status.ServerVersion
			if got != tt.want || !utf8.ValidString(got) || len(got) > probe.MaxVersionLength {
				t.Errorf("version = %q, want %q", got, tt.want)
			}
			if writes != 1 {
				t.Errorf("writes = %d: the same backend looked changed", writes)
			}
		})
	}
}

func TestForbiddenAdminSecretNamespace(t *testing.T) {
	var writes int
	srv := sharedServer(func(s *testapi.ClusterWidgetServer) { s.Spec.AdminCredentialsSecretRef.Namespace = "tenant" })
	c := newClient(t, counting(&writes), srv, admin(opNS), admin("tenant"))
	p := &stub{}
	clk := &clock{t0}
	for i := 0; i < 2; i++ {
		var s testapi.ClusterWidgetServer
		if err := c.Get(context.Background(), sharedKey, &s); err != nil {
			t.Fatal(err)
		}
		res, err := probe.Reconcile(context.Background(), c, &s, p, opts(clk))
		if err != nil || res.RequeueAfter != time.Minute+3*time.Second { // not an error: backoff cannot fix it
			t.Fatalf("res = %+v, err = %v", res, err)
		}
	}
	var s testapi.ClusterWidgetServer
	if err := c.Get(context.Background(), sharedKey, &s); err != nil {
		t.Fatal(err)
	}
	cond(t, s.Status, conditions.ServerReachable, metav1.ConditionFalse, conditions.AdminSecretNamespaceForbidden)
	cond(t, s.Status, conditions.Ready, metav1.ConditionFalse, conditions.AdminSecretNamespaceForbidden)
	if p.calls != 0 || writes != 1 || s.Status.LastProbeTime != nil {
		t.Errorf("calls = %d, writes = %d, lastProbeTime = %v", p.calls, writes, s.Status.LastProbeTime)
	}
}

func TestUnreadableConnectionSettings(t *testing.T) {
	c := newClient(t, interceptor.Funcs{}, teamServer(nil)) // the admin Secret is missing
	p := &stub{}
	res, err := probe.Reconcile(context.Background(), c, getTeam(t, c), p, opts(&clock{t0}))
	if err == nil || res != (ctrl.Result{}) || p.calls != 0 {
		t.Fatalf("res = %+v, err = %v, calls = %d", res, err, p.calls)
	}
	st := getTeam(t, c).Status
	cond(t, st, conditions.ServerReachable, metav1.ConditionUnknown, conditions.InvalidServer)
	c2 := cond(t, st, conditions.Ready, metav1.ConditionFalse, conditions.InvalidServer)
	if strings.Contains(c2.Message, "admin") {
		t.Errorf("the raw error is in the status: %q", c2.Message)
	}
}

func TestOperatorNamespace(t *testing.T) {
	c := newClient(t, interceptor.Funcs{}, sharedServer(nil), admin(opNS))
	o := opts(&clock{t0})
	o.OperatorNamespace = ""
	get := func() *testapi.ClusterWidgetServer {
		var s testapi.ClusterWidgetServer
		if err := c.Get(context.Background(), sharedKey, &s); err != nil {
			t.Fatal(err)
		}
		return &s
	}
	t.Setenv(server.OperatorNamespaceEnv, "")
	if _, err := probe.Reconcile(context.Background(), c, get(), &stub{}, o); !errors.Is(err, server.ErrOperatorNamespaceUnset) {
		t.Errorf("unset: err = %v", err)
	}
	t.Setenv(server.OperatorNamespaceEnv, opNS)
	if _, err := probe.Reconcile(context.Background(), c, get(), &stub{}, o); err != nil {
		t.Errorf("from the environment: err = %v", err)
	}
}

// noScope is a client that cannot tell whether a kind is namespaced.
type noScope struct{ client.Client }

func (noScope) IsObjectNamespaced(runtime.Object) (bool, error) {
	return false, errors.New("no mapping")
}

func TestUnknownScope(t *testing.T) {
	c := newClient(t, interceptor.Funcs{}, teamServer(nil), admin("team-a"))
	p := &stub{}
	if _, err := probe.Reconcile(context.Background(), noScope{c}, getTeam(t, c), p, opts(&clock{t0})); err == nil || p.calls != 0 {
		t.Errorf("err = %v, calls = %d", err, p.calls)
	}
}

func TestDeletingServerIsLeftAlone(t *testing.T) {
	now := metav1.NewTime(t0)
	srv := teamServer(func(s *testapi.WidgetServer) {
		s.DeletionTimestamp, s.Finalizers = &now, []string{"keep"}
	})
	var writes int
	c := newClient(t, counting(&writes), srv, admin("team-a"))
	p := &stub{}
	res, err := probe.Reconcile(context.Background(), c, getTeam(t, c), p, opts(&clock{t0}))
	if err != nil || res != (ctrl.Result{}) || p.calls != 0 || writes != 0 {
		t.Errorf("res = %+v, err = %v, calls = %d, writes = %d", res, err, p.calls, writes)
	}
}

func TestStatusWriteFailures(t *testing.T) {
	broken := errors.New("etcd is down")
	boom := errors.New("backend is down")
	tests := []struct {
		name    string
		objs    []client.Object
		prober  *stub
		wantAll []error // every one must be in the returned error
	}{
		{"success", []client.Object{admin("team-a")}, &stub{}, []error{broken}},
		{"probe failure", []client.Object{admin("team-a")}, &stub{err: boom}, []error{broken, boom}},
		{"unreadable settings", nil, &stub{}, []error{broken}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := newClient(t, failing(broken), append([]client.Object{teamServer(nil)}, tt.objs...)...)
			_, err := probe.Reconcile(context.Background(), c, getTeam(t, c), tt.prober, opts(&clock{t0}))
			for _, want := range tt.wantAll {
				if !errors.Is(err, want) {
					t.Errorf("err = %v, want %v in it", err, want)
				}
			}
		})
	}
	t.Run("forbidden", func(t *testing.T) {
		srv := sharedServer(func(s *testapi.ClusterWidgetServer) { s.Spec.AdminCredentialsSecretRef.Namespace = "tenant" })
		c := newClient(t, failing(broken), srv)
		var s testapi.ClusterWidgetServer
		if err := c.Get(context.Background(), sharedKey, &s); err != nil {
			t.Fatal(err)
		}
		if _, err := probe.Reconcile(context.Background(), c, &s, &stub{}, opts(&clock{t0})); !errors.Is(err, broken) {
			t.Errorf("err = %v", err)
		}
	})
	t.Run("server gone", func(t *testing.T) {
		c := newClient(t, failing(apierrors.NewNotFound(schema.GroupResource{Resource: "widgetservers"}, "team")),
			teamServer(nil), admin("team-a"))
		if _, err := probe.Reconcile(context.Background(), c, getTeam(t, c), &stub{}, opts(&clock{t0})); err != nil {
			t.Errorf("err = %v", err)
		}
	})
}

func TestProberFunc(t *testing.T) {
	var got server.Server
	f := probe.ProberFunc(func(_ context.Context, srv server.Server, _ server.Connection) (string, error) {
		got = srv
		return "v", nil
	})
	srv := teamServer(nil)
	if v, err := f.Probe(context.Background(), srv, server.Connection{}); v != "v" || err != nil || got != server.Server(srv) {
		t.Errorf("v = %q, err = %v, srv = %v", v, err, got)
	}
}

func TestReconciler(t *testing.T) {
	c := newClient(t, interceptor.Funcs{}, teamServer(nil), admin("team-a"))
	p := &stub{version: "1"}
	r := &probe.Reconciler{
		Client: c, New: func() probe.Server { return &testapi.WidgetServer{} }, Prober: p, Options: opts(&clock{t0}),
	}
	if res, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "team-a", Name: "gone"}}); err != nil || res != (ctrl.Result{}) || p.calls != 0 {
		t.Errorf("a missing server: res = %+v, err = %v, calls = %d", res, err, p.calls)
	}
	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: teamKey}); err != nil {
		t.Fatal(err)
	}
	if got := getTeam(t, c).Status.ServerVersion; got != "1" {
		t.Errorf("version = %q", got)
	}
	// A failing read is an error.
	r.Client = newClient(t, interceptor.Funcs{Get: func(context.Context, client.WithWatch, client.ObjectKey, client.Object, ...client.GetOption) error {
		return errors.New("cache is not synced")
	}})
	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: teamKey}); err == nil {
		t.Error("a failed read was swallowed")
	}
}

func TestPredicates(t *testing.T) {
	p := probe.Predicates()
	old := teamServer(nil)
	statusOnly := old.DeepCopy()
	statusOnly.Status.ServerVersion = "16.2"
	specChange := old.DeepCopy()
	specChange.Generation = 2
	if !p.Create(event.CreateEvent{Object: old}) {
		t.Error("creation does not probe")
	}
	if p.Update(event.UpdateEvent{ObjectOld: old, ObjectNew: statusOnly}) {
		t.Error("a status write triggers a probe")
	}
	if !p.Update(event.UpdateEvent{ObjectOld: old, ObjectNew: specChange}) {
		t.Error("a spec change does not trigger a probe")
	}
}
