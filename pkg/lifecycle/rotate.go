package lifecycle

import (
	"fmt"
	"strconv"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/craftainer-labs/lib-operator-go/pkg/apis/common"
	"github.com/craftainer-labs/lib-operator-go/pkg/binding"
	"github.com/craftainer-labs/lib-operator-go/pkg/conditions"
)

// rotationOutcome is what rotate did, for the conditions Reconcile sets.
type rotationOutcome struct {
	// unsupported: a rotation is wanted and the External is not a Rotator.
	unsupported bool
	// blocked: a rotation is due and managementPolicies lacks Update.
	blocked bool
	// rotated and finished: RotateBegin or RotateFinish completed in this call.
	rotated, finished bool
	// bindingMissing: the binding Secret is gone and cannot be recreated;
	// missingWhy says why, for the condition and the Event.
	bindingMissing bool
	missingWhy     string
	// grace: the previous generation is still valid; retireIn is how long.
	grace    bool
	retireIn time.Duration
}

func toState(rs common.RotationStatus) RotationState {
	return RotationState{Active: Generation(rs.Active), Previous: Generation(rs.Previous), Sequence: rs.Sequence}
}

// rotate runs the state machine of ADR 0002 section 8 for a claim that is up to
// date: due, begin, grace, finish. A grace period is in force while
// status.rotation.previous or retireAfter is set; no rotation begins during
// it.
func (r *run[S, O]) rotate(cur O) (rotationOutcome, error) {
	cl := r.claim
	rs := cl.GetRotationStatus()
	policy := cl.GetRotationPolicy()
	trigger := cl.GetAnnotations()[AnnotationRotate]
	triggered := trigger != "" && trigger != rs.LastTrigger
	rot, ok := r.ext.(Rotator[S, O])
	if !ok {
		return rotationOutcome{unsupported: policy != nil || triggered}, nil
	}
	inGrace := rs.Previous != "" || rs.RetireAfter != nil
	if inGrace && r.pol.ObserveOnly {
		// An Observe-only claim never changes the backend, so it does not retire
		// a generation either; a rotation left over from before the policies were
		// narrowed waits until they allow it again.
		return rotationOutcome{}, nil
	}
	if inGrace {
		if triggered {
			// A trigger during the grace period is consumed and rejected, not
			// queued: the user sets a new value after the grace period ends.
			rs.LastTrigger = trigger
			cl.SetRotationStatus(rs)
			r.event(corev1.EventTypeWarning, conditions.EventRotationDeferred, "Rotate",
				"%s=%q arrived while the previous credential generation is still valid; it is ignored, set a new value after the grace period ends",
				AnnotationRotate, trigger)
		}
		return r.finishIfDue(rot, cur, rs)
	}
	if !r.due(policy, rs, triggered) {
		return rotationOutcome{}, nil
	}
	if !r.pol.Update {
		// A trigger without Update is consumed and rejected once, like one during a
		// grace period: it neither repeats every poll nor rotates when Update is
		// added later. Only an elapsed interval keeps the claim blocked.
		if triggered {
			rs.LastTrigger = trigger
			cl.SetRotationStatus(rs)
			r.event(corev1.EventTypeWarning, conditions.EventRotationDeferred, "Rotate",
				"%s=%q is ignored: managementPolicies lacks Update, so the claim may not rotate; set a new value once it has Update",
				AnnotationRotate, trigger)
		}
		return rotationOutcome{blocked: r.due(policy, rs, false)}, nil
	}
	return r.begin(rot, cur, rs, trigger, policy)
}

// due reports whether a rotation is due: the trigger annotation changed, or the
// interval has elapsed since the last rotation (the claim's creation, if it
// never rotated).
func (r *run[S, O]) due(policy *common.RotationPolicy, rs common.RotationStatus, triggered bool) bool {
	interval := policy.IntervalOrZero()
	if triggered {
		return true
	}
	if interval <= 0 {
		return false
	}
	base := r.claim.GetCreationTimestamp().Time
	if rs.LastRotated != nil {
		base = rs.LastRotated.Time
	}
	return !r.now.Before(base.Add(interval))
}

// The annotations of the binding Secret that say which rotation it holds. They
// are written after the Secret's data, so their presence means the data is
// the one of that sequence; the status is written last, and when that write
// fails the next reconcile finds the annotations and adopts the rotation
// instead of issuing it a second time.
const (
	annotationRotationSequence   = "craftainer.dev/rotation-sequence"
	annotationRotationGeneration = "craftainer.dev/rotation-generation"
)

// begin issues the next generation, writes it to the binding Secret, marks the
// Secret with the rotation and then records it in the status. A crash before
// the mark repeats RotateBegin, which is idempotent; a failed status write
// after the mark is recovered by adoptIssued.
func (r *run[S, O]) begin(rot Rotator[S, O], cur O, rs common.RotationStatus, trigger string,
	policy *common.RotationPolicy) (rotationOutcome, error) {
	gen, adopted, err := r.adoptIssued(rs)
	if err != nil {
		return rotationOutcome{}, err
	}
	if !adopted {
		var data binding.Data
		data, gen, err = rot.RotateBegin(r.ctx, r.spec, cur, toState(rs))
		if err != nil {
			r.event(corev1.EventTypeWarning, conditions.EventRotationFailed, "Rotate", "RotateBegin failed: %s", err)
			return rotationOutcome{}, fmt.Errorf("RotateBegin: %w", err)
		}
		if err := r.writeBinding(data); err != nil {
			r.event(corev1.EventTypeWarning, conditions.EventRotationFailed, "Rotate",
				"writing the binding Secret failed: %s", err)
			return rotationOutcome{}, fmt.Errorf("write the rotated binding: %w", err)
		}
		if err := r.markIssued(rs.Sequence+1, gen); err != nil {
			r.event(corev1.EventTypeWarning, conditions.EventRotationFailed, "Rotate",
				"marking the binding Secret failed: %s", err)
			return rotationOutcome{}, fmt.Errorf("mark the rotated binding: %w", err)
		}
	}
	hardCut := false
	if h, ok := rot.(HardCutRotator); ok {
		hardCut = h.HardCut()
	}
	previous := rs.Active
	rs.Active, rs.Previous, rs.RetireAfter = string(gen), "", nil
	rs.Sequence++
	last := metav1.NewTime(r.now)
	rs.LastRotated = &last
	if trigger != "" {
		rs.LastTrigger = trigger
	}
	out := rotationOutcome{rotated: true}
	reason := conditions.EventRotatedWithoutOverlap
	grace := policy.GracePeriodOrDefault()
	if !hardCut {
		retire := metav1.NewTime(r.now.Add(grace))
		rs.Previous, rs.RetireAfter = previous, &retire
		out.grace, out.retireIn = grace > 0, grace
		reason = conditions.EventRotationStarted
	}
	r.claim.SetRotationStatus(rs)
	r.event(corev1.EventTypeNormal, reason, "Rotate", "issued credential generation %s", rs.Active)
	if !hardCut && grace <= 0 {
		// An explicit gracePeriod of 0 is a hard cut: retire the previous
		// generation now, in this reconcile, never as a zero RequeueAfter. The
		// grace state recorded above is already due, so a failure here is retried.
		fin, err := r.finishIfDue(rot, cur, rs)
		if err != nil {
			return rotationOutcome{}, err
		}
		fin.rotated = true
		return fin, nil
	}
	return out, nil
}

// adoptIssued reports whether the binding Secret already holds the rotation
// that follows rs: the rotation was issued and written, and only the status
// write after it failed. It returns that rotation's generation.
func (r *run[S, O]) adoptIssued(rs common.RotationStatus) (Generation, bool, error) {
	var secret corev1.Secret
	key := client.ObjectKey{Namespace: r.claim.GetNamespace(), Name: r.claim.GetBindingSecretName()}
	if err := r.bindingReader().Get(r.ctx, key, &secret); err != nil {
		if apierrors.IsNotFound(err) {
			return "", false, nil
		}
		return "", false, fmt.Errorf("read the binding Secret: %w", err)
	}
	gen := secret.Annotations[annotationRotationGeneration]
	if gen == "" || !metav1.IsControlledBy(&secret, r.claim) ||
		secret.Annotations[annotationRotationSequence] != strconv.FormatInt(rs.Sequence+1, 10) {
		return "", false, nil
	}
	return Generation(gen), true, nil
}

// markIssued records on the binding Secret that it holds the rotation with the
// sequence and generation, without touching its data.
func (r *run[S, O]) markIssued(sequence int64, gen Generation) error {
	orig := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: r.claim.GetNamespace(), Name: r.claim.GetBindingSecretName()}}
	marked := orig.DeepCopy()
	marked.Annotations = map[string]string{
		annotationRotationSequence:   strconv.FormatInt(sequence, 10),
		annotationRotationGeneration: string(gen),
	}
	return r.c.Patch(r.ctx, marked, client.MergeFrom(orig))
}

// finishIfDue retires the previous generation once the grace period is over.
func (r *run[S, O]) finishIfDue(rot Rotator[S, O], cur O, rs common.RotationStatus) (rotationOutcome, error) {
	if rs.RetireAfter != nil && r.now.Before(rs.RetireAfter.Time) {
		return rotationOutcome{grace: true, retireIn: rs.RetireAfter.Sub(r.now)}, nil
	}
	if err := rot.RotateFinish(r.ctx, r.spec, cur, toState(rs)); err != nil {
		r.event(corev1.EventTypeWarning, conditions.EventRotationFailed, "Rotate", "RotateFinish failed: %s", err)
		return rotationOutcome{}, fmt.Errorf("RotateFinish: %w", err)
	}
	rs.Previous, rs.RetireAfter = "", nil
	r.claim.SetRotationStatus(rs)
	r.event(corev1.EventTypeNormal, conditions.EventRotationFinished, "Rotate",
		"retired the previous credential generation")
	return rotationOutcome{finished: true}, nil
}
