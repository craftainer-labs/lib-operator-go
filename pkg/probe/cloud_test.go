package probe_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/craftainer-labs/lib-operator-go/pkg/apis/common"
	"github.com/craftainer-labs/lib-operator-go/pkg/conditions"
	"github.com/craftainer-labs/lib-operator-go/pkg/internal/testapi"
	"github.com/craftainer-labs/lib-operator-go/pkg/probe"
	"github.com/craftainer-labs/lib-operator-go/pkg/server"
)

// cloudStub is a CloudProber that records what it was given.
type cloudStub struct {
	res         probe.CloudResult
	err         error
	calls       int
	conn        server.CloudConnection
	hasDeadline bool
}

func (s *cloudStub) ProbeCloud(ctx context.Context, _ server.CloudServer, conn server.CloudConnection) (probe.CloudResult, error) {
	s.calls++
	s.conn = conn
	_, s.hasDeadline = ctx.Deadline()
	return s.res, s.err
}

func cloudTeam(mut func(*testapi.CloudWidgetServer)) *testapi.CloudWidgetServer {
	s := &testapi.CloudWidgetServer{ObjectMeta: metav1.ObjectMeta{Name: "team", Namespace: "team-a", Generation: 1}}
	if mut != nil {
		mut(s)
	}
	return s
}

func cloudShared(mut func(*testapi.ClusterCloudWidgetServer)) *testapi.ClusterCloudWidgetServer {
	s := &testapi.ClusterCloudWidgetServer{
		ObjectMeta: metav1.ObjectMeta{Name: "shared", Generation: 1},
		Spec:       testapi.ClusterCloudWidgetServerSpec{AllowedNamespaces: common.AllowedNamespaces{From: common.NamespacesAll}},
	}
	if mut != nil {
		mut(s)
	}
	return s
}

func getCloudTeam(t *testing.T, c client.Client) *testapi.CloudWidgetServer {
	t.Helper()
	var s testapi.CloudWidgetServer
	if err := c.Get(context.Background(), teamKey, &s); err != nil {
		t.Fatal(err)
	}
	return &s
}

func cloudReconciler(c client.Client, p probe.CloudProber, s setup) *probe.Reconciler {
	r := &probe.Reconciler{Client: c, NewCloud: func() probe.CloudServer { return &testapi.CloudWidgetServer{} }, CloudProber: p, Options: s.Options}
	r.SetClock(s.clk.now, func() float64 { return 0.5 })
	return r
}

func runCloud(ctx context.Context, c client.Client, srv probe.CloudServer, p probe.CloudProber, s setup) (ctrl.Result, error) {
	return cloudReconciler(c, p, s).ProbeCloudServer(ctx, srv)
}

func withCreds(s *testapi.CloudWidgetServer) {
	s.Spec.CredentialsSecret = &common.AdminSecretRef{Name: "creds"}
}

func creds(namespace string) *corev1.Secret {
	return &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: "creds"}, Data: map[string][]byte{"accessKeyId": []byte("AK")}}
}

var _ probe.CloudServer = (*testapi.CloudWidgetServer)(nil)

func TestCloudServerWithNoAdminSecretIsProbed(t *testing.T) {
	var writes int
	c := newClient(t, counting(&writes), cloudTeam(withCreds), creds("team-a"))
	clk := &clock{t0}
	p := &cloudStub{res: probe.CloudResult{Version: "16.2", Discovered: map[string]string{"endpoint": "db.example", "port": "5432"}}}
	res, err := runCloud(context.Background(), c, getCloudTeam(t, c), p, opts(clk))
	if err != nil || res.RequeueAfter != time.Minute+3*time.Second {
		t.Fatalf("res = %+v, err = %v", res, err)
	}
	if p.calls != 1 || !p.hasDeadline || string(p.conn.Credentials["accessKeyId"]) != "AK" || len(p.conn.Admin) != 0 {
		t.Errorf("probe = %+v", p)
	}
	st := getCloudTeam(t, c).Status
	cond(t, st, conditions.ServerReachable, metav1.ConditionTrue, conditions.Available)
	cond(t, st, conditions.Ready, metav1.ConditionTrue, conditions.Available)
	if st.ServerVersion != "16.2" || st.Discovered["endpoint"] != "db.example" || st.Discovered["port"] != "5432" || len(st.Discovered) != 2 {
		t.Errorf("status = %+v", st)
	}
	if writes != 1 {
		t.Errorf("writes = %d, want 1", writes)
	}

	// Unchanged: no write inside the refresh interval.
	if _, err := runCloud(context.Background(), c, getCloudTeam(t, c), p, opts(clk)); err != nil || writes != 1 {
		t.Fatalf("err = %v, writes = %d", err, writes)
	}
	// A different map is written.
	p.res.Discovered = map[string]string{"endpoint": "db2.example"}
	if _, err := runCloud(context.Background(), c, getCloudTeam(t, c), p, opts(clk)); err != nil || writes != 2 {
		t.Fatalf("err = %v, writes = %d", err, writes)
	}
	if d := getCloudTeam(t, c).Status.Discovered; len(d) != 1 || d["endpoint"] != "db2.example" {
		t.Errorf("discovered = %v", d)
	}
	// An empty result clears it.
	p.res.Discovered = map[string]string{}
	if _, err := runCloud(context.Background(), c, getCloudTeam(t, c), p, opts(clk)); err != nil || writes != 3 {
		t.Fatalf("err = %v, writes = %d", err, writes)
	}
	if d := getCloudTeam(t, c).Status.Discovered; d != nil {
		t.Errorf("discovered = %v, want none", d)
	}
}

func TestCloudProbeFailureKeepsWhatWasDiscovered(t *testing.T) {
	c := newClient(t, interceptor.Funcs{}, cloudTeam(nil))
	clk := &clock{t0}
	p := &cloudStub{res: probe.CloudResult{Version: "1.0", Discovered: map[string]string{"region": "eu-west-1"}}}
	if _, err := runCloud(context.Background(), c, getCloudTeam(t, c), p, opts(clk)); err != nil {
		t.Fatal(err)
	}
	p.err, p.res = errors.New("AccessDenied: arn:aws:iam::111122223333"), probe.CloudResult{Discovered: map[string]string{"region": "other"}}
	res, err := runCloud(context.Background(), c, getCloudTeam(t, c), p, opts(clk))
	if err != nil || res.RequeueAfter == 0 {
		t.Fatalf("res = %+v, err = %v", res, err)
	}
	st := getCloudTeam(t, c).Status
	reach := cond(t, st, conditions.ServerReachable, metav1.ConditionFalse, conditions.ServerUnreachable)
	if strings.Contains(reach.Message, "arn:") || st.ServerVersion != "1.0" || st.Discovered["region"] != "eu-west-1" {
		t.Errorf("status = %+v", st)
	}
}

func TestDiscoveredFitsTheSchema(t *testing.T) {
	big := map[string]string{}
	for i := range 15 {
		big["k"+string(rune('a'+i))] = "v"
	}
	big["ka"] = strings.Repeat("é", 400) // 800 bytes, cut at a character boundary
	big["kb"] = "bad\xffbyte"
	c := newClient(t, interceptor.Funcs{}, cloudTeam(nil))
	p := &cloudStub{res: probe.CloudResult{Discovered: big}}
	if _, err := runCloud(context.Background(), c, getCloudTeam(t, c), p, opts(&clock{t0})); err != nil {
		t.Fatal(err)
	}
	d := getCloudTeam(t, c).Status.Discovered
	if len(d) != probe.MaxDiscovered {
		t.Fatalf("len = %d, want %d", len(d), probe.MaxDiscovered)
	}
	for k := range d {
		if k > "kj" { // the first ten by key are kept
			t.Errorf("kept %q", k)
		}
	}
	if len(d["ka"]) > probe.MaxDiscoveredLength || !utf8.ValidString(d["ka"]) || d["kb"] != "badbyte" {
		t.Errorf("values = %q, %q", d["ka"], d["kb"])
	}
}

func TestSelfHostedProbeNeverTouchesDiscovered(t *testing.T) {
	srv := teamServer(func(s *testapi.WidgetServer) { s.Status.Discovered = map[string]string{"endpoint": "kept"} })
	c := newClient(t, interceptor.Funcs{}, srv, admin("team-a"))
	if _, err := run(context.Background(), c, getTeam(t, c), &stub{version: "1"}, opts(&clock{t0})); err != nil {
		t.Fatal(err)
	}
	if d := getTeam(t, c).Status.Discovered; d["endpoint"] != "kept" || len(d) != 1 {
		t.Errorf("discovered = %v", d)
	}
}

func TestCloudConnectionProblems(t *testing.T) {
	type tc struct {
		name      string
		srv       *testapi.CloudWidgetServer
		objs      []client.Object
		reachable metav1.ConditionStatus
		reason    string
	}
	cases := []tc{
		{"credentials Secret in a forbidden namespace", cloudTeam(func(s *testapi.CloudWidgetServer) {
			s.Spec.CredentialsSecret = &common.AdminSecretRef{Name: "creds", Namespace: opNS}
		}), []client.Object{creds(opNS)}, metav1.ConditionFalse, conditions.CredentialsNamespaceForbidden},
		{"credentials Secret missing", cloudTeam(withCreds), nil, metav1.ConditionUnknown, conditions.CredentialsUnavailable},
		{"admin Secret in a forbidden namespace", cloudTeam(func(s *testapi.CloudWidgetServer) {
			s.Spec.AdminCredentialsSecretRef = &common.AdminSecretRef{Name: "admin", Namespace: opNS}
		}), nil, metav1.ConditionFalse, conditions.AdminSecretNamespaceForbidden},
		{"admin Secret missing", cloudTeam(func(s *testapi.CloudWidgetServer) {
			s.Spec.AdminCredentialsSecretRef = &common.AdminSecretRef{Name: "admin"}
		}), nil, metav1.ConditionUnknown, conditions.InvalidServer},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			c := newClient(t, interceptor.Funcs{}, append([]client.Object{tt.srv}, tt.objs...)...)
			p := &cloudStub{}
			res, err := runCloud(context.Background(), c, getCloudTeam(t, c), p, opts(&clock{t0}))
			if err != nil || res.RequeueAfter == 0 || p.calls != 0 {
				t.Fatalf("res = %+v, err = %v, calls = %d", res, err, p.calls)
			}
			st := getCloudTeam(t, c).Status
			cond(t, st, conditions.ServerReachable, tt.reachable, tt.reason)
			cond(t, st, conditions.Ready, metav1.ConditionFalse, tt.reason)
		})
	}
	t.Run("a transient credentials read failure is returned, and the status left alone", func(t *testing.T) {
		var writes int
		f := counting(&writes)
		f.Get = func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, o ...client.GetOption) error {
			if _, ok := obj.(*corev1.Secret); ok {
				return errors.New("etcd timeout")
			}
			return c.Get(ctx, key, obj, o...)
		}
		c := newClient(t, f, cloudTeam(withCreds), creds("team-a"))
		if _, err := runCloud(context.Background(), c, getCloudTeam(t, c), &cloudStub{}, opts(&clock{t0})); err == nil || writes != 0 {
			t.Errorf("err = %v, writes = %d", err, writes)
		}
	})
}

func TestCloudReconcile(t *testing.T) {
	ctx := context.Background()
	p := &cloudStub{res: probe.CloudResult{Version: "1", Discovered: map[string]string{"endpoint": "x"}}}
	t.Run("namespaced", func(t *testing.T) {
		c := newClient(t, interceptor.Funcs{}, cloudTeam(nil))
		r := cloudReconciler(c, p, opts(&clock{t0}))
		if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: teamKey}); err != nil {
			t.Fatal(err)
		}
		if getCloudTeam(t, c).Status.Discovered["endpoint"] != "x" {
			t.Error("not probed")
		}
	})
	t.Run("a server that is gone is not an error", func(t *testing.T) {
		r := cloudReconciler(newClient(t, interceptor.Funcs{}), p, opts(&clock{t0}))
		if res, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: teamKey}); err != nil || res.RequeueAfter != 0 {
			t.Errorf("res = %+v, err = %v", res, err)
		}
	})
	t.Run("a server being deleted is left alone", func(t *testing.T) {
		now := metav1.Now()
		srv := cloudTeam(func(s *testapi.CloudWidgetServer) { s.DeletionTimestamp = &now })
		c := newClient(t, interceptor.Funcs{})
		before := p.calls
		if res, err := runCloud(ctx, c, srv, p, opts(&clock{t0})); err != nil || res.RequeueAfter != 0 || p.calls != before {
			t.Errorf("res = %+v, err = %v", res, err)
		}
	})
	t.Run("the reconcile of a self-hosted reconciler is unchanged", func(t *testing.T) {
		c := newClient(t, interceptor.Funcs{}, teamServer(nil), admin("team-a"))
		sp := &stub{version: "2"}
		if _, err := reconciler(c, sp, opts(&clock{t0})).Reconcile(ctx, ctrl.Request{NamespacedName: teamKey}); err != nil || sp.calls != 1 {
			t.Errorf("err = %v, calls = %d", err, sp.calls)
		}
	})
}

func TestCloudProberFunc(t *testing.T) {
	want := probe.CloudResult{Version: "9"}
	var p probe.CloudProber = probe.CloudProberFunc(func(context.Context, server.CloudServer, server.CloudConnection) (probe.CloudResult, error) {
		return want, errors.New("x")
	})
	got, err := p.ProbeCloud(context.Background(), &testapi.CloudWidgetServer{}, server.CloudConnection{})
	if got.Version != "9" || err == nil {
		t.Errorf("got %+v, %v", got, err)
	}
}

func TestCloudDependents(t *testing.T) {
	cluster := cloudShared(func(s *testapi.ClusterCloudWidgetServer) {
		s.Spec.AdminCredentialsSecretRef = &common.AdminSecretRef{Name: "admin", Namespace: opNS}
		s.Spec.CredentialsSecret = &common.AdminSecretRef{Name: "creds", Namespace: opNS}
		s.Spec.TLS = &common.TLS{CABundle: &common.CABundle{SecretRef: &common.KeyReference{Name: "ca-secret"}}}
	})
	team := cloudTeam(func(s *testapi.CloudWidgetServer) {
		withCreds(s)
		s.Spec.AdminCredentialsSecretRef = &common.AdminSecretRef{Name: "admin"}
		s.Spec.TLS = &common.TLS{CABundle: &common.CABundle{ConfigMapRef: &common.KeyReference{Name: "ca-map"}}}
	})
	bare := cloudTeam(func(s *testapi.CloudWidgetServer) { s.Name, s.Namespace = "bare", "team-b" })
	obj := func(o client.Object, ns, name string) client.Object {
		o.SetNamespace(ns)
		o.SetName(name)
		return o
	}
	names := func(reqs []reconcile.Request) []string {
		var out []string
		for _, r := range reqs {
			out = append(out, r.String())
		}
		return out
	}
	tests := []struct {
		name string
		o    client.Object
		want []string
	}{
		{"the admin Secret", obj(&corev1.Secret{}, "team-a", "admin"), []string{"team-a/team"}},
		{"the credentials Secret", obj(&corev1.Secret{}, "team-a", "creds"), []string{"team-a/team"}},
		{"the same name elsewhere", obj(&corev1.Secret{}, "team-c", "creds"), nil},
		{"the CA ConfigMap", obj(&corev1.ConfigMap{}, "team-a", "ca-map"), []string{"team-a/team"}},
		{"a ConfigMap named like a Secret", obj(&corev1.ConfigMap{}, "team-a", "creds"), nil},
		{"an unrelated Secret", obj(&corev1.Secret{}, "team-a", "other"), nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := newClient(t, interceptor.Funcs{}, team, bare)
			got, err := cloudReconciler(c, &cloudStub{}, opts(&clock{t0})).Dependents(context.Background(), tt.o)
			if err != nil || !slices.Equal(names(got), tt.want) {
				t.Errorf("got %v, err = %v, want %v", names(got), err, tt.want)
			}
		})
	}
	t.Run("a cluster server and its CA Secret", func(t *testing.T) {
		r := cloudReconciler(newClient(t, interceptor.Funcs{}, cluster, bare), &cloudStub{}, opts(&clock{t0}))
		r.NewCloud = func() probe.CloudServer { return &testapi.ClusterCloudWidgetServer{} }
		for _, name := range []string{"admin", "creds", "ca-secret"} {
			got, err := r.Dependents(context.Background(), obj(&corev1.Secret{}, opNS, name))
			if err != nil || !slices.Equal(names(got), []string{"/shared"}) {
				t.Errorf("%s: got %v, err = %v", name, names(got), err)
			}
		}
		if got, _ := r.Dependents(context.Background(), obj(&corev1.ConfigMap{}, opNS, "ca-map")); got != nil {
			t.Errorf("matched %v", got)
		}
	})
	t.Run("a TLS block without a bundle", func(t *testing.T) {
		srv := cloudTeam(func(s *testapi.CloudWidgetServer) { s.Spec.TLS = &common.TLS{} })
		got, err := cloudReconciler(newClient(t, interceptor.Funcs{}, srv), &cloudStub{}, opts(&clock{t0})).
			Dependents(context.Background(), obj(&corev1.Secret{}, "team-a", "ca-secret"))
		if err != nil || got != nil {
			t.Errorf("got %v, err = %v", got, err)
		}
	})
	t.Run("an object that is not a server of the kind uses nothing", func(t *testing.T) {
		if probe.CloudUsesForTest(&corev1.Secret{}, &corev1.Secret{}) || probe.ServerUsesForTest(&corev1.Secret{}, &corev1.Secret{}) {
			t.Error("a Secret matched as a server")
		}
	})
}
