package server_test

import (
	"context"
	"errors"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/craftainer-labs/lib-operator-go/pkg/apis/common"
	"github.com/craftainer-labs/lib-operator-go/pkg/internal/testapi"
	"github.com/craftainer-labs/lib-operator-go/pkg/server"
)

var (
	cloudClusterRef    = server.Ref{Kind: "ClusterCloudWidgetServer", Name: "cloud"}
	cloudNamespacedRef = server.Ref{Kind: "CloudWidgetServer", Name: "team"}
)

// The test kinds implement the cloud interface and, being a different shape,
// not the self-hosted one.
var _ server.CloudServer = (*testapi.ClusterCloudWidgetServer)(nil)
var _ server.CloudServer = (*testapi.CloudWidgetServer)(nil)

func cloudCluster(mut func(*testapi.ClusterCloudWidgetServer)) *testapi.ClusterCloudWidgetServer {
	s := &testapi.ClusterCloudWidgetServer{
		ObjectMeta: metav1.ObjectMeta{Name: "cloud"},
		Spec: testapi.ClusterCloudWidgetServerSpec{
			AllowedNamespaces: common.AllowedNamespaces{From: common.NamespacesAll},
		},
	}
	if mut != nil {
		mut(s)
	}
	return s
}

func cloudNamespaced(mut func(*testapi.CloudWidgetServer)) *testapi.CloudWidgetServer {
	s := &testapi.CloudWidgetServer{ObjectMeta: metav1.ObjectMeta{Name: "team", Namespace: "team-a"}}
	if mut != nil {
		mut(s)
	}
	return s
}

func credsSecret(namespace string) client.Object {
	return secret(namespace, "creds", map[string][]byte{"accessKeyId": []byte("AK"), "secretAccessKey": []byte("SK")})
}

func TestResolveCloudNeedsNoAdminSecret(t *testing.T) {
	ctx := context.Background()
	t.Run("workload identity, cluster server", func(t *testing.T) {
		c := newClient(t, nil, cloudCluster(func(s *testapi.ClusterCloudWidgetServer) { s.Spec.Endpoint = "https://db.example:5432" }))
		conn, err := server.ResolveCloud(ctx, c, "team-a", cloudClusterRef, server.WithOperatorNamespace(opNS))
		if err != nil {
			t.Fatal(err)
		}
		if conn.Endpoint != "https://db.example:5432" || len(conn.Admin) != 0 || conn.Credentials != nil ||
			conn.HealthCheckInterval != time.Minute {
			t.Errorf("conn = %+v", conn)
		}
	})
	t.Run("a Secret credential, namespaced server", func(t *testing.T) {
		c := newClient(t, nil, cloudNamespaced(func(s *testapi.CloudWidgetServer) {
			s.Spec.CredentialsSecret = &common.AdminSecretRef{Name: "creds"}
		}), credsSecret("team-a"))
		conn, err := server.ResolveCloud(ctx, c, "team-a", cloudNamespacedRef)
		if err != nil || string(conn.Credentials["secretAccessKey"]) != "SK" || len(conn.Admin) != 0 {
			t.Errorf("conn = %+v, err = %v", conn, err)
		}
	})
	t.Run("an own-namespace credentials Secret may name its namespace", func(t *testing.T) {
		c := newClient(t, nil, cloudNamespaced(func(s *testapi.CloudWidgetServer) {
			s.Spec.CredentialsSecret = &common.AdminSecretRef{Name: "creds", Namespace: "team-a"}
		}), credsSecret("team-a"))
		if _, err := server.ResolveCloud(ctx, c, "team-a", cloudNamespacedRef); err != nil {
			t.Error(err)
		}
	})
	t.Run("an admin Secret is read only when the reference is set", func(t *testing.T) {
		c := newClient(t, nil, cloudCluster(func(s *testapi.ClusterCloudWidgetServer) {
			s.Spec.AdminCredentialsSecretRef = &common.AdminSecretRef{Name: "admin", Namespace: opNS, Key: "password"}
			s.Spec.CredentialsSecret = &common.AdminSecretRef{Name: "creds", Namespace: opNS}
		}), adminSecret(opNS), credsSecret(opNS))
		conn, err := server.ResolveCloud(ctx, c, "team-a", cloudClusterRef, server.WithOperatorNamespace(opNS))
		if err != nil || string(conn.Admin["password"]) != "pw" || len(conn.Admin) != 1 || string(conn.Credentials["accessKeyId"]) != "AK" {
			t.Errorf("conn = %+v, err = %v", conn, err)
		}
	})
	t.Run("a missing admin Secret that is named is an error", func(t *testing.T) {
		c := newClient(t, nil, cloudCluster(func(s *testapi.ClusterCloudWidgetServer) {
			s.Spec.AdminCredentialsSecretRef = &common.AdminSecretRef{Name: "admin", Namespace: opNS}
		}))
		_, err := server.ResolveCloud(ctx, c, "team-a", cloudClusterRef, server.WithOperatorNamespace(opNS))
		if !apierrors.IsNotFound(err) || errors.Is(err, server.ErrCredentialsUnavailable) {
			t.Errorf("err = %v", err)
		}
	})
}

func TestResolveCloudEnforcesAllowedNamespaces(t *testing.T) {
	ctx := context.Background()
	selector := func(s *testapi.ClusterCloudWidgetServer) {
		s.Spec.AllowedNamespaces = common.AllowedNamespaces{From: common.NamespacesSelector, Selector: &common.LabelSelector{
			MatchLabels: map[string]string{"team": "a"},
		}}
	}
	cases := []struct {
		name string
		mut  func(*testapi.ClusterCloudWidgetServer)
		ns   string
		want error
	}{
		{"all", nil, "team-a", nil},
		{"selector matches", selector, "team-a", nil},
		{"selector does not match", selector, "team-b", server.ErrNamespaceNotAllowed},
		{"no allowedNamespaces fails closed", func(s *testapi.ClusterCloudWidgetServer) { s.Spec.AllowedNamespaces = common.AllowedNamespaces{} }, "team-a", server.ErrNamespaceNotAllowed},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := newClient(t, nil, cloudCluster(tc.mut), ns("team-a", map[string]string{"team": "a"}), ns("team-b", nil))
			_, err := server.ResolveCloud(ctx, c, tc.ns, cloudClusterRef, server.WithOperatorNamespace(opNS))
			if !errors.Is(err, tc.want) {
				t.Errorf("err = %v, want %v", err, tc.want)
			}
		})
	}
	t.Run("a denied claim never reads a Secret", func(t *testing.T) {
		reads := 0
		c := newClient(t, &interceptor.Funcs{Get: func(ctx context.Context, cl client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			if _, ok := obj.(*testapi.ClusterCloudWidgetServer); !ok {
				reads++
			}
			return cl.Get(ctx, key, obj, opts...)
		}}, cloudCluster(func(s *testapi.ClusterCloudWidgetServer) {
			selector(s)
			s.Spec.CredentialsSecret = &common.AdminSecretRef{Name: "creds", Namespace: opNS}
		}), credsSecret(opNS), ns("team-b", nil))
		if _, err := server.ResolveCloud(ctx, c, "team-b", cloudClusterRef, server.WithOperatorNamespace(opNS)); !errors.Is(err, server.ErrNamespaceNotAllowed) {
			t.Fatalf("err = %v", err)
		}
		if reads != 1 { // only the claim's Namespace, for the selector
			t.Errorf("non-server reads = %d, want 1", reads)
		}
	})
	t.Run("a namespaced server is only for its own namespace", func(t *testing.T) {
		c := newClient(t, nil, cloudNamespaced(nil))
		if _, err := server.ResolveCloud(ctx, c, "team-b", cloudNamespacedRef); !errors.Is(err, server.ErrServerNotFound) {
			t.Errorf("err = %v", err)
		}
	})
}

func TestResolveCloudErrors(t *testing.T) {
	ctx := context.Background()
	t.Run("unknown kind and a self-hosted kind", func(t *testing.T) {
		c := newClient(t, nil, namespacedServer(nil))
		for _, kind := range []string{"Nope", "WidgetServer"} {
			if _, err := server.ResolveCloud(ctx, c, "team-a", server.Ref{Kind: kind, Name: "team"}); !errors.Is(err, server.ErrUnknownServerKind) {
				t.Errorf("%s: err = %v", kind, err)
			}
		}
	})
	t.Run("the self-hosted lookup still finds its kind after a cloud lookup", func(t *testing.T) {
		c := newClient(t, nil, cloudNamespaced(nil), namespacedServer(nil), adminSecret("team-a"))
		if _, err := server.ResolveCloud(ctx, c, "team-a", cloudNamespacedRef); err != nil {
			t.Fatal(err)
		}
		if _, err := server.ResolveCloud(ctx, c, "team-a", cloudNamespacedRef); err != nil { // cached
			t.Fatal(err)
		}
		if _, err := server.Resolve(ctx, c, "team-a", cloudNamespacedRef); !errors.Is(err, server.ErrUnknownServerKind) {
			t.Errorf("a cloud kind is not a self-hosted Server: %v", err)
		}
		if _, err := server.Resolve(ctx, c, "team-a", namespacedRef); err != nil {
			t.Error(err)
		}
	})
	t.Run("scope unknown to the REST mapper", func(t *testing.T) {
		c := fake.NewClientBuilder().WithScheme(testScheme(t)).WithRESTMapper(meta.NewDefaultRESTMapper(nil)).Build()
		if _, err := server.ResolveCloud(ctx, c, "team-a", cloudNamespacedRef); err == nil || errors.Is(err, server.ErrServerNotFound) {
			t.Errorf("err = %v", err)
		}
	})
	t.Run("server not found, API error", func(t *testing.T) {
		if _, err := server.ResolveCloud(ctx, newClient(t, nil), "team-a", cloudNamespacedRef); !errors.Is(err, server.ErrServerNotFound) {
			t.Errorf("err = %v", err)
		}
		boom := errors.New("boom")
		c := newClient(t, &interceptor.Funcs{Get: func(context.Context, client.WithWatch, client.ObjectKey, client.Object, ...client.GetOption) error {
			return boom
		}})
		if _, err := server.ResolveCloud(ctx, c, "team-a", cloudNamespacedRef); !errors.Is(err, boom) {
			t.Errorf("err = %v", err)
		}
	})
	t.Run("operator namespace unset", func(t *testing.T) {
		t.Setenv(server.OperatorNamespaceEnv, "")
		c := newClient(t, nil, cloudCluster(nil))
		if _, err := server.ResolveCloud(ctx, c, "team-a", cloudClusterRef); !errors.Is(err, server.ErrOperatorNamespaceUnset) {
			t.Errorf("err = %v", err)
		}
	})
}

func TestCloudCredentialsSecret(t *testing.T) {
	ctx := context.Background()
	t.Run("a cluster server may not name another namespace", func(t *testing.T) {
		c := newClient(t, nil, cloudCluster(func(s *testapi.ClusterCloudWidgetServer) {
			s.Spec.CredentialsSecret = &common.AdminSecretRef{Name: "creds", Namespace: "tenant"}
		}), credsSecret("tenant"))
		_, err := server.ResolveCloud(ctx, c, "team-a", cloudClusterRef, server.WithOperatorNamespace(opNS))
		if !errors.Is(err, server.ErrCredentialsSecretNamespaceForbidden) {
			t.Errorf("err = %v", err)
		}
	})
	t.Run("a namespaced server may not name another namespace", func(t *testing.T) {
		c := newClient(t, nil, cloudNamespaced(func(s *testapi.CloudWidgetServer) {
			s.Spec.CredentialsSecret = &common.AdminSecretRef{Name: "creds", Namespace: opNS}
		}), credsSecret(opNS))
		_, err := server.ResolveCloud(ctx, c, "team-a", cloudNamespacedRef)
		if !errors.Is(err, server.ErrCredentialsSecretNamespaceForbidden) {
			t.Errorf("err = %v", err)
		}
	})
	t.Run("a missing Secret is unavailable, and still a not-found error", func(t *testing.T) {
		c := newClient(t, nil, cloudNamespaced(func(s *testapi.CloudWidgetServer) {
			s.Spec.CredentialsSecret = &common.AdminSecretRef{Name: "creds"}
		}))
		_, err := server.ResolveCloud(ctx, c, "team-a", cloudNamespacedRef)
		if !errors.Is(err, server.ErrCredentialsUnavailable) || !apierrors.IsNotFound(err) {
			t.Errorf("err = %v", err)
		}
	})
	t.Run("another read failure is returned as it is", func(t *testing.T) {
		boom := errors.New("boom")
		c := newClient(t, &interceptor.Funcs{Get: func(ctx context.Context, cl client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			if key.Name == "creds" {
				return boom
			}
			return cl.Get(ctx, key, obj, opts...)
		}}, cloudNamespaced(func(s *testapi.CloudWidgetServer) {
			s.Spec.CredentialsSecret = &common.AdminSecretRef{Name: "creds"}
		}))
		_, err := server.ResolveCloud(ctx, c, "team-a", cloudNamespacedRef)
		if !errors.Is(err, boom) || errors.Is(err, server.ErrCredentialsUnavailable) {
			t.Errorf("err = %v", err)
		}
	})
	t.Run("a bad connection setting fails before the credentials are read", func(t *testing.T) {
		c := newClient(t, nil, cloudNamespaced(func(s *testapi.CloudWidgetServer) {
			s.Spec.ProxyURL = "ftp://proxy"
			s.Spec.CredentialsSecret = &common.AdminSecretRef{Name: "creds"}
		}), credsSecret("team-a"))
		if _, err := server.ResolveCloud(ctx, c, "team-a", cloudNamespacedRef); !errors.Is(err, server.ErrInvalidServer) {
			t.Errorf("err = %v", err)
		}
	})
}

func TestConnectionForCloud(t *testing.T) {
	ctx := context.Background()
	t.Setenv(server.OperatorNamespaceEnv, "")

	t.Run("namespaced server with a Secret credential", func(t *testing.T) {
		srv := cloudNamespaced(func(s *testapi.CloudWidgetServer) {
			s.Spec.CredentialsSecret = &common.AdminSecretRef{Name: "creds"}
			s.Spec.HealthCheckInterval = &metav1.Duration{Duration: time.Second}
		})
		c := newClient(t, nil, credsSecret("team-a"))
		conn, err := server.ConnectionForCloud(ctx, c, srv, server.WithHealthCheckInterval(2*time.Minute))
		if err != nil || string(conn.Credentials["accessKeyId"]) != "AK" || len(conn.Admin) != 0 || conn.HealthCheckInterval != 10*time.Second {
			t.Errorf("conn = %+v, err = %v", conn, err)
		}
	})
	t.Run("cluster server, workload identity, the operator namespace from the option", func(t *testing.T) {
		conn, err := server.ConnectionForCloud(ctx, newClient(t, nil), cloudCluster(nil), server.WithOperatorNamespace(opNS))
		if err != nil || conn.Credentials != nil || conn.HealthCheckInterval != time.Minute {
			t.Errorf("conn = %+v, err = %v", conn, err)
		}
	})
	t.Run("an error still carries the interval", func(t *testing.T) {
		conn, err := server.ConnectionForCloud(ctx, newClient(t, nil), cloudCluster(nil), server.WithHealthCheckInterval(3*time.Minute))
		if !errors.Is(err, server.ErrOperatorNamespaceUnset) || conn.Endpoint != "" || conn.HealthCheckInterval != 3*time.Minute {
			t.Errorf("conn = %+v, err = %v", conn, err)
		}
		srv := cloudNamespaced(func(s *testapi.CloudWidgetServer) {
			s.Spec.CredentialsSecret = &common.AdminSecretRef{Name: "creds"}
			s.Spec.Endpoint = "https://x"
		})
		conn, err = server.ConnectionForCloud(ctx, newClient(t, nil), srv, server.WithHealthCheckInterval(4*time.Minute))
		if !errors.Is(err, server.ErrCredentialsUnavailable) || conn.Endpoint != "" || conn.HealthCheckInterval != 4*time.Minute {
			t.Errorf("conn = %+v, err = %v", conn, err)
		}
	})
	t.Run("a namespaced server without a namespace", func(t *testing.T) {
		srv := cloudNamespaced(func(s *testapi.CloudWidgetServer) { s.Namespace = "" })
		if _, err := server.ConnectionForCloud(ctx, newClient(t, nil), srv); !errors.Is(err, server.ErrInvalidServer) {
			t.Errorf("err = %v", err)
		}
	})
	t.Run("scope unknown to the REST mapper", func(t *testing.T) {
		c := fake.NewClientBuilder().WithScheme(testScheme(t)).WithRESTMapper(meta.NewDefaultRESTMapper(nil)).Build()
		if _, err := server.ConnectionForCloud(ctx, c, cloudNamespaced(nil)); err == nil {
			t.Error("want an error")
		}
	})
}

// The self-hosted entry points are unchanged: an admin reference is still
// required to be readable, whatever else a server names.
func TestSelfHostedEntryPointsAreUnchanged(t *testing.T) {
	ctx := context.Background()
	c := newClient(t, nil, namespacedServer(nil))
	if _, err := server.Resolve(ctx, c, "team-a", namespacedRef); !apierrors.IsNotFound(err) {
		t.Errorf("Resolve without the admin Secret: %v", err)
	}
	if _, err := server.ConnectionFor(ctx, c, namespacedServer(nil)); !apierrors.IsNotFound(err) {
		t.Errorf("ConnectionFor without the admin Secret: %v", err)
	}
}
