package server_test

import (
	"context"
	"crypto/tls"
	"errors"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/craftainer-labs/lib-operator-go/pkg/apis/common"
	"github.com/craftainer-labs/lib-operator-go/pkg/internal/testapi"
	"github.com/craftainer-labs/lib-operator-go/pkg/server"
)

const opNS = "craftainer-system"

func testScheme(t *testing.T) *runtime.Scheme {
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

// mapper marks ClusterWidgetServer as cluster scoped; the fake client treats
// every kind as namespaced unless told otherwise.
func mapper() meta.RESTMapper {
	m := meta.NewDefaultRESTMapper([]schema.GroupVersion{testapi.GroupVersion})
	m.Add(testapi.GroupVersion.WithKind("ClusterWidgetServer"), meta.RESTScopeRoot)
	m.Add(testapi.GroupVersion.WithKind("WidgetServer"), meta.RESTScopeNamespace)
	return m
}

func newClient(t *testing.T, funcs *interceptor.Funcs, objs ...client.Object) client.Client {
	t.Helper()
	b := fake.NewClientBuilder().WithScheme(testScheme(t)).WithRESTMapper(mapper()).WithObjects(objs...)
	if funcs != nil {
		b = b.WithInterceptorFuncs(*funcs)
	}
	return b.Build()
}

func ns(name string, labels map[string]string) *corev1.Namespace {
	return &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name, Labels: labels}}
}

func secret(namespace, name string, data map[string][]byte) *corev1.Secret {
	return &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name}, Data: data}
}

func adminSecret(namespace string) *corev1.Secret {
	return secret(namespace, "admin", map[string][]byte{"username": []byte("root"), "password": []byte("pw")})
}

func clusterServer(mut func(*testapi.ClusterWidgetServer)) *testapi.ClusterWidgetServer {
	s := &testapi.ClusterWidgetServer{
		ObjectMeta: metav1.ObjectMeta{Name: "shared"},
		Spec: testapi.ClusterWidgetServerSpec{
			Endpoint:          "https://backend.example:5432",
			AllowedNamespaces: common.AllowedNamespaces{From: common.NamespacesAll},
		},
	}
	s.Spec.AdminCredentialsSecretRef = common.AdminSecretRef{Name: "admin", Namespace: opNS}
	if mut != nil {
		mut(s)
	}
	return s
}

func namespacedServer(mut func(*testapi.WidgetServer)) *testapi.WidgetServer {
	s := &testapi.WidgetServer{
		ObjectMeta: metav1.ObjectMeta{Name: "team", Namespace: "team-a"},
		Spec:       testapi.WidgetServerSpec{Endpoint: "https://team.example"},
	}
	s.Spec.AdminCredentialsSecretRef = common.AdminSecretRef{Name: "admin"}
	if mut != nil {
		mut(s)
	}
	return s
}

var (
	clusterRef    = server.Ref{Kind: "ClusterWidgetServer", Name: "shared"}
	namespacedRef = server.Ref{Kind: "WidgetServer", Name: "team"}
)

func TestResolveNamespacedServer(t *testing.T) {
	c := newClient(t, nil, namespacedServer(nil), adminSecret("team-a"))
	conn, err := server.Resolve(context.Background(), c, "team-a", namespacedRef)
	if err != nil {
		t.Fatal(err)
	}
	if conn.Endpoint != "https://team.example" || string(conn.Admin["username"]) != "root" || string(conn.Admin["password"]) != "pw" {
		t.Errorf("connection = %+v", conn)
	}
	if conn.MinTLSVersion != tls.VersionTLS12 || conn.HealthCheckInterval != time.Minute ||
		conn.Proxy != nil || conn.CABundle != nil || conn.ServerName != "" {
		t.Errorf("defaults = %+v", conn)
	}
}

func TestResolveNamespacedServerIsOnlyForItsOwnNamespace(t *testing.T) {
	c := newClient(t, nil, namespacedServer(nil), adminSecret("team-a"), adminSecret("team-b"))
	_, err := server.Resolve(context.Background(), c, "team-b", namespacedRef)
	if !errors.Is(err, server.ErrServerNotFound) {
		t.Errorf("err = %v, want ErrServerNotFound", err)
	}
}

func TestResolveClusterServerAllowedNamespaces(t *testing.T) {
	selector := func(l map[string]string, e ...common.LabelSelectorRequirement) func(*testapi.ClusterWidgetServer) {
		return func(s *testapi.ClusterWidgetServer) {
			s.Spec.AllowedNamespaces = common.AllowedNamespaces{
				From:     common.NamespacesSelector,
				Selector: &common.LabelSelector{MatchLabels: l, MatchExpressions: e},
			}
		}
	}
	byName := common.LabelSelectorRequirement{Key: "kubernetes.io/metadata.name", Operator: "In", Values: []string{"team-a", "team-b"}}
	tests := []struct {
		name    string
		mut     func(*testapi.ClusterWidgetServer)
		wantErr error
	}{
		{"All", nil, nil},
		{"selector by name", selector(nil, byName), nil},
		{"selector by label", selector(map[string]string{"craftainer.dev/db": "allowed"}), nil},
		{"selector miss", selector(map[string]string{"craftainer.dev/db": "denied"}), server.ErrNamespaceNotAllowed},
		{"empty selector allows nothing", selector(nil), server.ErrNamespaceNotAllowed},
		{"no from allows nothing", func(s *testapi.ClusterWidgetServer) { s.Spec.AllowedNamespaces = common.AllowedNamespaces{} }, server.ErrNamespaceNotAllowed},
		{"unknown from allows nothing", func(s *testapi.ClusterWidgetServer) {
			s.Spec.AllowedNamespaces = common.AllowedNamespaces{From: "Same"}
		}, server.ErrNamespaceNotAllowed},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := newClient(t, nil, clusterServer(tt.mut), adminSecret(opNS),
				ns("team-a", map[string]string{"kubernetes.io/metadata.name": "team-a", "craftainer.dev/db": "allowed"}))
			conn, err := server.Resolve(context.Background(), c, "team-a", clusterRef, server.WithOperatorNamespace(opNS))
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("err = %v, want %v", err, tt.wantErr)
			}
			if tt.wantErr == nil && conn.Endpoint != "https://backend.example:5432" {
				t.Errorf("endpoint = %q", conn.Endpoint)
			}
			if tt.wantErr != nil && (conn.Endpoint != "" || conn.Admin != nil) {
				t.Errorf("a refused claim got a connection: %+v", conn)
			}
		})
	}
}

func TestResolveClusterServerNamespaceLookupFails(t *testing.T) {
	selector := func(s *testapi.ClusterWidgetServer) {
		s.Spec.AllowedNamespaces = common.AllowedNamespaces{From: common.NamespacesSelector,
			Selector: &common.LabelSelector{MatchLabels: map[string]string{"a": "b"}}}
	}
	c := newClient(t, nil, clusterServer(selector), adminSecret(opNS)) // no Namespace object
	_, err := server.Resolve(context.Background(), c, "team-a", clusterRef, server.WithOperatorNamespace(opNS))
	if err == nil || errors.Is(err, server.ErrNamespaceNotAllowed) {
		t.Errorf("err = %v, want a lookup error", err)
	}
}

func TestResolveOperatorNamespace(t *testing.T) {
	c := newClient(t, nil, clusterServer(nil), adminSecret(opNS))
	ctx := context.Background()

	t.Setenv(server.OperatorNamespaceEnv, "")
	if _, err := server.Resolve(ctx, c, "team-a", clusterRef); !errors.Is(err, server.ErrOperatorNamespaceUnset) {
		t.Errorf("unset: err = %v", err)
	}
	t.Setenv(server.OperatorNamespaceEnv, opNS)
	if _, err := server.Resolve(ctx, c, "team-a", clusterRef); err != nil {
		t.Errorf("from the environment: err = %v", err)
	}
	// The option wins over the environment.
	t.Setenv(server.OperatorNamespaceEnv, "elsewhere")
	if _, err := server.Resolve(ctx, c, "team-a", clusterRef, server.WithOperatorNamespace(opNS)); err != nil {
		t.Errorf("option: err = %v", err)
	}
}

func TestResolveAdminSecretNamespace(t *testing.T) {
	tests := []struct {
		name     string
		resolve  func(t *testing.T) error
		wantErr  error
		wantUsed bool
	}{
		{"cluster server naming another namespace", func(t *testing.T) error {
			c := newClient(t, nil, clusterServer(func(s *testapi.ClusterWidgetServer) {
				s.Spec.AdminCredentialsSecretRef.Namespace = "team-a" // a tenant's Secret
			}), adminSecret(opNS), adminSecret("team-a"))
			_, err := server.Resolve(context.Background(), c, "team-a", clusterRef, server.WithOperatorNamespace(opNS))
			return err
		}, server.ErrAdminSecretNamespaceForbidden, false},
		{"cluster server leaving it empty means the operator namespace", func(t *testing.T) error {
			c := newClient(t, nil, clusterServer(func(s *testapi.ClusterWidgetServer) {
				s.Spec.AdminCredentialsSecretRef.Namespace = ""
			}), adminSecret(opNS))
			_, err := server.Resolve(context.Background(), c, "team-a", clusterRef, server.WithOperatorNamespace(opNS))
			return err
		}, nil, true},
		{"namespaced server naming another namespace", func(t *testing.T) error {
			c := newClient(t, nil, namespacedServer(func(s *testapi.WidgetServer) {
				s.Spec.AdminCredentialsSecretRef.Namespace = opNS
			}), adminSecret(opNS), adminSecret("team-a"))
			_, err := server.Resolve(context.Background(), c, "team-a", namespacedRef)
			return err
		}, server.ErrAdminSecretNamespaceForbidden, false},
		{"namespaced server naming its own namespace", func(t *testing.T) error {
			c := newClient(t, nil, namespacedServer(func(s *testapi.WidgetServer) {
				s.Spec.AdminCredentialsSecretRef.Namespace = "team-a"
			}), adminSecret("team-a"))
			_, err := server.Resolve(context.Background(), c, "team-a", namespacedRef)
			return err
		}, nil, true},
		{"admin secret missing", func(t *testing.T) error {
			c := newClient(t, nil, namespacedServer(nil))
			_, err := server.Resolve(context.Background(), c, "team-a", namespacedRef)
			return err
		}, nil, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.resolve(t)
			if tt.wantErr != nil && !errors.Is(err, tt.wantErr) {
				t.Errorf("err = %v, want %v", err, tt.wantErr)
			}
			if tt.wantErr == nil && tt.wantUsed && err != nil {
				t.Errorf("err = %v", err)
			}
			if tt.wantErr == nil && !tt.wantUsed && err == nil {
				t.Error("expected an error")
			}
		})
	}
}

func TestResolveAdminKey(t *testing.T) {
	ref := func(key string) func(*testapi.WidgetServer) {
		return func(s *testapi.WidgetServer) { s.Spec.AdminCredentialsSecretRef.Key = key }
	}
	c := newClient(t, nil, namespacedServer(ref("password")), adminSecret("team-a"))
	conn, err := server.Resolve(context.Background(), c, "team-a", namespacedRef)
	if err != nil {
		t.Fatal(err)
	}
	if len(conn.Admin) != 1 || string(conn.Admin["password"]) != "pw" {
		t.Errorf("admin = %v, want only the named key", conn.Admin)
	}
	c = newClient(t, nil, namespacedServer(ref("token")), adminSecret("team-a"))
	if _, err := server.Resolve(context.Background(), c, "team-a", namespacedRef); !errors.Is(err, server.ErrInvalidServer) {
		t.Errorf("missing key: err = %v, want ErrInvalidServer", err)
	}
}

func TestResolveConnectionSettings(t *testing.T) {
	mut := func(s *testapi.WidgetServer) {
		s.Spec.TLS = &common.TLS{ServerName: "db.internal", MinVersion: common.TLS13,
			CABundle: &common.CABundle{SecretRef: &common.KeyReference{Name: "ca", Key: "ca.crt"}}}
		s.Spec.ProxyURL = "socks5://proxy.internal:1080"
		s.Spec.HealthCheckInterval = &metav1.Duration{Duration: 5 * time.Minute}
	}
	c := newClient(t, nil, namespacedServer(mut), adminSecret("team-a"),
		secret("team-a", "ca", map[string][]byte{"ca.crt": []byte("PEM")}))
	conn, err := server.Resolve(context.Background(), c, "team-a", namespacedRef)
	if err != nil {
		t.Fatal(err)
	}
	if conn.ServerName != "db.internal" || conn.MinTLSVersion != tls.VersionTLS13 || string(conn.CABundle) != "PEM" ||
		conn.Proxy == nil || conn.Proxy.Host != "proxy.internal:1080" || conn.HealthCheckInterval != 5*time.Minute {
		t.Errorf("connection = %+v", conn)
	}
	conn.CABundle[0] = 'X'
	conn.Admin["username"][0] = 'X'
	again, _ := server.Resolve(context.Background(), c, "team-a", namespacedRef)
	if string(again.CABundle) != "PEM" || string(again.Admin["username"]) != "root" {
		t.Error("Resolve hands out shared slices")
	}
}

func TestResolveMinVersionDefaultsAndRejects(t *testing.T) {
	for version, want := range map[common.TLSVersion]uint16{"": tls.VersionTLS12, common.TLS12: tls.VersionTLS12} {
		c := newClient(t, nil, namespacedServer(func(s *testapi.WidgetServer) {
			s.Spec.TLS = &common.TLS{MinVersion: version}
		}), adminSecret("team-a"))
		conn, err := server.Resolve(context.Background(), c, "team-a", namespacedRef)
		if err != nil || conn.MinTLSVersion != want {
			t.Errorf("version %q: %v, %v", version, conn.MinTLSVersion, err)
		}
	}
	c := newClient(t, nil, namespacedServer(func(s *testapi.WidgetServer) {
		s.Spec.TLS = &common.TLS{MinVersion: "1.1"}
	}), adminSecret("team-a"))
	if _, err := server.Resolve(context.Background(), c, "team-a", namespacedRef); !errors.Is(err, server.ErrInvalidServer) {
		t.Errorf("TLS 1.1: err = %v, want ErrInvalidServer", err)
	}
}

func TestResolveHealthCheckBounds(t *testing.T) {
	for in, want := range map[time.Duration]time.Duration{
		time.Second: 10 * time.Second, 10 * time.Second: 10 * time.Second,
		30 * time.Minute: 30 * time.Minute, 48 * time.Hour: time.Hour,
	} {
		c := newClient(t, nil, namespacedServer(func(s *testapi.WidgetServer) {
			s.Spec.HealthCheckInterval = &metav1.Duration{Duration: in}
		}), adminSecret("team-a"))
		conn, err := server.Resolve(context.Background(), c, "team-a", namespacedRef)
		if err != nil || conn.HealthCheckInterval != want {
			t.Errorf("interval %v: got %v, %v; want %v", in, conn.HealthCheckInterval, err, want)
		}
	}
}

func TestResolveCABundleSources(t *testing.T) {
	withCA := func(b *common.CABundle) func(*testapi.WidgetServer) {
		return func(s *testapi.WidgetServer) { s.Spec.TLS = &common.TLS{CABundle: b} }
	}
	cm := func(data map[string]string, bin map[string][]byte) *corev1.ConfigMap {
		return &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: "team-a", Name: "ca"}, Data: data, BinaryData: bin}
	}
	secretRef := &common.KeyReference{Name: "ca", Key: "ca.crt"}
	cmRef := &common.KeyReference{Name: "ca", Key: "ca.crt"}
	tests := []struct {
		name    string
		bundle  *common.CABundle
		objs    []client.Object
		want    string
		wantErr bool
		invalid bool
	}{
		{"no bundle", nil, nil, "", false, false},
		{"configmap text", &common.CABundle{ConfigMapRef: cmRef}, []client.Object{cm(map[string]string{"ca.crt": "TEXT"}, nil)}, "TEXT", false, false},
		{"configmap binary", &common.CABundle{ConfigMapRef: cmRef}, []client.Object{cm(nil, map[string][]byte{"ca.crt": []byte("BIN")})}, "BIN", false, false},
		{"configmap key missing", &common.CABundle{ConfigMapRef: cmRef}, []client.Object{cm(map[string]string{"other": "x"}, nil)}, "", true, true},
		{"configmap missing", &common.CABundle{ConfigMapRef: cmRef}, nil, "", true, false},
		{"secret key missing", &common.CABundle{SecretRef: secretRef}, []client.Object{secret("team-a", "ca", map[string][]byte{"other": []byte("x")})}, "", true, true},
		{"secret missing", &common.CABundle{SecretRef: secretRef}, nil, "", true, false},
		{"neither reference", &common.CABundle{}, nil, "", true, true},
		{"both references", &common.CABundle{SecretRef: secretRef, ConfigMapRef: cmRef}, nil, "", true, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			objs := append([]client.Object{namespacedServer(withCA(tt.bundle)), adminSecret("team-a")}, tt.objs...)
			c := newClient(t, nil, objs...)
			conn, err := server.Resolve(context.Background(), c, "team-a", namespacedRef)
			if (err != nil) != tt.wantErr || errors.Is(err, server.ErrInvalidServer) != tt.invalid {
				t.Fatalf("err = %v, wantErr %v, invalid %v", err, tt.wantErr, tt.invalid)
			}
			if string(conn.CABundle) != tt.want {
				t.Errorf("CABundle = %q, want %q", conn.CABundle, tt.want)
			}
		})
	}
}

func TestResolveProxy(t *testing.T) {
	for proxy, wantErr := range map[string]bool{
		"http://p:3128": false, "https://p:3128": false, "socks5://p:1080": false,
		"ftp://p:21": true, "http://": true, "http://[::1": true, "//p": true,
	} {
		c := newClient(t, nil, namespacedServer(func(s *testapi.WidgetServer) { s.Spec.ProxyURL = proxy }), adminSecret("team-a"))
		_, err := server.Resolve(context.Background(), c, "team-a", namespacedRef)
		if (err != nil) != wantErr || (err != nil && !errors.Is(err, server.ErrInvalidServer)) {
			t.Errorf("proxy %q: err = %v, wantErr %v", proxy, err, wantErr)
		}
	}
}

func TestResolveErrors(t *testing.T) {
	ctx := context.Background()
	t.Run("server not found", func(t *testing.T) {
		c := newClient(t, nil)
		for _, ref := range []server.Ref{clusterRef, namespacedRef} {
			if _, err := server.Resolve(ctx, c, "team-a", ref, server.WithOperatorNamespace(opNS)); !errors.Is(err, server.ErrServerNotFound) {
				t.Errorf("%s: err = %v", ref.Kind, err)
			}
		}
	})
	t.Run("unknown kind", func(t *testing.T) {
		c := newClient(t, nil)
		if _, err := server.Resolve(ctx, c, "team-a", server.Ref{Kind: "Nope", Name: "x"}); !errors.Is(err, server.ErrUnknownServerKind) {
			t.Errorf("err = %v", err)
		}
	})
	t.Run("a registered kind that is not a server", func(t *testing.T) {
		c := newClient(t, nil)
		if _, err := server.Resolve(ctx, c, "team-a", server.Ref{Kind: "Widget", Name: "x"}); !errors.Is(err, server.ErrUnknownServerKind) {
			t.Errorf("err = %v", err)
		}
	})
	t.Run("scope unknown to the REST mapper", func(t *testing.T) {
		c := fake.NewClientBuilder().WithScheme(testScheme(t)).
			WithRESTMapper(meta.NewDefaultRESTMapper(nil)).Build()
		if _, err := server.Resolve(ctx, c, "team-a", namespacedRef); err == nil || errors.Is(err, server.ErrServerNotFound) {
			t.Errorf("err = %v", err)
		}
	})
	t.Run("API error on get", func(t *testing.T) {
		boom := errors.New("boom")
		c := newClient(t, &interceptor.Funcs{Get: func(context.Context, client.WithWatch, client.ObjectKey, client.Object, ...client.GetOption) error {
			return boom
		}})
		if _, err := server.Resolve(ctx, c, "team-a", namespacedRef); !errors.Is(err, boom) || errors.Is(err, server.ErrServerNotFound) {
			t.Errorf("err = %v", err)
		}
	})
}
