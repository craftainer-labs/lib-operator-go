package server

import (
	"context"
	"errors"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/craftainer-labs/lib-operator-go/pkg/apis/common"
)

var (
	// ErrCredentialsSecretNamespaceForbidden means a cloud server names a
	// credentials Secret outside the namespace it may use (the operator
	// namespace for a cluster server, its own namespace for a namespaced one).
	// The condition reason is conditions.CredentialsNamespaceForbidden.
	ErrCredentialsSecretNamespaceForbidden = errors.New("cloud credentials secret is in a forbidden namespace")
	// ErrCredentialsUnavailable means the credentials Secret a cloud server
	// names does not exist. The condition reason is
	// conditions.CredentialsUnavailable.
	ErrCredentialsUnavailable = errors.New("cloud credentials secret is unavailable")
)

// CloudServer is implemented by the API type of a server kind that can run in
// a cloud with no admin Secret (it embeds common.CloudServerCommon). It is the
// counterpart of Server for ResolveCloud and ConnectionForCloud;
type CloudServer interface {
	client.Object
	// GetCloudCommon returns the settings every cloud-capable server shares.
	GetCloudCommon() *common.CloudServerCommon
	// GetEndpoint returns the data-plane endpoint override of the server (host
	// and port, or URL); it becomes Connection.Endpoint unchanged and may be
	// empty, because a cloud server's endpoint is discovered.
	GetEndpoint() string
	// GetAllowedNamespaces returns allowedNamespaces of a cluster-scoped server
	// and nil for a namespaced one.
	GetAllowedNamespaces() *common.AllowedNamespaces
	// GetCredentialsSecret returns the name and the namespace of
	// spec.credentials.secretRef, and an empty name when the server uses
	// workload identity (credentials absent, or source WorkloadIdentity).
	GetCredentialsSecret() (name, namespace string)
}

// CloudConnection is a Connection plus the cloud credential.
type CloudConnection struct {
	// Connection has an empty Admin when the server names no
	// adminCredentialsSecretRef.
	Connection
	// Credentials is the data of the credentials Secret (spec.credentials.
	// secretRef), nil for workload identity. It is the secret argument of
	// cloud.Egress.AWS, GCP and Azure.
	Credentials map[string][]byte
}

// ResolveCloud is Resolve for a CloudServer: it returns ErrServerNotFound for
// a missing server and ErrNamespaceNotAllowed when a cluster server's
// allowedNamespaces does not admit claimNS (the same rule, fail closed). The
// admin Secret is read only when adminCredentialsSecretRef is set, else Admin
// is empty. The credentials Secret is read only when the server names one.
// Both and the CA bundle come from the operator namespace (cluster server) or
// the claim's namespace (namespaced server); a credentials Secret in any other
// namespace is ErrCredentialsSecretNamespaceForbidden. A kind that is not a
// CloudServer (a self-hosted kind, for Resolve) is ErrUnknownServerKind. On an
// error the connection is empty.
func ResolveCloud(ctx context.Context, c client.Client, claimNS string, ref Ref, opts ...Option) (CloudConnection, error) {
	cfg := newConfig(opts)
	srv, secretNS, err := find[CloudServer](ctx, c, claimNS, ref, cfg)
	if err != nil {
		return CloudConnection{}, err
	}
	return cloudConnection(ctx, c, srv, secretNS, cfg)
}

// ConnectionForCloud is ConnectionFor for a CloudServer: the connection of an
// already read server for work that has no claim (the probe), with
// allowedNamespaces not consulted. As on ConnectionFor, an error leaves the
// connection empty except for HealthCheckInterval. A server whose credentials
// Secret is missing is ErrCredentialsUnavailable, which wraps the API's
// not-found error as well.
func ConnectionForCloud(ctx context.Context, c client.Client, srv CloudServer, opts ...Option) (CloudConnection, error) {
	cfg := newConfig(opts)
	interval := healthCheckInterval(srv.GetCloudCommon().HealthCheckInterval, cfg.interval())
	secretNS, err := ownNamespace(c, srv, cfg)
	if err != nil {
		return CloudConnection{Connection: Connection{HealthCheckInterval: interval}}, err
	}
	conn, err := cloudConnection(ctx, c, srv, secretNS, cfg)
	conn.HealthCheckInterval = interval
	return conn, err
}

func cloudConnection(ctx context.Context, c client.Client, srv CloudServer, secretNS string, cfg config) (CloudConnection, error) {
	cm := srv.GetCloudCommon()
	// The credentials namespace is checked before any Secret is read, as the
	// admin Secret's is, so a forbidden one is reported as such.
	name, ns := srv.GetCredentialsSecret()
	if name != "" && ns != "" && ns != secretNS {
		return CloudConnection{}, fmt.Errorf("%w: %q, want %q", ErrCredentialsSecretNamespaceForbidden, ns, secretNS)
	}
	conn, err := connection(ctx, c, secretNS, cfg, connSpec{
		endpoint: srv.GetEndpoint(), tls: cm.TLS, proxyURL: cm.ProxyURL,
		interval: cm.HealthCheckInterval, admin: cm.AdminCredentialsSecretRef,
	})
	if err != nil {
		return CloudConnection{}, err
	}
	out := CloudConnection{Connection: conn}
	if name == "" {
		return out, nil
	}
	if out.Credentials, err = secretData(ctx, c, secretNS, name); err != nil {
		if apierrors.IsNotFound(err) {
			return CloudConnection{}, fmt.Errorf("%w: %w", ErrCredentialsUnavailable, err)
		}
		return CloudConnection{}, err
	}
	return out, nil
}
