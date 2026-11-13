package server

import (
	"cmp"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net/url"
	"os"
	"slices"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/version"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/craftainer-labs/lib-operator-go/pkg/apis/common"
)

// OperatorNamespaceEnv names the environment variable Resolve falls back to
// for the operator's own namespace when WithOperatorNamespace is not given.
// The operator's Deployment sets it from the downward API.
const OperatorNamespaceEnv = "POD_NAMESPACE"

const (
	defaultHealthCheckInterval = time.Minute
	minHealthCheckInterval     = 10 * time.Second
	maxHealthCheckInterval     = time.Hour
)

// Ref names a Cluster<Kind>Server or a namespaced <Kind>Server. A namespaced
// server is always looked up in the claim's own namespace.
type Ref = common.ServerRef

// Server is implemented by the API type of every server kind, cluster-scoped
// or namespaced. Resolve finds the type by Ref.Kind in the client's scheme.
type Server interface {
	client.Object
	// GetCommon returns the settings every server kind shares.
	GetCommon() *common.ServerCommon
	// GetEndpoint returns the kind's own endpoint (host and port, URL or
	// issuer); it becomes Connection.Endpoint unchanged.
	GetEndpoint() string
	// GetAllowedNamespaces returns allowedNamespaces of a cluster-scoped server
	// and nil for a namespaced one.
	GetAllowedNamespaces() *common.AllowedNamespaces
}

// Connection is everything a backend client needs to reach one server.
type Connection struct {
	Endpoint string
	CABundle []byte
	// Admin is the data of the server's adminCredentialsSecretRef (only the
	// named key when the reference has one).
	Admin map[string][]byte
	// Proxy is the server's proxyURL; nil means the process environment.
	Proxy *url.URL
	// ServerName is tls.serverName; empty means the host of Endpoint.
	ServerName string
	// MinTLSVersion is tls.minVersion as a crypto/tls constant
	// (tls.VersionTLS12 or tls.VersionTLS13). Resolve never returns zero: an
	// unset field becomes tls.VersionTLS12, the ADR's floor.
	MinTLSVersion uint16
	// HealthCheckInterval is healthCheckInterval. The default (1m, or the one
	// of WithHealthCheckInterval) and the bounds (10s to 1h) are applied, so on
	// success it is never zero. (ConnectionFor also sets it on an error;
	// Resolve returns an empty Connection then.)
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
	// ErrAdminSecretNamespaceForbidden means a server names an admin Secret
	// outside the namespace it may use (the operator namespace for a cluster
	// server, its own namespace for a namespaced one).
	ErrAdminSecretNamespaceForbidden = errors.New("admin credentials secret is in a forbidden namespace")
	// ErrOperatorNamespaceUnset means a cluster server needs the operator
	// namespace and neither WithOperatorNamespace nor POD_NAMESPACE gave it.
	ErrOperatorNamespaceUnset = errors.New("operator namespace is not set")
	// ErrUnknownServerKind means Ref.Kind is not a Server type in the scheme.
	ErrUnknownServerKind = errors.New("server kind is not registered")
	// ErrInvalidServer means the server object is unusable: a malformed proxy
	// URL, a TLS version or CA reference the schema would have rejected, or a
	// Secret or ConfigMap that does not hold the referenced key.
	ErrInvalidServer = errors.New("server is invalid")
)

// Option configures Resolve.
type Option func(*config)

type config struct {
	operatorNamespace string
	defaultInterval   time.Duration
}

// operatorNS returns the operator namespace of the option or the environment.
func (cfg config) operatorNS() (string, error) {
	ns := cmp.Or(cfg.operatorNamespace, os.Getenv(OperatorNamespaceEnv))
	if ns == "" {
		return "", ErrOperatorNamespaceUnset
	}
	return ns, nil
}

// interval is the default healthCheckInterval of a server that sets none.
func (cfg config) interval() time.Duration {
	if cfg.defaultInterval <= 0 {
		return defaultHealthCheckInterval
	}
	return cfg.defaultInterval
}

// WithOperatorNamespace sets the operator's own namespace, the only namespace a
// cluster server's admin Secret and CA bundle may come from.
func WithOperatorNamespace(ns string) Option {
	return func(c *config) { c.operatorNamespace = ns }
}

// WithHealthCheckInterval sets the Connection.HealthCheckInterval of a server
// that sets no healthCheckInterval; the default is 1m. A server's own value is
// still bounded to 10s..1h; the default given here is used as it is.
func WithHealthCheckInterval(def time.Duration) Option {
	return func(c *config) { c.defaultInterval = def }
}

// Resolve returns the connection settings of the server ref names, for a claim
// in claimNS. It returns ErrServerNotFound for a missing server and
// ErrNamespaceNotAllowed when a cluster server's allowedNamespaces does not
// admit claimNS; a cluster server without a usable allowedNamespaces admits
// nothing. The admin Secret and the CA bundle are read from the operator
// namespace (cluster server) or the claim's namespace (namespaced server).
func Resolve(ctx context.Context, c client.Client, claimNS string, ref Ref, opts ...Option) (Connection, error) {
	var cfg config
	for _, o := range opts {
		o(&cfg)
	}
	srv, err := newServer(c.Scheme(), ref.Kind)
	if err != nil {
		return Connection{}, err
	}
	namespaced, err := c.IsObjectNamespaced(srv)
	if err != nil {
		return Connection{}, fmt.Errorf("server kind %q: %w", ref.Kind, err)
	}
	key := types.NamespacedName{Name: ref.Name}
	var secretNS string
	if namespaced {
		key.Namespace, secretNS = claimNS, claimNS
	}
	if err := c.Get(ctx, key, srv); err != nil {
		if apierrors.IsNotFound(err) {
			return Connection{}, fmt.Errorf("%w: %s %q", ErrServerNotFound, ref.Kind, ref.Name)
		}
		return Connection{}, fmt.Errorf("get %s %q: %w", ref.Kind, ref.Name, err)
	}
	if !namespaced {
		if err := checkNamespace(ctx, c, claimNS, srv.GetAllowedNamespaces()); err != nil {
			return Connection{}, err
		}
		if secretNS, err = cfg.operatorNS(); err != nil {
			return Connection{}, err
		}
	}
	return connection(ctx, c, srv, secretNS, cfg)
}

// ConnectionFor returns the connection settings of srv itself, an already read
// server, for work that has no claim: the server's own health probe (package
// probe). There is no claim namespace, so allowedNamespaces is not consulted.
// The admin Secret and the CA bundle come from the operator namespace (cluster
// server) or the server's namespace (namespaced one), under the same checks as
// Resolve (they share one implementation). A namespaced server without a
// namespace is ErrInvalidServer. On an error the Connection is empty except for
// HealthCheckInterval, so a caller can still schedule its next attempt.
func ConnectionFor(ctx context.Context, c client.Client, srv Server, opts ...Option) (Connection, error) {
	var cfg config
	for _, o := range opts {
		o(&cfg)
	}
	conn, err := connectionFor(ctx, c, srv, cfg)
	// Set even on an error, so a caller can still schedule its next attempt.
	conn.HealthCheckInterval = healthCheckInterval(srv.GetCommon().HealthCheckInterval, cfg.interval())
	return conn, err
}

func connectionFor(ctx context.Context, c client.Client, srv Server, cfg config) (Connection, error) {
	namespaced, err := c.IsObjectNamespaced(srv)
	if err != nil {
		return Connection{}, fmt.Errorf("server %q: %w", srv.GetName(), err)
	}
	secretNS := srv.GetNamespace()
	if namespaced && secretNS == "" {
		return Connection{}, fmt.Errorf("%w: namespaced server %q has no namespace", ErrInvalidServer, srv.GetName())
	}
	if !namespaced {
		if secretNS, err = cfg.operatorNS(); err != nil {
			return Connection{}, err
		}
	}
	return connection(ctx, c, srv, secretNS, cfg)
}

// serverTypes caches which registered type serves a kind, so Resolve does not
// walk every type of the scheme on each reconcile. A scheme is complete before
// the manager starts, so a successful lookup does not change; a failed one is
// not cached.
var serverTypes sync.Map

type serverTypeKey struct {
	scheme *runtime.Scheme
	kind   string
}

// newServer returns a new object of the registered Server type of that kind.
// When a kind is registered in several versions or groups, the greatest group
// wins, and within a group the latest version by Kubernetes version priority
// (v1 over v1beta2 over v1alpha1, and v1beta10 over v1beta2), never by string
// order; an operator registers one version.
func newServer(s *runtime.Scheme, kind string) (Server, error) {
	key := serverTypeKey{s, kind}
	if gvk, ok := serverTypes.Load(key); ok {
		obj, _ := s.New(gvk.(schema.GroupVersionKind))
		return obj.(Server), nil
	}
	var gvks []schema.GroupVersionKind
	for gvk := range s.AllKnownTypes() {
		if gvk.Kind != kind {
			continue
		}
		if obj, err := s.New(gvk); err == nil {
			if _, ok := obj.(Server); ok {
				gvks = append(gvks, gvk)
			}
		}
	}
	if len(gvks) == 0 {
		return nil, fmt.Errorf("%w: %q", ErrUnknownServerKind, kind)
	}
	latest := slices.MaxFunc(gvks, func(a, b schema.GroupVersionKind) int {
		return cmp.Or(cmp.Compare(a.Group, b.Group), version.CompareKubeAwareVersionStrings(a.Version, b.Version))
	})
	serverTypes.Store(key, latest)
	obj, _ := s.New(latest)
	return obj.(Server), nil
}

func checkNamespace(ctx context.Context, c client.Client, claimNS string, allowed *common.AllowedNamespaces) error {
	if allowed == nil || allowed.From == "" {
		return ErrNamespaceNotAllowed
	}
	var ns corev1.Namespace
	if allowed.From == common.NamespacesSelector {
		if err := c.Get(ctx, types.NamespacedName{Name: claimNS}, &ns); err != nil {
			return fmt.Errorf("get namespace %q: %w", claimNS, err)
		}
	}
	if !allowed.Allows(&ns) {
		return ErrNamespaceNotAllowed
	}
	return nil
}

func connection(ctx context.Context, c client.Client, srv Server, secretNS string, cfg config) (Connection, error) {
	cm := srv.GetCommon()
	ref := cm.AdminCredentialsSecretRef
	if ref.Namespace != "" && ref.Namespace != secretNS {
		return Connection{}, fmt.Errorf("%w: %q, want %q", ErrAdminSecretNamespaceForbidden, ref.Namespace, secretNS)
	}
	admin, err := secretData(ctx, c, secretNS, ref.Name)
	if err != nil {
		return Connection{}, err
	}
	if ref.Key != "" {
		v, ok := admin[ref.Key]
		if !ok {
			return Connection{}, fmt.Errorf("%w: admin secret %q has no key %q", ErrInvalidServer, ref.Name, ref.Key)
		}
		admin = map[string][]byte{ref.Key: v}
	}
	out := Connection{
		Endpoint:            srv.GetEndpoint(),
		Admin:               admin,
		MinTLSVersion:       tls.VersionTLS12,
		HealthCheckInterval: healthCheckInterval(cm.HealthCheckInterval, cfg.interval()),
	}
	if cm.TLS != nil {
		out.ServerName = cm.TLS.ServerName
		switch cm.TLS.MinVersion {
		case "", common.TLS12:
		case common.TLS13:
			out.MinTLSVersion = tls.VersionTLS13
		default:
			return Connection{}, fmt.Errorf("%w: tls.minVersion %q", ErrInvalidServer, cm.TLS.MinVersion)
		}
		if out.CABundle, err = caBundle(ctx, c, secretNS, cm.TLS.CABundle); err != nil {
			return Connection{}, err
		}
	}
	if out.Proxy, err = proxy(cm.ProxyURL); err != nil {
		return Connection{}, err
	}
	return out, nil
}

func secretData(ctx context.Context, c client.Client, ns, name string) (map[string][]byte, error) {
	var s corev1.Secret
	if err := c.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, &s); err != nil {
		return nil, fmt.Errorf("get secret %s/%s: %w", ns, name, err)
	}
	out := make(map[string][]byte, len(s.Data))
	for k, v := range s.Data {
		out[k] = append([]byte(nil), v...)
	}
	return out, nil
}

func caBundle(ctx context.Context, c client.Client, ns string, b *common.CABundle) ([]byte, error) {
	switch {
	case b == nil:
		return nil, nil
	case b.SecretRef != nil && b.ConfigMapRef == nil:
		data, err := secretData(ctx, c, ns, b.SecretRef.Name)
		if err != nil {
			return nil, err
		}
		key := b.SecretRef.KeyOrDefault()
		pem, ok := data[key]
		if !ok {
			return nil, fmt.Errorf("%w: secret %q has no key %q", ErrInvalidServer, b.SecretRef.Name, key)
		}
		return pem, nil
	case b.ConfigMapRef != nil && b.SecretRef == nil:
		var cm corev1.ConfigMap
		if err := c.Get(ctx, types.NamespacedName{Namespace: ns, Name: b.ConfigMapRef.Name}, &cm); err != nil {
			return nil, fmt.Errorf("get configmap %s/%s: %w", ns, b.ConfigMapRef.Name, err)
		}
		key := b.ConfigMapRef.KeyOrDefault()
		if v, ok := cm.Data[key]; ok {
			return []byte(v), nil
		}
		if v, ok := cm.BinaryData[key]; ok {
			return append([]byte(nil), v...), nil
		}
		return nil, fmt.Errorf("%w: configmap %q has no key %q", ErrInvalidServer, b.ConfigMapRef.Name, key)
	default:
		return nil, fmt.Errorf("%w: caBundle needs exactly one of secretRef and configMapRef", ErrInvalidServer)
	}
}

func proxy(raw string) (*url.URL, error) {
	if raw == "" {
		return nil, nil
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("%w: proxyURL is not a URL", ErrInvalidServer)
	}
	switch u.Scheme {
	case "http", "https", "socks5":
	default:
		return nil, fmt.Errorf("%w: proxyURL scheme %q", ErrInvalidServer, u.Scheme)
	}
	if u.Host == "" {
		return nil, fmt.Errorf("%w: proxyURL has no host", ErrInvalidServer)
	}
	return u, nil
}

// healthCheckInterval returns the server's healthCheckInterval limited to the
// bounds of the CRD (10s to 1h), or def when it is unset.
func healthCheckInterval(d *metav1.Duration, def time.Duration) time.Duration {
	if d == nil {
		return def
	}
	return min(max(d.Duration, minHealthCheckInterval), maxHealthCheckInterval)
}
