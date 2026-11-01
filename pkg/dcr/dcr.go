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
	"k8s.io/apimachinery/pkg/types"
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

// Write creates or replaces the claim's state. Call it again after every PUT:
// RFC 7592 lets the server return a new token.
func (s Store) Write(ctx context.Context, claim client.Object, st State) error {
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{
		Name:      s.key(claim).Name,
		Namespace: s.Namespace,
		Labels: map[string]string{
			binding.LabelManagedBy: s.ManagedBy,
			LabelClaimNamespace:    claim.GetNamespace(),
			LabelClaimName:         claim.GetName(),
			LabelClaimUID:          string(claim.GetUID()),
		},
	}, Type: corev1.SecretTypeOpaque, Data: map[string][]byte{
		KeyClientID:              []byte(st.ClientID),
		KeyRegistrationClientURI: []byte(st.RegistrationClientURI),
		KeyRegistrationToken:     []byte(st.RegistrationAccessToken),
	}}
	err := s.Client.Create(ctx, secret)
	if apierrors.IsAlreadyExists(err) {
		err = s.Client.Update(ctx, secret)
	}
	return err
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
// the client stays manageable through RFC 7592. It returns ErrNotFound when
// there is no state.
func (s Store) Orphan(ctx context.Context, claim client.Object) error {
	var secret corev1.Secret
	if err := s.Client.Get(ctx, s.key(claim), &secret); err != nil {
		if apierrors.IsNotFound(err) {
			return ErrNotFound
		}
		return err
	}
	if secret.Labels == nil {
		secret.Labels = map[string]string{}
	}
	secret.Labels[LabelOrphaned] = "true"
	return s.Client.Update(ctx, &secret)
}
