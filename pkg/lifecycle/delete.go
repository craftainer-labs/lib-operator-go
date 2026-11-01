package lifecycle

import (
	"fmt"

	corev1 "k8s.io/api/core/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	"github.com/craftainer-labs/lib-operator-go/pkg/apis/common"
	"github.com/craftainer-labs/lib-operator-go/pkg/conditions"
)

// finalize runs the deletion outcome table of ADR 0002 section 4 for a claim
// with a deletion timestamp, then removes the finalizer. A paused claim is
// still deleted. The binding Secret goes with the claim through its owner
// reference.
func (r *run[S, O]) finalize() (ctrl.Result, error) {
	cl := r.claim
	if !controllerutil.ContainsFinalizer(cl, r.opts.FinalizerName) {
		r.opts.Audit.Forget(cl.GetUID()) // it goes without release(): nothing of ours holds it
		return ctrl.Result{}, nil
	}
	conditions.Ensure(cl.GetConditions(), cl.GetGeneration())
	policy, repinned := r.deletionPolicy()
	if repinned {
		r.event(corev1.EventTypeWarning, conditions.EventDeletionPolicyRepinned, "Delete",
			"status.effectiveDeletionPolicy was lost and spec.deletionPolicy is unset; deleting with %s", policy)
	}
	switch {
	case !r.pol.Delete:
		if r.pol.Create || r.pol.Update {
			r.event(corev1.EventTypeWarning, conditions.EventExternalObjectOrphaned, "Delete", "%s", r.orphanNote())
			if r.opts.OnOrphan != nil {
				if err := r.opts.OnOrphan(r.ctx, cl); err != nil {
					return r.failed("OnOrphan", err)
				}
			}
		} else {
			r.event(corev1.EventTypeNormal, conditions.EventRetained, "Delete",
				"the external object was never created by this claim and is left in place")
		}
	case r.opts.Class == ClassData && policy != common.DeletionDelete:
		r.event(corev1.EventTypeNormal, conditions.EventRetained, "Delete",
			"deletionPolicy is %s; the external object is left in place", policy)
	default:
		if blocked, res, err := r.deleteExternal(); blocked {
			return res, err
		}
	}
	return r.release()
}

// orphanNote says what stays behind. It names the external object and, for a
// credential kind, that its credential is still valid.
func (r *run[S, O]) orphanNote() string {
	note := "managementPolicies lacks Delete; the external object " + r.claim.GetExternalName() + " is left in place"
	if r.opts.Class == ClassCredential {
		note += " and its credential is still valid"
	}
	return note
}

// deletionPolicy returns the policy in force for a data kind: spec.deletionPolicy
// when set, else the pinned status value, else Retain with repinned true (the
// status was lost and the claim was deleted before the repin). A credential
// kind has none.
func (r *run[S, O]) deletionPolicy() (policy common.DeletionPolicy, repinned bool) {
	if r.opts.Class != ClassData {
		return "", false
	}
	if p := r.claim.GetDeletionPolicy(); p != "" {
		return p, false
	}
	if p := r.claim.GetEffectiveDeletionPolicy(); p != "" {
		return p, false
	}
	return common.DeletionRetain, true
}

// deleteExternal deletes the external object. blocked is true when the
// finalizer must stay (the server cannot be resolved or reached, or the
// backend failed) and res and err are then Reconcile's return values.
func (r *run[S, O]) deleteExternal() (blocked bool, res ctrl.Result, err error) {
	if r.opts.ResolveErr != nil {
		reason, final := r.resolveFailure()
		res, err = r.resolveResult("Delete", reason, final)
		return true, res, err
	}
	if r.ext == nil {
		return true, ctrl.Result{}, fmt.Errorf("%w: neither an External nor ResolveErr", ErrInvalidOptions)
	}
	obs, oerr := r.ext.Observe(r.ctx, r.spec)
	if oerr != nil {
		res, err = r.failed("Delete", oerr)
		return true, res, err
	}
	if obs.Exists {
		if derr := r.ext.Delete(r.ctx, obs.Current); derr != nil {
			res, err = r.failed("Delete", derr)
			return true, res, err
		}
		r.event(corev1.EventTypeNormal, conditions.EventDeleted, "Delete", "deleted the external object")
	}
	return false, ctrl.Result{}, nil
}

// release removes the finalizer.
func (r *run[S, O]) release() (ctrl.Result, error) {
	cl := r.claim
	orig := cl.DeepCopyObject().(client.Object)
	controllerutil.RemoveFinalizer(cl, r.opts.FinalizerName)
	err := r.gone(r.c.Patch(r.ctx, cl, client.MergeFromWithOptions(orig, client.MergeFromWithOptimisticLock{})))
	if err == nil {
		r.audit("Delete", "success", true)
		r.opts.Audit.Forget(cl.GetUID())
	}
	return ctrl.Result{}, client.IgnoreNotFound(err)
}
