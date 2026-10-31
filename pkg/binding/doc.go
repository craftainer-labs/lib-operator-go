// Package binding is the credential output of every operator: a Secret in the
// claim's namespace of type servicebinding.io/<type> (ADR 0002, "Service
// Binding Secret"). Nothing here depends on a service binding runtime.
package binding

import (
	"context"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// TypePrefix starts the type of every binding Secret.
const TypePrefix = "servicebinding.io/"

// SecretType returns the Secret type for a binding type such as "postgresql".
// It is the one implemented function in this skeleton, so the coverage gate
// has a real statement to measure.
func SecretType(typ string) corev1.SecretType {
	return corev1.SecretType(TypePrefix + typ)
}

// Data is the Secret's data: the Service Binding spec's well-known keys plus
// the operator's own, as raw bytes.
type Data map[string][]byte

// WriteSecretFunc is the signature of WriteSecret, which NO1b implements as a
// function and then removes this type. It creates or updates the Secret `name`
// in owner's namespace with an owner reference to owner, type
// servicebinding.io/<typ> and the `type` and `provider` keys set, and fails
// when the Secret exists without that owner.
type WriteSecretFunc func(ctx context.Context, c client.Client, owner client.Object,
	name, typ, provider string, data Data) error
