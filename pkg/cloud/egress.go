package cloud

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/craftainer-labs/lib-operator-go/pkg/egress"
)

// Scope is the scope of the server kind a provider block belongs to.
type Scope string

// Values of Scope.
const (
	// ScopeCluster is a cluster-scoped server, created by the platform admin.
	ScopeCluster Scope = "Cluster"
	// ScopeNamespaced is a namespaced server, created by a tenant.
	ScopeNamespaced Scope = "Namespaced"
)

// Egress is what every client of the package dials through: the policy of the
// operator's EGRESS_MODE, the optional server proxy, and the mode itself, which
// gates endpointOverride.
type Egress struct {
	// Mode is the operator's EGRESS_MODE. endpointOverride needs
	// egress.ModeAllowPrivate.
	Mode egress.Mode
	// Policy is egress.NewPolicy(Mode); a test may use the zero Policy.
	Policy egress.Policy
	// Proxy is the proxy to tunnel through, or nil. It may not carry
	// credentials (egress.ErrProxyUserinfo).
	Proxy *url.URL
	// RootCAs are the roots the clients trust; nil means the system roots.
	RootCAs *x509.CertPool
}

// NewEgress returns the Egress of an operator.
func NewEgress(mode egress.Mode, proxy *url.URL) Egress {
	return Egress{Mode: mode, Policy: egress.NewPolicy(mode), Proxy: proxy}
}

// transport builds a transport that dials through Policy.HostDialer. It reads
// no proxy from the environment: a proxy is Egress.Proxy, whose address and
// every destination are checked by the Policy.
func (e Egress) transport() (*http.Transport, error) {
	dial, err := e.Policy.HostDialer(e.Proxy)
	if err != nil {
		return nil, err
	}
	return &http.Transport{
		DialContext:           dial,
		TLSClientConfig:       &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: e.RootCAs},
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: time.Minute,
		ExpectContinueTimeout: time.Second,
		IdleConnTimeout:       90 * time.Second,
		MaxIdleConns:          10,
		ForceAttemptHTTP2:     true,
	}, nil
}

// HTTPClient returns an HTTP client whose every connection goes through the
// Policy. Redirects are followed, and each hop is dialled through the Policy
// too.
func (e Egress) HTTPClient() (*http.Client, error) {
	tr, err := e.transport()
	if err != nil {
		return nil, err
	}
	return &http.Client{Transport: tr}, nil
}

// endpoint returns the endpointOverride to use, "" for none. It is honoured
// only on a cluster-scoped server and only in allow-private mode.
func (e Egress) endpoint(scope Scope, raw string) (string, error) {
	if raw == "" {
		return "", nil
	}
	if scope != ScopeCluster || e.Mode != egress.ModeAllowPrivate {
		return "", ErrEndpointOverrideForbidden
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil {
		return "", ErrInvalidEndpoint
	}
	return raw, nil
}

// source returns the credentials source: an absent credentials is
// WorkloadIdentity, which a namespaced server cannot use.
func source(scope Scope, c *Credentials) (CredentialsSource, error) {
	src := SourceWorkloadIdentity
	if c != nil && c.Source != "" {
		src = c.Source
	}
	switch {
	case src == SourceWorkloadIdentity && scope != ScopeCluster:
		return "", ErrWorkloadIdentityForbidden
	case src != SourceWorkloadIdentity && src != SourceSecret:
		return "", fmt.Errorf("%w: unknown credentials source", ErrCredentialsUnavailable)
	}
	return src, nil
}
