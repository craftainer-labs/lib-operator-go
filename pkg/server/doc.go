// Package server resolves the server a claim references and enforces the
// server's allowedNamespaces (ADR 0002, "Server tiers").
package server

import (
	"context"
	"errors"
	"net/url"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Ref names a Cluster<Kind>Server or a namespaced <Kind>Server. A namespaced
// server is always looked up in the claim's own namespace.
type Ref struct {
	Kind, Name string
}

// Connection is everything a backend client needs to reach one server.
type Connection struct {
	Endpoint string
	CABundle []byte
	// Admin is the data of the server's adminCredentialsSecretRef.
	Admin map[string][]byte
	// Proxy is the server's proxyURL; nil means the process environment.
	Proxy *url.URL
	// ServerName is tls.serverName; empty means the host of Endpoint.
	ServerName string
	// MinTLSVersion is tls.minVersion as a crypto/tls constant
	// (tls.VersionTLS12 or tls.VersionTLS13). Resolve never returns zero: an
	// unset field becomes tls.VersionTLS12, the ADR's floor.
	MinTLSVersion uint16
	// HealthCheckInterval is healthCheckInterval. Resolve applies the default
	// (1m) and the bounds (10s to 1h), so it is never zero.
	HealthCheckInterval time.Duration
}

var (
	// ErrNamespaceNotAllowed means the claim's namespace is outside the
	// server's allowedNamespaces; the claim gets Synced=False with this reason.
	// Reconcile does not return it as an error (which would back off): it
	// requeues at the drift-poll interval (ADR 0002, section 2).
	ErrNamespaceNotAllowed = errors.New("claim namespace is not allowed by the server")
	// ErrServerNotFound means the referenced server does not exist.
	ErrServerNotFound = errors.New("referenced server not found")
)

// ResolveFunc is the signature of Resolve, which NO1b implements as a function
// and then removes this type.
type ResolveFunc func(ctx context.Context, c client.Client, claimNS string, ref Ref) (Connection, error)
