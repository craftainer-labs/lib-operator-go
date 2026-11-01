// Package dcr stores the state of dynamic client registration (RFC 7591 and
// 7592) in the operator namespace (ADR 0002, section 9). The registration
// access token (RAT) manages the client, so it lives in a Secret named
// dcr-<claim uid>, never in a status. A cross-namespace owner reference is not
// allowed, so the claim's finalizer removes the Secret (Store.Delete); an
// orphaned client keeps it, relabelled (Store.Orphan).
package dcr

import (
	"context"
	"errors"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/craftainer-labs/lib-operator-go/pkg/binding"
)

// Labels and keys of a state Secret.
const (
	LabelClaimNamespace = "craftainer.dev/claim-namespace"
	LabelClaimName      = "craftainer.dev/claim-name"
	LabelClaimUID       = "craftainer.dev/claim-uid"
	// LabelOrphaned is "true" on the Secret of a client the operator no longer
	// manages; it is then the cluster administrator's to delete.
	LabelOrphaned = "craftainer.dev/orphaned"

	KeyClientID              = "clientId"
	KeyRegistrationClientURI = "registrationClientUri"
	KeyRegistrationToken     = "registrationAccessToken"
)

// ErrNotFound means the claim has no state Secret.
var ErrNotFound = errors.New("no registration state for the claim")

// State is what registration returned for one client.
type State struct {
	ClientID              string
	RegistrationClientURI string
	// RegistrationAccessToken is a credential: String and GoString hide it.
	RegistrationAccessToken string
}

// String hides the registration access token.
func (s State) String() string {
	return fmt.Sprintf("dcr.State{ClientID:%q, RegistrationClientURI:%q, RegistrationAccessToken:<redacted>}",
		s.ClientID, s.RegistrationClientURI)
}

// GoString is String, for %#v.
func (s State) GoString() string { return s.String() }

// Store reads and writes state Secrets in one namespace.
type Store struct {
	Client client.Client
	// Namespace is the operator namespace.
	Namespace string
	// ManagedBy is the operator's name for the managed-by label.
	ManagedBy string
}

// SecretName returns the name of the claim's state Secret.
func SecretName(uid types.UID) string { return "dcr-" + string(uid) }

func (s Store) key(claim client.Object) types.NamespacedName {
	return types.NamespacedName{Namespace: s.Namespace, Name: SecretName(claim.GetUID())}
}

// ErrConcurrentUpdate means another writer stored a newer registration token
// while Write was running; Write does not overwrite it. Read the state again.
var ErrConcurrentUpdate = errors.New("another writer stored a newer registration token")

// Write creates the claim's state, or merges st into the existing Secret with
// optimistic concurrency: it keeps the Secret's other labels (including
// craftainer.dev/orphaned), retries on a conflict, and returns
// ErrConcurrentUpdate instead of overwriting a registration token that
// changed under it. Call it again after every PUT: RFC 7592 lets the server
// return a new token.
func (s Store) Write(ctx context.Context, claim client.Object, st State) error {
	first, base := true, ""
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var cur corev1.Secret
		err := s.Client.Get(ctx, s.key(claim), &cur)
		if err != nil && !apierrors.IsNotFound(err) {
			return err
		}
		token := string(cur.Data[KeyRegistrationToken])
		switch {
		case first:
			first, base = false, token
		case token != base && token != st.RegistrationAccessToken:
			return ErrConcurrentUpdate
		}
		cur.Name, cur.Namespace, cur.Type = s.key(claim).Name, s.Namespace, corev1.SecretTypeOpaque
		if cur.Labels == nil {
			cur.Labels = map[string]string{}
		}
		cur.Labels[binding.LabelManagedBy] = s.ManagedBy
		cur.Labels[LabelClaimNamespace] = claim.GetNamespace()
		cur.Labels[LabelClaimName] = claim.GetName()
		cur.Labels[LabelClaimUID] = string(claim.GetUID())
		cur.Data = map[string][]byte{
			KeyClientID:              []byte(st.ClientID),
			KeyRegistrationClientURI: []byte(st.RegistrationClientURI),
			KeyRegistrationToken:     []byte(st.RegistrationAccessToken),
		}
		if apierrors.IsNotFound(err) {
			err = s.Client.Create(ctx, &cur)
			if apierrors.IsAlreadyExists(err) { // lost a create race: read again
				return apierrors.NewConflict(schema.GroupResource{Resource: "secrets"}, cur.Name, err)
			}
			return err
		}
		return s.Client.Update(ctx, &cur)
	})
}

// Read returns the claim's state, or ErrNotFound.
func (s Store) Read(ctx context.Context, claim client.Object) (State, error) {
	var secret corev1.Secret
	if err := s.Client.Get(ctx, s.key(claim), &secret); err != nil {
		if apierrors.IsNotFound(err) {
			return State{}, ErrNotFound
		}
		return State{}, err
	}
	return State{
		ClientID:                string(secret.Data[KeyClientID]),
		RegistrationClientURI:   string(secret.Data[KeyRegistrationClientURI]),
		RegistrationAccessToken: string(secret.Data[KeyRegistrationToken]),
	}, nil
}

// Delete removes the claim's state; a missing Secret is not an error.
func (s Store) Delete(ctx context.Context, claim client.Object) error {
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: s.key(claim).Name, Namespace: s.Namespace}}
	return client.IgnoreNotFound(s.Client.Delete(ctx, secret))
}

// Orphan keeps the claim's state and labels it craftainer.dev/orphaned=true, so
// the client stays manageable through RFC 7592. A claim with no state Secret
// (deleted before its registration completed) has nothing to keep: that is
// success, so its finalizer can clear.
func (s Store) Orphan(ctx context.Context, claim client.Object) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var secret corev1.Secret
		if err := s.Client.Get(ctx, s.key(claim), &secret); err != nil {
			return client.IgnoreNotFound(err)
		}
		if secret.Labels == nil {
			secret.Labels = map[string]string{}
		}
		secret.Labels[LabelOrphaned] = "true"
		return s.Client.Update(ctx, &secret)
	})
}
