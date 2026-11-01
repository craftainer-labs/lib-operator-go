package binding

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
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

// WriteOption configures WriteSecret and Inspect.
type WriteOption func(*writeConfig)

type writeConfig struct {
	managedBy string
	reader    client.Reader
}

// WithManagedBy sets the app.kubernetes.io/managed-by label, the operator's
// name; the default is DefaultManagedBy.
func WithManagedBy(operator string) WriteOption {
	return func(c *writeConfig) { c.managedBy = operator }
}

// WithReader sets the reader that looks for a Secret that already exists.
// Pass the manager's uncached API reader: the operator's Secret cache is
// restricted to its managed-by label (CacheByObject), so through the cache a
// user's unlabelled Secret of the same name is invisible and would be
// reported as missing. The default is the client itself.
func WithReader(r client.Reader) WriteOption {
	return func(c *writeConfig) { c.reader = r }
}

func newWriteConfig(opts []WriteOption) writeConfig {
	cfg := writeConfig{managedBy: DefaultManagedBy}
	for _, o := range opts {
		o(&cfg)
	}
	return cfg
}

// WriteSecret creates or updates the Secret name in owner's namespace with a
// controller owner reference to owner, type servicebinding.io/<typ>, the
// managed-by and craftainer.dev/claim labels, and data plus the type and
// provider keys. It returns ErrNameCollision when the Secret exists and owner
// is not its controller, whether the existing Secret is seen by the reader or
// only discovered when the create fails with AlreadyExists. A Secret's type is
// immutable, so a different type on an existing Secret is an error. A Secret
// that already matches is not written.
func WriteSecret(ctx context.Context, c client.Client, owner client.Object,
	name, typ, provider string, data Data, opts ...WriteOption) error {
	cfg := newWriteConfig(opts)
	reader := cfg.reader
	if reader == nil {
		reader = c
	}
	want := SecretType(typ)
	secret := &corev1.Secret{}
	err := reader.Get(ctx, client.ObjectKey{Namespace: owner.GetNamespace(), Name: name}, secret)
	exists := err == nil
	if err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	if exists {
		if !metav1.IsControlledBy(secret, owner) {
			return fmt.Errorf("%w: %s/%s", ErrNameCollision, secret.Namespace, secret.Name)
		}
		if secret.Type != want {
			return fmt.Errorf("secret %s/%s has type %q, want %q (a Secret's type is immutable)",
				secret.Namespace, secret.Name, secret.Type, want)
		}
	} else {
		secret = &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: owner.GetNamespace()}}
	}
	before := secret.DeepCopy()
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
	if err := controllerutil.SetControllerReference(owner, secret, c.Scheme()); err != nil {
		return err
	}
	switch {
	case !exists:
		err := c.Create(ctx, secret)
		if apierrors.IsAlreadyExists(err) {
			return fmt.Errorf("%w: %s/%s", ErrNameCollision, secret.Namespace, secret.Name)
		}
		return err
	case equality.Semantic.DeepEqual(before, secret):
		return nil
	default:
		return c.Update(ctx, secret)
	}
}

// State is what Inspect found.
type State int

const (
	// StateMissing means there is no Secret of that name.
	StateMissing State = iota
	// StateCurrent means the claim controls the Secret and its type, labels and
	// type and provider keys are as WriteSecret leaves them.
	StateCurrent
	// StateDrifted means the claim controls the Secret but its type, labels or type
	// and provider keys were edited.
	StateDrifted
	// StateForeign means the Secret exists and the claim is not its controller.
	StateForeign
)

// Inspect reads the binding Secret through the reader (the uncached API reader
// of the manager, so a Secret outside the cache's label filter is seen) and
// classifies it. It also returns the Secret when there is one. The credential
// keys are not compared: only the backend knows them.
func Inspect(ctx context.Context, reader client.Reader, owner client.Object,
	name, typ, provider string, opts ...WriteOption) (State, *corev1.Secret, error) {
	cfg := newWriteConfig(opts)
	var secret corev1.Secret
	if err := reader.Get(ctx, client.ObjectKey{Namespace: owner.GetNamespace(), Name: name}, &secret); err != nil {
		if apierrors.IsNotFound(err) {
			return StateMissing, nil, nil
		}
		return 0, nil, err
	}
	switch {
	case !metav1.IsControlledBy(&secret, owner):
		return StateForeign, &secret, nil
	case secret.Type != SecretType(typ),
		secret.Labels[LabelManagedBy] != cfg.managedBy,
		secret.Labels[LabelClaim] != owner.GetName(),
		string(secret.Data[KeyType]) != typ,
		string(secret.Data[KeyProvider]) != provider:
		return StateDrifted, &secret, nil
	default:
		return StateCurrent, &secret, nil
	}
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
