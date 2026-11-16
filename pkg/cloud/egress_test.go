package cloud

import (
	"crypto/x509"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"testing"

	"github.com/craftainer-labs/lib-operator-go/pkg/egress"
)

func TestNewEgressUsesTheModesPolicy(t *testing.T) {
	strict := NewEgress(egress.ModeStrict, nil)
	if err := strict.Policy.Check(netip.MustParseAddr("10.1.2.3")); !errors.Is(err, egress.ErrBlocked) {
		t.Errorf("strict allows a private address: %v", err)
	}
	open := NewEgress(egress.ModeAllowPrivate, nil)
	if err := open.Policy.Check(netip.MustParseAddr("10.1.2.3")); err != nil {
		t.Errorf("allow-private refuses a private address: %v", err)
	}
	if open.Mode != egress.ModeAllowPrivate {
		t.Errorf("Mode = %q", open.Mode)
	}
}

func TestHTTPClientDialsThroughThePolicy(t *testing.T) {
	srv, h := newServer(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })

	// The Policy blocks loopback in both modes: an emulator needs a private address.
	for _, mode := range []egress.Mode{egress.ModeStrict, egress.ModeAllowPrivate} {
		c, err := NewEgress(mode, nil).HTTPClient()
		if err != nil {
			t.Fatal(err)
		}
		if err := getErr(c, srv.URL); !errors.Is(err, egress.ErrBlocked) {
			t.Errorf("%s: Get(loopback) = %v, want ErrBlocked", mode, err)
		}
	}
	if h.count() != 0 {
		t.Errorf("the blocked requests reached the server %d times", h.count())
	}

	c, err := openEgress().HTTPClient()
	if err != nil {
		t.Fatal(err)
	}
	resp, err := c.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent || h.count() != 1 {
		t.Errorf("open Policy: status %d, hits %d", resp.StatusCode, h.count())
	}
}

func TestHTTPClientIgnoresTheEnvironmentProxy(t *testing.T) {
	tr, err := openEgress().transport()
	if err != nil {
		t.Fatal(err)
	}
	if tr.Proxy != nil {
		t.Error("the transport reads a proxy from the environment")
	}
}

func TestProxyWithCredentialsIsRefused(t *testing.T) {
	e := Egress{Mode: egress.ModeAllowPrivate, Proxy: &url.URL{Scheme: "http", Host: "10.0.0.1:3128", User: url.UserPassword("u", "p")}}
	if _, err := e.HTTPClient(); !errors.Is(err, egress.ErrProxyUserinfo) {
		t.Errorf("HTTPClient = %v", err)
	}
	cluster := Credentials{Source: SourceSecret, SecretRef: &SecretRef{Name: "s"}}
	if _, err := e.AWS(t.Context(), ScopeCluster, AWSClusterBlock{}, &cluster, nil); !errors.Is(err, egress.ErrProxyUserinfo) {
		t.Errorf("AWS = %v", err)
	}
	if _, err := e.GCP(t.Context(), ScopeCluster, GCPClusterBlock{}, &cluster, nil); !errors.Is(err, egress.ErrProxyUserinfo) {
		t.Errorf("GCP = %v", err)
	}
	if _, err := e.Azure(t.Context(), ScopeCluster, AzureClusterBlock{}, &cluster, nil); !errors.Is(err, egress.ErrProxyUserinfo) {
		t.Errorf("Azure = %v", err)
	}
}

func TestEndpoint(t *testing.T) {
	tests := []struct {
		name  string
		e     Egress
		scope Scope
		raw   string
		want  string
		err   error
	}{
		{"none", NewEgress(egress.ModeStrict, nil), ScopeCluster, "", "", nil},
		{"none on a namespaced server", NewEgress(egress.ModeStrict, nil), ScopeNamespaced, "", "", nil},
		{"strict mode", NewEgress(egress.ModeStrict, nil), ScopeCluster, "http://10.1.2.3:4566", "", ErrEndpointOverrideForbidden},
		{"namespaced server", openEgress(), ScopeNamespaced, "http://10.1.2.3:4566", "", ErrEndpointOverrideForbidden},
		{"unknown scope", openEgress(), "", "http://10.1.2.3:4566", "", ErrEndpointOverrideForbidden},
		{"allowed", openEgress(), ScopeCluster, "http://10.1.2.3:4566", "http://10.1.2.3:4566", nil},
		{"https", openEgress(), ScopeCluster, "https://emulator.test", "https://emulator.test", nil},
		{"scheme", openEgress(), ScopeCluster, "ftp://10.1.2.3", "", ErrInvalidEndpoint},
		{"no host", openEgress(), ScopeCluster, "http://", "", ErrInvalidEndpoint},
		{"user information", openEgress(), ScopeCluster, "http://u:p@10.1.2.3", "", ErrInvalidEndpoint},
		{"unparsable", openEgress(), ScopeCluster, "http://[::1", "", ErrInvalidEndpoint},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tt.e.endpoint(tt.scope, tt.raw)
			if !errors.Is(err, tt.err) || got != tt.want {
				t.Errorf("endpoint = %q, %v; want %q, %v", got, err, tt.want, tt.err)
			}
			if err != nil {
				noSecret(t, err, "u:p", "10.1.2.3")
			}
		})
	}
}

func TestSource(t *testing.T) {
	tests := []struct {
		name  string
		scope Scope
		c     *Credentials
		want  CredentialsSource
		err   error
	}{
		{"absent is WorkloadIdentity", ScopeCluster, nil, SourceWorkloadIdentity, nil},
		{"empty source is WorkloadIdentity", ScopeCluster, &Credentials{}, SourceWorkloadIdentity, nil},
		{"secret", ScopeCluster, &Credentials{Source: SourceSecret}, SourceSecret, nil},
		{"secret on a namespaced server", ScopeNamespaced, &Credentials{Source: SourceSecret}, SourceSecret, nil},
		{"namespaced absent", ScopeNamespaced, nil, "", ErrWorkloadIdentityForbidden},
		{"namespaced WorkloadIdentity", ScopeNamespaced, &Credentials{Source: SourceWorkloadIdentity}, "", ErrWorkloadIdentityForbidden},
		{"unknown scope", "", nil, "", ErrWorkloadIdentityForbidden},
		{"unknown source", ScopeCluster, &Credentials{Source: "Token"}, "", ErrCredentialsUnavailable},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := source(tt.scope, tt.c)
			if !errors.Is(err, tt.err) || got != tt.want {
				t.Errorf("source = %q, %v; want %q, %v", got, err, tt.want, tt.err)
			}
		})
	}
}

func TestResolversRefuseBeforeAnyWork(t *testing.T) {
	override := "http://10.1.2.3:4566"
	strict := NewEgress(egress.ModeStrict, nil)
	secret := &Credentials{Source: SourceSecret, SecretRef: &SecretRef{Name: "s"}}
	ctx := t.Context()
	if _, err := strict.AWS(ctx, ScopeCluster, AWSClusterBlock{EndpointOverride: override}, secret, nil); !errors.Is(err, ErrEndpointOverrideForbidden) {
		t.Errorf("AWS strict override = %v", err)
	}
	if _, err := strict.GCP(ctx, ScopeCluster, GCPClusterBlock{EndpointOverride: override}, secret, nil); !errors.Is(err, ErrEndpointOverrideForbidden) {
		t.Errorf("GCP strict override = %v", err)
	}
	if _, err := strict.Azure(ctx, ScopeCluster, AzureClusterBlock{EndpointOverride: override}, secret, nil); !errors.Is(err, ErrEndpointOverrideForbidden) {
		t.Errorf("Azure strict override = %v", err)
	}
	if _, err := strict.AWS(ctx, ScopeNamespaced, AWSClusterBlock{}, nil, nil); !errors.Is(err, ErrWorkloadIdentityForbidden) {
		t.Errorf("AWS namespaced WorkloadIdentity = %v", err)
	}
	if _, err := strict.GCP(ctx, ScopeNamespaced, GCPClusterBlock{}, nil, nil); !errors.Is(err, ErrWorkloadIdentityForbidden) {
		t.Errorf("GCP namespaced WorkloadIdentity = %v", err)
	}
	if _, err := strict.Azure(ctx, ScopeNamespaced, AzureClusterBlock{}, nil, nil); !errors.Is(err, ErrWorkloadIdentityForbidden) {
		t.Errorf("Azure namespaced WorkloadIdentity = %v", err)
	}
}

func TestNamespacedCredentialsAsCredentials(t *testing.T) {
	var none *NamespacedCredentials
	if none.AsCredentials() != nil {
		t.Error("nil must stay nil")
	}
	ref := &SecretRef{Name: "s"}
	got := (&NamespacedCredentials{Source: SourceSecret, SecretRef: ref}).AsCredentials()
	if got.Source != SourceSecret || got.SecretRef != ref {
		t.Errorf("AsCredentials = %+v", got)
	}
}

func TestRedirects(t *testing.T) {
	other, otherHits := newServer(t, func(http.ResponseWriter, *http.Request) {})
	var self *httptest.Server
	self, _ = newServerRaw(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/same":
			http.Redirect(w, r, "/done", http.StatusFound)
		case "/other":
			http.Redirect(w, r, other.URL, http.StatusFound)
		case "/loop":
			http.Redirect(w, r, "/loop", http.StatusFound)
		case "/downgrade":
			http.Redirect(w, r, "http://"+self.Listener.Addr().String()+"/done", http.StatusFound)
		}
	}, true)
	e := openEgress()
	e.RootCAs = x509.NewCertPool()
	e.RootCAs.AddCert(self.Certificate())
	c, err := e.HTTPClient()
	if err != nil {
		t.Fatal(err)
	}
	if err := getErr(c, self.URL+"/same"); err != nil {
		t.Errorf("same-host redirect: %v", err)
	}
	for _, path := range []string{"/other", "/loop", "/downgrade"} {
		if err := getErr(c, self.URL+path); !errors.Is(err, errRedirectRefused) {
			t.Errorf("%s: %v", path, err)
		}
	}
	if otherHits.count() != 0 {
		t.Errorf("the redirect target was reached %d times", otherHits.count())
	}
}
