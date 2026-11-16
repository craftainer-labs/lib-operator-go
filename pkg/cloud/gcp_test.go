package cloud

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/craftainer-labs/lib-operator-go/pkg/egress"
)

// fakeGoogleOAuth answers the service account JWT grant.
func fakeGoogleOAuth(t *testing.T, tokenType string) (url string, h *hits) {
	t.Helper()
	srv, h := newServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		body := `{"access_token":"ya29.fake-access-token","expires_in":3600`
		if tokenType != "" {
			body += `,"token_type":"` + tokenType + `"`
		}
		_, _ = io.WriteString(w, body+"}")
	})
	return srv.URL, h
}

func gcpSecret(key []byte) map[string][]byte { return map[string][]byte{KeyGCPCredentials: key} }

var gcpCreds = &Credentials{Source: SourceSecret, SecretRef: &SecretRef{Name: "gcp"}}

func TestGCPSecretServiceAccount(t *testing.T) {
	oauth, h := fakeGoogleOAuth(t, "Bearer")
	api, apiHits := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, r.Header.Get("Authorization"))
	})
	p := GCPClusterBlock{GCPBlock: GCPBlock{Project: "proj-12345"}, EndpointOverride: api.URL}
	c, err := openEgress().GCP(t.Context(), ScopeCluster, p, gcpCreds, gcpSecret(serviceAccountJSON(t, oauth)))
	if err != nil {
		t.Fatal(err)
	}
	if c.Endpoint != api.URL {
		t.Errorf("Endpoint = %q", c.Endpoint)
	}

	tok, exp, err := GCPTokenSource(c.Credentials).Token(t.Context())
	if err != nil || tok != "ya29.fake-access-token" || time.Until(exp) < 50*time.Minute {
		t.Errorf("Token = %q, %v, %v", tok, exp, err)
	}
	if h.count() != 1 || h.field("grant_type") != "urn:ietf:params:oauth:grant-type:jwt-bearer" || h.field("assertion") == "" {
		t.Errorf("OAuth saw %d requests, grant_type %q", h.count(), h.field("grant_type"))
	}

	req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, api.URL, nil)
	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "Bearer ya29.fake-access-token" || apiHits.count() != 1 {
		t.Errorf("the API saw Authorization %q in %d requests", body, apiHits.count())
	}
	if req.Header.Get("Authorization") != "" {
		t.Error("the caller's request was modified")
	}
}

func TestGCPTokenWithoutATypeIsBearer(t *testing.T) {
	oauth, _ := fakeGoogleOAuth(t, "")
	api, _ := newServer(t, func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, r.Header.Get("Authorization")) })
	c, err := openEgress().GCP(t.Context(), ScopeNamespaced, GCPClusterBlock{}, gcpCreds, gcpSecret(serviceAccountJSON(t, oauth)), "scope-a", "scope-b")
	if err != nil {
		t.Fatal(err)
	}
	resp, err := c.HTTPClient.Get(api.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if body, _ := io.ReadAll(resp.Body); string(body) != "Bearer ya29.fake-access-token" {
		t.Errorf("Authorization = %q", body)
	}
}

func TestGCPWorkloadIdentityUsesApplicationDefaultCredentials(t *testing.T) {
	oauth, _ := fakeGoogleOAuth(t, "Bearer")
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", writeFile(t, "adc.json", string(serviceAccountJSON(t, oauth))))
	c, err := openEgress().GCP(t.Context(), ScopeCluster, GCPClusterBlock{}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if tok, _, err := GCPTokenSource(c.Credentials).Token(t.Context()); err != nil || tok != "ya29.fake-access-token" {
		t.Errorf("Token = %q, %v", tok, err)
	}
}

func TestGCPNoApplicationDefaultCredentials(t *testing.T) {
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", t.TempDir()+"/missing.json")
	_, err := openEgress().GCP(t.Context(), ScopeCluster, GCPClusterBlock{}, nil, nil)
	if !errors.Is(err, ErrCredentialsUnavailable) {
		t.Errorf("GCP = %v", err)
	}
	noSecret(t, err, "missing.json")
}

// The Secret may hold a service_account key and nothing else: an
// external_account (or any other) file can make the client fetch a URL or run
// a program that a tenant chose.
func TestGCPSecretRejections(t *testing.T) {
	sa := string(serviceAccountJSON(t, "https://oauth2.example.test/token"))
	tests := []struct {
		name   string
		e      Egress
		secret map[string][]byte
		leak   string
	}{
		{"no key", openEgress(), nil, ""},
		{"not JSON", openEgress(), gcpSecret([]byte("LEAK-CHECK")), "LEAK-CHECK"},
		{"external_account", openEgress(), gcpSecret([]byte(`{"type":"external_account","audience":"LEAK-CHECK",` +
			`"credential_source":{"executable":{"command":"/bin/sh -c LEAK-CHECK"}}}`)), "LEAK-CHECK"},
		{"authorized_user", openEgress(), gcpSecret([]byte(`{"type":"authorized_user","refresh_token":"LEAK-CHECK"}`)), "LEAK-CHECK"},
		{"impersonated", openEgress(), gcpSecret([]byte(`{"type":"impersonated_service_account","x":"LEAK-CHECK"}`)), "LEAK-CHECK"},
		{"no type", openEgress(), gcpSecret([]byte(`{"private_key":"LEAK-CHECK"}`)), "LEAK-CHECK"},
		{"incomplete service_account", openEgress(), gcpSecret([]byte(`{"type":"service_account","token_uri":"https://x.test/t"}`)), ""},
		{"http token_uri outside allow-private", Egress{Mode: egress.ModeStrict}, gcpSecret([]byte(strings.Replace(sa, "https://oauth2.example.test/token", "http://oauth2.example.test/LEAK-CHECK", 1))), "LEAK-CHECK"},
		{"token_uri scheme", openEgress(), gcpSecret([]byte(strings.Replace(sa, "https://oauth2.example.test/token", "ftp://oauth2.example.test/LEAK-CHECK", 1))), "LEAK-CHECK"},
		{"token_uri user information", openEgress(), gcpSecret([]byte(strings.Replace(sa, "https://oauth2.example.test/token", "https://u:LEAK-CHECK@oauth2.example.test/t", 1))), "LEAK-CHECK"},
		{"token_uri without host", openEgress(), gcpSecret([]byte(strings.Replace(sa, "https://oauth2.example.test/token", "https:///LEAK-CHECK", 1))), "LEAK-CHECK"},
		{"token_uri unparsable", openEgress(), gcpSecret([]byte(strings.Replace(sa, "https://oauth2.example.test/token", "https://[::1/LEAK-CHECK", 1))), "LEAK-CHECK"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := tt.e.GCP(t.Context(), ScopeCluster, GCPClusterBlock{}, gcpCreds, tt.secret)
			if !errors.Is(err, ErrCredentialsUnavailable) {
				t.Fatalf("GCP = %v", err)
			}
			noSecret(t, err, tt.leak)
		})
	}
}

// An https token_uri is accepted without allow-private mode (a public Google
// endpoint); nothing is fetched until the token is asked for.
func TestGCPHTTPSTokenURIInStrictMode(t *testing.T) {
	e := NewEgress(egress.ModeStrict, nil)
	c, err := e.GCP(t.Context(), ScopeCluster, GCPClusterBlock{}, gcpCreds, gcpSecret(serviceAccountJSON(t, "https://oauth2.example.test/token")))
	if err != nil || c.Endpoint != "" {
		t.Errorf("GCP = %+v, %v", c, err)
	}
}

// The token exchange dials through the Policy: a token_uri on loopback is
// refused and never sees a request, and the failure carries no key material.
func TestGCPTokenEndpointDialsThroughThePolicy(t *testing.T) {
	oauth, h := fakeGoogleOAuth(t, "Bearer")
	api, apiHits := newServer(t, func(http.ResponseWriter, *http.Request) {})
	c, err := NewEgress(egress.ModeAllowPrivate, nil).GCP(t.Context(), ScopeCluster, GCPClusterBlock{}, gcpCreds, gcpSecret(serviceAccountJSON(t, oauth)))
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = GCPTokenSource(c.Credentials).Token(t.Context())
	if !errors.Is(err, ErrCredentialsUnavailable) {
		t.Fatalf("Token = %v", err)
	}
	noSecret(t, err, "PRIVATE KEY", oauth)

	req, _ := http.NewRequestWithContext(t.Context(), http.MethodPost, api.URL, strings.NewReader("body"))
	if err := doErr(c.HTTPClient, req); !errors.Is(err, ErrCredentialsUnavailable) {
		t.Errorf("Do = %v", err)
	}
	req, _ = http.NewRequestWithContext(t.Context(), http.MethodGet, api.URL, nil)
	if err := doErr(c.HTTPClient, req); !errors.Is(err, ErrCredentialsUnavailable) {
		t.Errorf("Do without a body = %v", err)
	}
	if h.count() != 0 || apiHits.count() != 0 {
		t.Errorf("OAuth saw %d requests and the API %d", h.count(), apiHits.count())
	}
}

// The API client dials through the Policy as well.
func TestGCPAPIClientDialsThroughThePolicy(t *testing.T) {
	api, apiHits := newServer(t, func(http.ResponseWriter, *http.Request) {})
	// A service account whose token exchange succeeds against an open Policy,
	// then an API call through the strict one.
	oauth, _ := fakeGoogleOAuth(t, "Bearer")
	open, err := openEgress().GCP(t.Context(), ScopeCluster, GCPClusterBlock{}, gcpCreds, gcpSecret(serviceAccountJSON(t, oauth)))
	if err != nil {
		t.Fatal(err)
	}
	tr, err := NewEgress(egress.ModeAllowPrivate, nil).transport()
	if err != nil {
		t.Fatal(err)
	}
	c := &http.Client{Transport: &bearerTransport{base: tr, creds: open.Credentials}}
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, api.URL, nil)
	if err := doErr(c, req); !errors.Is(err, egress.ErrBlocked) {
		t.Errorf("Do = %v, want ErrBlocked", err)
	}
	if apiHits.count() != 0 {
		t.Errorf("the API was reached %d times", apiHits.count())
	}
}

func TestGCPBadPrivateKeyIsACategoryError(t *testing.T) {
	oauth, _ := fakeGoogleOAuth(t, "Bearer")
	key := strings.Replace(string(serviceAccountJSON(t, oauth)), "BEGIN PRIVATE KEY", "BEGIN LEAK-CHECK", 1)
	c, err := openEgress().GCP(t.Context(), ScopeCluster, GCPClusterBlock{}, gcpCreds, gcpSecret([]byte(key)))
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = GCPTokenSource(c.Credentials).Token(t.Context())
	if !errors.Is(err, ErrCredentialsUnavailable) {
		t.Fatalf("Token = %v", err)
	}
	noSecret(t, err, "LEAK-CHECK")
}
