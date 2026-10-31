// Package server resolves the server a claim references and enforces the
// server's allowedNamespaces (ADR 0002, "Server tiers").
package server

import (
	"context"
	"errors"
	"net/url"

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
}

var (
	// ErrNamespaceNotAllowed means the claim's namespace is outside the
	// server's allowedNamespaces; the claim gets Synced=False with this reason.
	ErrNamespaceNotAllowed = errors.New("claim namespace is not allowed by the server")
	// ErrServerNotFound means the referenced server does not exist.
	ErrServerNotFound = errors.New("referenced server not found")
)

// ResolveFunc is the signature of Resolve, which NO1b implements as a function
// and then removes this type.
type ResolveFunc func(ctx context.Context, c client.Client, claimNS string, ref Ref) (Connection, error)
