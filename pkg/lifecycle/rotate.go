package lifecycle

import (
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/craftainer-labs/lib-operator-go/pkg/apis/common"
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
	if rs.Previous != "" || rs.RetireAfter != nil {
		return r.finishIfDue(rot, cur, rs)
	}
	if !r.due(policy, rs, triggered) {
		return rotationOutcome{}, nil
	}
	if !r.pol.Update {
		return rotationOutcome{blocked: true}, nil
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

// begin issues the next generation, writes it to the binding Secret and then
// records it. A crash between the two repeats RotateBegin, which is idempotent.
func (r *run[S, O]) begin(rot Rotator[S, O], cur O, rs common.RotationStatus, trigger string,
	policy *common.RotationPolicy) (rotationOutcome, error) {
	data, gen, err := rot.RotateBegin(r.ctx, r.spec, cur, toState(rs))
	if err != nil {
		r.event(corev1.EventTypeWarning, conditions.EventRotationFailed, "Rotate", "RotateBegin failed: %s", err)
		return rotationOutcome{}, fmt.Errorf("RotateBegin: %w", err)
	}
	if err := r.writeBinding(data); err != nil {
		r.event(corev1.EventTypeWarning, conditions.EventRotationFailed, "Rotate",
			"writing the binding Secret failed: %s", err)
		return rotationOutcome{}, fmt.Errorf("write the rotated binding: %w", err)
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
	if !hardCut {
		grace := policy.GracePeriodOrDefault()
		retire := metav1.NewTime(r.now.Add(grace))
		rs.Previous, rs.RetireAfter = previous, &retire
		out.grace, out.retireIn = true, grace
		reason = conditions.EventRotationStarted
	}
	r.claim.SetRotationStatus(rs)
	r.event(corev1.EventTypeNormal, reason, "Rotate", "issued credential generation %s", rs.Active)
	return out, nil
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
