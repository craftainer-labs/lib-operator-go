package cloud

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"cloud.google.com/go/auth"
	"cloud.google.com/go/auth/credentials"

	"github.com/craftainer-labs/lib-operator-go/pkg/egress"
)

// GCPScopeCloudPlatform is the OAuth scope GCP and Memorystore calls need.
const GCPScopeCloudPlatform = "https://www.googleapis.com/auth/cloud-platform"

// serviceAccountType is the only "type" a Secret's credentials.json may have.
// external_account and impersonation files can make the client fetch a URL or
// run an executable that a tenant chose (ADR 0002, 16.2, decision 10).
const serviceAccountType = "service_account"

// GCPClient is what Egress.GCP resolves.
type GCPClient struct {
	// Credentials are the resolved credentials; GCPTokenSource uses them.
	Credentials *auth.Credentials
	// HTTPClient adds the access token to every request and dials through the
	// Policy. Hand it to a Google API client (for example
	// option.WithHTTPClient).
	HTTPClient *http.Client
	// Endpoint is the endpointOverride, "" for none; hand it to the API client
	// (for example option.WithEndpoint).
	Endpoint string
}

// GCP returns the credentials and an authorized HTTP client of a provider.gcp
// block. WorkloadIdentity is Application Default Credentials (the GKE metadata
// server, or the external_account file the platform mounts and names in
// GOOGLE_APPLICATION_CREDENTIALS); Secret is the service_account key in
// secret["credentials.json"]. Any other key type is ErrCredentialsUnavailable.
// The token exchange goes through the Policy, including the service account's
// token_uri, which must be https outside allow-private mode. With no scopes the
// token has GCPScopeCloudPlatform. Tokens are fetched on first use, so a
// rejected key shows on the first call or in GCPTokenSource, not here.
//
// The GCE and GKE metadata server is a link-local address the Policy blocks;
// the SDK reaches it with its own client, because the operator's environment,
// not a tenant, chooses it.
func (e Egress) GCP(_ context.Context, scope Scope, p GCPClusterBlock, c *Credentials, secret map[string][]byte, scopes ...string) (*GCPClient, error) {
	endpoint, err := e.endpoint(scope, p.EndpointOverride)
	if err != nil {
		return nil, err
	}
	src, err := source(scope, c)
	if err != nil {
		return nil, err
	}
	tr, err := e.transport()
	if err != nil {
		return nil, err
	}
	if len(scopes) == 0 {
		scopes = []string{GCPScopeCloudPlatform}
	}
	opts := &credentials.DetectOptions{
		Scopes: scopes,
		Client: &http.Client{Transport: tr},
		// Refresh on the caller's goroutine: no background refresh outlives the
		// context that asked for the token.
		DisableAsyncRefresh: true,
	}
	var creds *auth.Credentials
	if src == SourceSecret {
		creds, err = e.gcpFromSecret(secret, opts)
	} else {
		creds, err = credentials.DetectDefault(opts)
		if err != nil {
			err = fmt.Errorf("%w: no Application Default Credentials", ErrCredentialsUnavailable)
		}
	}
	if err != nil {
		return nil, err
	}
	return &GCPClient{
		Credentials: creds,
		HTTPClient:  &http.Client{Transport: &bearerTransport{base: tr, creds: creds}},
		Endpoint:    endpoint,
	}, nil
}

// gcpFromSecret accepts only a service_account key whose token_uri is https
// (http only in allow-private mode, for an emulator).
func (e Egress) gcpFromSecret(secret map[string][]byte, opts *credentials.DetectOptions) (*auth.Credentials, error) {
	b := secret[KeyGCPCredentials]
	var f struct {
		Type     string `json:"type"`
		TokenURI string `json:"token_uri"`
	}
	if len(b) == 0 {
		return nil, fmt.Errorf("%w: the Secret needs the key %s", ErrCredentialsUnavailable, KeyGCPCredentials)
	}
	if err := json.Unmarshal(b, &f); err != nil {
		return nil, fmt.Errorf("%w: %s is not JSON", ErrCredentialsUnavailable, KeyGCPCredentials)
	}
	if f.Type != serviceAccountType {
		return nil, fmt.Errorf("%w: %s must be a %s key", ErrCredentialsUnavailable, KeyGCPCredentials, serviceAccountType)
	}
	if f.TokenURI != "" {
		u, err := url.Parse(f.TokenURI)
		if err != nil || u.Hostname() == "" || u.User != nil ||
			(u.Scheme != "https" && (u.Scheme != "http" || e.Mode != egress.ModeAllowPrivate)) {
			return nil, fmt.Errorf("%w: the key's token_uri is not an allowed URL", ErrCredentialsUnavailable)
		}
	}
	creds, err := credentials.NewCredentialsFromJSON(credentials.ServiceAccount, b, opts)
	if err != nil {
		return nil, fmt.Errorf("%w: %s is not a usable service_account key", ErrCredentialsUnavailable, KeyGCPCredentials)
	}
	return creds, nil
}

// bearerTransport adds the Google access token to each request.
type bearerTransport struct {
	base  http.RoundTripper
	creds *auth.Credentials
}

// RoundTrip implements http.RoundTripper; it does not modify req.
func (t *bearerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	tok, err := t.creds.Token(req.Context())
	if err != nil {
		if req.Body != nil {
			_ = req.Body.Close()
		}
		return nil, fmt.Errorf("%w: the Google access token could not be fetched", ErrCredentialsUnavailable)
	}
	typ := tok.Type
	if typ == "" {
		typ = "Bearer"
	}
	r := req.Clone(req.Context())
	r.Header.Set("Authorization", typ+" "+tok.Value)
	return t.base.RoundTrip(r)
}

// GCPTokenSource returns the TokenSource of a Google access token, for
// Memorystore AUTH (lifetime 1 to 12 hours). The credentials cache and refresh
// the token themselves; wrap the source in a CachedSource to enforce a margin.
func GCPTokenSource(creds *auth.Credentials) TokenSource {
	return TokenFunc(func(ctx context.Context) (string, time.Time, error) {
		tok, err := creds.Token(ctx)
		if err != nil {
			return "", time.Time{}, fmt.Errorf("%w: the Google access token could not be fetched", ErrCredentialsUnavailable)
		}
		return tok.Value, tok.Expiry, nil
	})
}
