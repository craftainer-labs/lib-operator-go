package lifecycle

import (
	"fmt"

	corev1 "k8s.io/api/core/v1"

	"github.com/craftainer-labs/lib-operator-go/pkg/binding"
	"github.com/craftainer-labs/lib-operator-go/pkg/conditions"
)

// ensureBinding keeps the binding Secret of a credential kind level-triggered:
// an up-to-date external object does not mean the Secret is still there.
//
//   - Current: nothing to do.
//   - Foreign (a Secret of that name the claim does not control): a name
//     collision, as for any write.
//   - Drifted (type, labels, or the type and provider keys were edited): the
//     metadata is repaired and the other keys are kept, without calling the
//     backend.
//   - Missing: the credential values cannot be recovered without the backend,
//     so the rotate path issues new ones. That needs a Rotator, the Update
//     management policy and no grace period running (a rotation while one is
//     running would lose the generation awaiting retirement). Otherwise the
//     claim gets Ready=False, Synced=False, reason BindingSecretMissing and a
//     Warning Event, and is repaired by a later reconcile once it can be.
func (r *run[S, O]) ensureBinding(cur O) (rotationOutcome, error) {
	cl := r.claim
	name := cl.GetBindingSecretName()
	reader := r.opts.APIReader
	if reader == nil {
		reader = r.c
	}
	state, secret, err := binding.Inspect(r.ctx, reader, cl, name, r.opts.BindingType, r.opts.Provider, r.writeOpts()...)
	if err != nil {
		return rotationOutcome{}, err
	}
	switch state {
	case binding.StateCurrent:
		return rotationOutcome{}, nil
	case binding.StateForeign:
		return rotationOutcome{}, fmt.Errorf("%w: %s/%s", ErrNameCollision, secret.Namespace, secret.Name)
	case binding.StateDrifted:
		return rotationOutcome{}, r.repairMetadata(name, secret)
	}

	rs := cl.GetRotationStatus()
	rot, isRotator := r.ext.(Rotator[S, O])
	inGrace := rs.Previous != "" || rs.RetireAfter != nil
	switch {
	case !isRotator:
		return r.missing("the backend of this kind cannot issue the credential again (the External is not a Rotator)"), nil
	case !r.pol.Update:
		return r.missing("recreating it needs a rotation, which needs the Update management policy"), nil
	case inGrace:
		return r.missing("a rotation grace period is running; the Secret is recreated by a rotation once it ends"), nil
	}
	out, err := r.begin(rot, cur, rs, "", cl.GetRotationPolicy())
	if err == nil {
		r.event(corev1.EventTypeNormal, conditions.EventBindingRepaired, "Reconcile",
			"the binding Secret %s was missing and was recreated through a rotation", name)
	}
	return out, err
}

func (r *run[S, O]) missing(why string) rotationOutcome {
	return rotationOutcome{bindingMissing: true, missingWhy: fmt.Sprintf("binding Secret %s is missing: %s",
		r.claim.GetBindingSecretName(), why)}
}

// repairMetadata rewrites a drifted binding Secret with its own credential keys.
func (r *run[S, O]) repairMetadata(name string, secret *corev1.Secret) error {
	if err := r.writeBinding(binding.Data(secret.Data)); err != nil {
		return err
	}
	r.event(corev1.EventTypeNormal, conditions.EventBindingRepaired, "Reconcile",
		"the binding Secret %s was edited; its type, labels and type and provider keys were restored", name)
	return nil
}
