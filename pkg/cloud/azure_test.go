package cloud

import (
	"crypto/x509"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	azcloud "github.com/Azure/azure-sdk-for-go/sdk/azcore/cloud"

	"github.com/craftainer-labs/lib-operator-go/pkg/egress"
)

const testTenant = "00000000-0000-4000-8000-000000000001"

// fakeEntra is an Entra authority: the tenant's OpenID configuration and the
// client credentials token endpoint. MSAL needs an https authority, so it is a
// TLS server, and root is a pool that trusts it.
func fakeEntra(t *testing.T) (srv *httptest.Server, h *hits, root *x509.CertPool) {
	t.Helper()
	h = &hits{}
	srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.record(r)
		w.Header().Set("Content-Type", "application/json")
		base := srv.URL + "/" + testTenant
		switch {
		case strings.HasSuffix(r.URL.Path, "/.well-known/openid-configuration"):
			_, _ = io.WriteString(w, `{"issuer":"`+base+`/v2.0","authorization_endpoint":"`+base+`/oauth2/v2.0/authorize",`+
				`"token_endpoint":"`+base+`/oauth2/v2.0/token"}`)
		case strings.HasSuffix(r.URL.Path, "/token"):
			_, _ = io.WriteString(w, `{"access_token":"entra-fake-token","token_type":"Bearer","expires_in":3600,"ext_expires_in":3600}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	root = x509.NewCertPool()
	root.AddCert(srv.Certificate())
	return srv, h, root
}

func isolateAzure(t *testing.T) {
	t.Helper()
	for _, k := range []string{"AZURE_CLIENT_ID", "AZURE_TENANT_ID", "AZURE_FEDERATED_TOKEN_FILE", "AZURE_AUTHORITY_HOST", "AZURE_CLIENT_SECRET"} {
		t.Setenv(k, "") // registers the restore
		_ = os.Unsetenv(k)
	}
}

func azureKeys() map[string][]byte {
	return map[string][]byte{KeyAzureClientID: []byte("11111111-1111-4111-8111-111111111111\n"), KeyAzureClientSecret: []byte("entra-client-secret")}
}

var azureSecretRef = &Credentials{Source: SourceSecret, SecretRef: &SecretRef{Name: "azure"}}

func TestAzureSecretClientCredentials(t *testing.T) {
	isolateAzure(t)
	srv, h, root := fakeEntra(t)
	e := openEgress()
	e.RootCAs = root
	p := AzureClusterBlock{AzureBlock: AzureBlock{TenantID: testTenant}, EndpointOverride: srv.URL}
	c, err := e.Azure(t.Context(), ScopeCluster, p, azureSecretRef, azureKeys())
	if err != nil {
		t.Fatal(err)
	}
	if c.Endpoint != srv.URL || c.ClientOptions.Cloud.ActiveDirectoryAuthorityHost != srv.URL || c.ClientOptions.Transport == nil {
		t.Errorf("client = %+v", c)
	}
	for _, scope := range []string{AzurePostgresScope, AzureRedisScope} {
		tok, exp, err := AzureTokenSource(c.Credential, scope).Token(t.Context())
		if err != nil || tok != "entra-fake-token" || time.Until(exp) < 50*time.Minute {
			t.Errorf("Token(%s) = %q, %v, %v", scope, tok, exp, err)
		}
		if !strings.HasPrefix(h.field("scope"), scope+" ") || h.field("client_secret") != "entra-client-secret" ||
			h.field("client_id") != "11111111-1111-4111-8111-111111111111" {
			t.Errorf("Entra saw scope %q, client_id %q", h.field("scope"), h.field("client_id"))
		}
	}
}

func TestAzureWorkloadIdentity(t *testing.T) {
	isolateAzure(t)
	srv, h, root := fakeEntra(t)
	t.Setenv("AZURE_CLIENT_ID", "22222222-2222-4222-8222-222222222222")
	t.Setenv("AZURE_FEDERATED_TOKEN_FILE", writeFile(t, "token", "projected-service-account-token"))

	// Without an override nothing is fetched at construction.
	plain, err := openEgress().Azure(t.Context(), ScopeCluster, AzureClusterBlock{AzureBlock: AzureBlock{TenantID: testTenant}}, nil, nil)
	if err != nil || plain.Endpoint != "" || plain.ClientOptions.Cloud.ActiveDirectoryAuthorityHost != azcloud.AzurePublic.ActiveDirectoryAuthorityHost {
		t.Fatalf("Azure = %+v, %v", plain, err)
	}

	e := openEgress()
	e.RootCAs = root
	p := AzureClusterBlock{AzureBlock: AzureBlock{TenantID: testTenant}, EndpointOverride: srv.URL}
	c, err := e.Azure(t.Context(), ScopeCluster, p, &Credentials{Source: SourceWorkloadIdentity}, nil)
	if err != nil {
		t.Fatal(err)
	}
	tok, _, err := AzureTokenSource(c.Credential, AzurePostgresScope).Token(t.Context())
	if err != nil || tok != "entra-fake-token" {
		t.Fatalf("Token = %q, %v", tok, err)
	}
	if h.field("client_assertion") != "projected-service-account-token" || h.field("client_id") != "22222222-2222-4222-8222-222222222222" {
		t.Errorf("Entra saw client_id %q, client_assertion %q", h.field("client_id"), h.field("client_assertion"))
	}
}

func TestAzureNoWorkloadIdentity(t *testing.T) {
	isolateAzure(t)
	_, err := openEgress().Azure(t.Context(), ScopeCluster, AzureClusterBlock{AzureBlock: AzureBlock{TenantID: testTenant}}, nil, nil)
	if !errors.Is(err, ErrCredentialsUnavailable) {
		t.Errorf("Azure = %v", err)
	}
}

func TestAzureSecretErrorsCarryNoSecret(t *testing.T) {
	isolateAzure(t)
	tests := map[string]struct {
		tenant string
		secret map[string][]byte
	}{
		"nil":                 {testTenant, nil},
		"no client secret":    {testTenant, map[string][]byte{KeyAzureClientID: []byte("id-LEAK-CHECK")}},
		"no client id":        {testTenant, map[string][]byte{KeyAzureClientSecret: []byte("secret-LEAK-CHECK")}},
		"blank":               {testTenant, map[string][]byte{KeyAzureClientID: []byte(" "), KeyAzureClientSecret: []byte("secret-LEAK-CHECK")}},
		"tenant is not an ID": {"not a tenant!", azureKeys()},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := openEgress().Azure(t.Context(), ScopeCluster, AzureClusterBlock{AzureBlock: AzureBlock{TenantID: tt.tenant}}, azureSecretRef, tt.secret)
			if !errors.Is(err, ErrCredentialsUnavailable) {
				t.Fatalf("Azure = %v", err)
			}
			noSecret(t, err, "id-LEAK-CHECK", "secret-LEAK-CHECK", "entra-client-secret")
		})
	}
}

// The Entra authority dials through the Policy: it is on loopback here, which
// the Policy refuses in every mode, so it never sees a request.
func TestAzureAuthorityDialsThroughThePolicy(t *testing.T) {
	isolateAzure(t)
	srv, h, root := fakeEntra(t)
	e := NewEgress(egress.ModeAllowPrivate, nil)
	e.RootCAs = root
	p := AzureClusterBlock{AzureBlock: AzureBlock{TenantID: testTenant}, EndpointOverride: srv.URL}
	c, err := e.Azure(t.Context(), ScopeCluster, p, azureSecretRef, azureKeys())
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = AzureTokenSource(c.Credential, AzureRedisScope).Token(t.Context())
	if !errors.Is(err, ErrCredentialsUnavailable) {
		t.Fatalf("Token = %v", err)
	}
	noSecret(t, err, "entra-client-secret", srv.URL)
	if h.count() != 0 {
		t.Errorf("Entra was reached %d times", h.count())
	}
}

func TestAzureAuthorityMustBeHTTPS(t *testing.T) {
	isolateAzure(t)
	p := AzureClusterBlock{AzureBlock: AzureBlock{TenantID: testTenant}, EndpointOverride: "http://10.1.2.3:8080"}
	if _, err := openEgress().Azure(t.Context(), ScopeCluster, p, azureSecretRef, azureKeys()); !errors.Is(err, ErrInvalidEndpoint) {
		t.Errorf("Azure = %v", err)
	}
}
