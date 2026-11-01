package binding

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

// TypePrefix starts the type of every binding Secret.
const TypePrefix = "servicebinding.io/"

// The binding types (ADR 0002, section 7).
const (
	TypePostgreSQL = "postgresql"
	TypeRedis      = "redis" // Valkey and Redis
	TypeS3         = "s3"
	TypeMQTT       = "mqtt"
	TypeOIDC       = "oidc"
)

// Names fixed by the ADR.
const (
	// KeyType and KeyProvider are the Service Binding spec's reserved keys that
	// every binding Secret carries.
	KeyType     = "type"
	KeyProvider = "provider"
	// DefaultProvider is the provider key's value.
	DefaultProvider = "craftainer.dev"
	// LabelManagedBy marks the Secrets an operator owns; its Secret cache is
	// restricted to this label (CacheByObject).
	LabelManagedBy = "app.kubernetes.io/managed-by"
	// LabelClaim carries the owning claim's name.
	LabelClaim = "craftainer.dev/claim"
	// DefaultManagedBy is the managed-by value when WithManagedBy is not given.
	DefaultManagedBy = "craftainer-operator"
)

// typeKeys are the data keys of each binding type, from the table in ADR 0002
// section 7, without type and provider. Core 1.1.0 of the Service Binding spec
// defines no per-type lists; these are craftainer conventions.
var typeKeys = map[string][]string{
	TypePostgreSQL: {"host", "port", "database", "username", "password", "sslmode", "sslrootcert", "uri"},
	TypeRedis:      {"host", "port", "username", "password", "uri", "keyPrefix", "sslrootcert"},
	TypeS3:         {"endpoint", "region", "bucket", "accessKeyId", "secretAccessKey", "prefix", "pathStyle", "sslrootcert"},
	TypeMQTT:       {"host", "port", "protocol", "username", "password", "clientId", "topicRoot", "sslrootcert"},
	TypeOIDC:       {"issuer", "clientId", "clientSecret", "tokenEndpoint", "authorizationEndpoint", "jwksUri", "userinfoEndpoint"},
}

// Keys returns the data keys a binding of the type may hold, besides type and
// provider, and whether the type is known.
func Keys(typ string) ([]string, bool) {
	keys, ok := typeKeys[typ]
	return slices.Clone(keys), ok
}

// SecretType returns the Secret type for a binding type such as "postgresql".
func SecretType(typ string) corev1.SecretType {
	return corev1.SecretType(TypePrefix + typ)
}

// Data is the Secret's data: the Service Binding spec's well-known keys plus
// the operator's own, as raw bytes. Printing a Data shows its keys only, never
// the values, so it is safe to log by accident.
type Data map[string][]byte

// Keys returns the sorted keys.
func (d Data) Keys() []string {
	return slices.Sorted(maps.Keys(d))
}

// String lists the keys and hides the values.
func (d Data) String() string {
	return "binding.Data{" + strings.Join(d.Keys(), ",") + "}"
}

// GoString is String, for %#v.
func (d Data) GoString() string { return d.String() }

// ErrNameCollision means a Secret of the wanted name exists and the claim is
// not its controller, so WriteSecret refuses to take it over.
var ErrNameCollision = errors.New("a Secret of that name exists and is not owned by this claim")

// WriteOption configures WriteSecret.
type WriteOption func(*writeConfig)

type writeConfig struct{ managedBy string }

// WithManagedBy sets the app.kubernetes.io/managed-by label, the operator's
// name; the default is DefaultManagedBy.
func WithManagedBy(operator string) WriteOption {
	return func(c *writeConfig) { c.managedBy = operator }
}

// WriteSecret creates or updates the Secret name in owner's namespace with a
// controller owner reference to owner, type servicebinding.io/<typ>, the
// managed-by and craftainer.dev/claim labels, and data plus the type and
// provider keys. It returns ErrNameCollision when the Secret exists and owner
// is not its controller. A Secret's type is immutable, so a different type on
// an existing Secret is an error.
func WriteSecret(ctx context.Context, c client.Client, owner client.Object,
	name, typ, provider string, data Data, opts ...WriteOption) error {
	cfg := writeConfig{managedBy: DefaultManagedBy}
	for _, o := range opts {
		o(&cfg)
	}
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: owner.GetNamespace()}}
	want := SecretType(typ)
	_, err := controllerutil.CreateOrUpdate(ctx, c, secret, func() error {
		if secret.ResourceVersion != "" {
			if !metav1.IsControlledBy(secret, owner) {
				return fmt.Errorf("%w: %s/%s", ErrNameCollision, secret.Namespace, secret.Name)
			}
			if secret.Type != want {
				return fmt.Errorf("secret %s/%s has type %q, want %q (a Secret's type is immutable)",
					secret.Namespace, secret.Name, secret.Type, want)
			}
		}
		secret.Type = want
		secret.Labels = mergeLabels(secret.Labels, map[string]string{
			LabelManagedBy: cfg.managedBy,
			LabelClaim:     owner.GetName(),
		})
		secret.Data = make(map[string][]byte, len(data)+2)
		for k, v := range data {
			secret.Data[k] = append([]byte(nil), v...)
		}
		secret.Data[KeyType] = []byte(typ)
		secret.Data[KeyProvider] = []byte(provider)
		return controllerutil.SetControllerReference(owner, secret, c.Scheme())
	})
	return err
}

func mergeLabels(have, want map[string]string) map[string]string {
	out := maps.Clone(have)
	if out == nil {
		out = map[string]string{}
	}
	maps.Copy(out, want)
	return out
}

// CacheByObject restricts an operator's Secret cache to the Secrets carrying
// app.kubernetes.io/managed-by=<managedBy>, so it never lists or watches tenant
// Secrets it does not own (EB-2). Put it in cache.Options.ByObject.
func CacheByObject(managedBy string) map[client.Object]cache.ByObject {
	return map[client.Object]cache.ByObject{
		&corev1.Secret{}: {Label: labels.SelectorFromSet(labels.Set{LabelManagedBy: managedBy})},
	}
}
