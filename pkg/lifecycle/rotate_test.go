package lifecycle_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/craftainer-labs/lib-operator-go/pkg/apis/common"
	"github.com/craftainer-labs/lib-operator-go/pkg/binding"
	"github.com/craftainer-labs/lib-operator-go/pkg/conditions"
	"github.com/craftainer-labs/lib-operator-go/pkg/lifecycle"
)

func existingRot() *fakeRot {
	r := &fakeRot{NextData: binding.Data{"password": []byte("NEW-PASSWORD")}, NextGeneration: "gen-1"}
	r.Obs = lifecycle.Observation[obj]{Exists: true, UpToDate: true, Current: "cur"}
	return r
}

func hours(n int) *metav1.Duration { return &metav1.Duration{Duration: time.Duration(n) * time.Hour} }

// withPolicy sets spec.rotationPolicy; a zero interval leaves it unset.
func withPolicy(interval, grace *metav1.Duration) func(*testGadget) {
	return func(g *testGadget) {
		g.Spec.RotationPolicy = &common.RotationPolicy{Interval: interval, GracePeriod: grace}
	}
}

func withRotation(rs common.RotationStatus) func(*testGadget) {
	return func(g *testGadget) { g.Status.Rotation = &rs }
}

func trigger(v string) func(*testGadget) {
	return func(g *testGadget) {
		if g.Annotations == nil {
			g.Annotations = map[string]string{}
		}
		g.Annotations[lifecycle.AnnotationRotate] = v
	}
}

func withPolicies(p ...common.ManagementPolicy) func(*testGadget) {
	return func(g *testGadget) { g.Spec.ManagementPolicies = p }
}

func ts(t time.Time) *metav1.Time { m := metav1.NewTime(t); return &m }

func (h *harness) rotationAt(ext lifecycle.External[spec, obj], at time.Time) error {
	h.t.Helper()
	opts := h.credOpts()
	opts.Now = func() time.Time { return at }
	_, err := h.reconcileGadget(ext, opts)
	return err
}

func TestRotationDueByInterval(t *testing.T) {
	h := newHarness(t, nil, boundSecret(), gadget(withPolicy(hours(1), nil)))
	rot := existingRot()
	if err := h.rotationAt(rot, now); err != nil {
		t.Fatal(err)
	}
	if got := rot.Calls(); !slices.Equal(got, []string{"Observe", "RotateBegin"}) {
		t.Errorf("calls = %v", got)
	}
	if got := rot.BeginStates(); len(got) != 1 || got[0] != (lifecycle.RotationState{}) {
		t.Errorf("RotateBegin state = %+v, want the zero state", got)
	}
	if got := string(h.secret("g").Data["password"]); got != "NEW-PASSWORD" {
		t.Errorf("binding password = %q: the new data goes into the Secret", got)
	}
	g := h.gadget("g")
	rs := g.Status.Rotation
	if rs == nil || rs.Active != "gen-1" || rs.Previous != "" || rs.Sequence != 1 ||
		rs.LastRotated == nil || !rs.LastRotated.Time.Equal(now) ||
		rs.RetireAfter == nil || !rs.RetireAfter.Time.Equal(now.Add(time.Hour)) {
		t.Errorf("status.rotation = %+v", rs)
	}
	wantCond(t, g.Status.Conditions, ready, metav1.ConditionTrue, conditions.Available)
	wantCond(t, g.Status.Conditions, synced, metav1.ConditionTrue, conditions.Rotating)
	h.rec.AssertReasons(t, conditions.EventRotationStarted)
	h.rec.AssertNoSecret(t, "NEW-PASSWORD")
}

func TestRotationGracePeriodHoldsAndRequeuesAtRetireAfter(t *testing.T) {
	grace := &metav1.Duration{Duration: 5 * time.Minute}
	h := newHarness(t, nil, boundSecret(), gadget(withPolicy(hours(1), grace)))
	rot := existingRot()
	opts := h.credOpts()
	res, err := h.reconcileGadget(rot, opts)
	wantResult(t, res, err, 5*time.Minute) // min(poll, retireAfter) = the 5m grace period

	// Inside the grace period nothing happens, and the requeue shrinks to what is left.
	opts.Now = func() time.Time { return now.Add(3 * time.Minute) }
	res, err = h.reconcileGadget(rot, opts)
	wantResult(t, res, err, 2*time.Minute)
	if got := rot.Calls(); !slices.Equal(got, []string{"Observe", "RotateBegin", "Observe"}) {
		t.Errorf("calls = %v", got)
	}
	wantCond(t, h.gadget("g").Status.Conditions, synced, metav1.ConditionTrue, conditions.Rotating)
}

func TestRotationFinishesAfterTheGracePeriod(t *testing.T) {
	h := newHarness(t, nil, boundSecret(), gadget(trigger("go")))
	rot := existingRot()
	if err := h.rotationAt(rot, now); err != nil {
		t.Fatal(err)
	}
	if err := h.rotationAt(rot, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if got := rot.Calls(); !slices.Equal(got, []string{"Observe", "RotateBegin", "Observe", "RotateFinish"}) {
		t.Errorf("calls = %v", got)
	}
	if got := rot.FinishStates(); len(got) != 1 || got[0] != (lifecycle.RotationState{Active: "gen-1", Sequence: 1}) {
		t.Errorf("RotateFinish state = %+v", got)
	}
	g := h.gadget("g")
	if rs := g.Status.Rotation; rs.Previous != "" || rs.RetireAfter != nil || rs.Active != "gen-1" || rs.Sequence != 1 {
		t.Errorf("status.rotation = %+v", rs)
	}
	wantCond(t, g.Status.Conditions, synced, metav1.ConditionTrue, conditions.ReconcileSuccess)
	h.rec.AssertReasons(t, conditions.EventRotationStarted, conditions.EventRotationFinished)

	// The annotation is handled, so nothing is due.
	if err := h.rotationAt(rot, now.Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if n := len(rot.BeginStates()); n != 1 {
		t.Errorf("RotateBegin calls = %d, want 1", n)
	}
}

func TestSecondRotationRetiresTheFirstGeneration(t *testing.T) {
	h := newHarness(t, nil, boundSecret(), gadget(withPolicy(hours(1), nil),
		withRotation(common.RotationStatus{Active: "gen-1", Sequence: 1, LastRotated: ts(now.Add(-2 * time.Hour))})))
	rot := existingRot()
	rot.NextGeneration = "gen-2"
	if err := h.rotationAt(rot, now); err != nil {
		t.Fatal(err)
	}
	if got := rot.BeginStates(); len(got) != 1 || got[0] != (lifecycle.RotationState{Active: "gen-1", Sequence: 1}) {
		t.Errorf("RotateBegin state = %+v", got)
	}
	rs := h.gadget("g").Status.Rotation
	if rs.Active != "gen-2" || rs.Previous != "gen-1" || rs.Sequence != 2 || rs.RetireAfter == nil {
		t.Errorf("status.rotation = %+v", rs)
	}
	// The grace period is over: RotateFinish gets the state that has Previous set.
	if err := h.rotationAt(rot, now.Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if got := rot.FinishStates(); len(got) != 1 || got[0] != (lifecycle.RotationState{Active: "gen-2", Previous: "gen-1", Sequence: 2}) {
		t.Errorf("RotateFinish state = %+v", got)
	}
}

func TestRotationIntervalCounting(t *testing.T) {
	tests := []struct {
		name string
		mut  []func(*testGadget)
		want int // RotateBegin calls
	}{
		{"never rotated and the claim is older than the interval", []func(*testGadget){withPolicy(hours(1), nil)}, 1},
		{"never rotated and the claim is younger than the interval", []func(*testGadget){withPolicy(hours(48), nil)}, 0},
		{"rotated less than an interval ago", []func(*testGadget){withPolicy(hours(1), nil),
			withRotation(common.RotationStatus{Active: "g", Sequence: 1, LastRotated: ts(now.Add(-30 * time.Minute))})}, 0},
		{"rotated exactly an interval ago", []func(*testGadget){withPolicy(hours(1), nil),
			withRotation(common.RotationStatus{Active: "g", Sequence: 1, LastRotated: ts(now.Add(-time.Hour))})}, 1},
		{"a policy without an interval rotates on the trigger only", []func(*testGadget){withPolicy(nil, nil)}, 0},
		{"no policy at all", nil, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t, nil, boundSecret(), gadget(tt.mut...))
			rot := existingRot()
			if err := h.rotationAt(rot, now); err != nil {
				t.Fatal(err)
			}
			if got := len(rot.BeginStates()); got != tt.want {
				t.Errorf("RotateBegin calls = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestRotationOnTheTriggerAnnotation(t *testing.T) {
	h := newHarness(t, nil, boundSecret(), gadget(trigger("one")))
	rot := existingRot()
	if err := h.rotationAt(rot, now); err != nil {
		t.Fatal(err)
	}
	if rs := h.gadget("g").Status.Rotation; rs == nil || rs.LastTrigger != "one" || rs.Sequence != 1 {
		t.Fatalf("status.rotation = %+v", rs)
	}
	// Finish the grace period; the same annotation value is already handled.
	if err := h.rotationAt(rot, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := h.rotationAt(rot, now.Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if n := len(rot.BeginStates()); n != 1 {
		t.Fatalf("RotateBegin calls = %d, want 1: an unchanged annotation is not a new trigger", n)
	}
	// Any new string triggers one more.
	g := h.gadget("g")
	g.Annotations[lifecycle.AnnotationRotate] = "two"
	if err := h.c.Update(h.ctx, g); err != nil {
		t.Fatal(err)
	}
	rot.NextGeneration = "gen-2"
	if err := h.rotationAt(rot, now.Add(3*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if rs := h.gadget("g").Status.Rotation; rs.LastTrigger != "two" || rs.Sequence != 2 || rs.Active != "gen-2" {
		t.Errorf("status.rotation = %+v", rs)
	}
}

// A trigger that arrives during the grace period is consumed and rejected with
// a Warning, and does not rotate later (ADR 0002, section 8).
func TestTriggerDuringTheGracePeriodIsConsumedAndRejected(t *testing.T) {
	h := newHarness(t, nil, boundSecret(), gadget(withPolicy(hours(48), nil), trigger("new"),
		withRotation(common.RotationStatus{Active: "gen-1", Previous: "gen-0", Sequence: 1,
			RetireAfter: ts(now.Add(30 * time.Minute)), LastRotated: ts(now.Add(-30 * time.Minute)), LastTrigger: "old"})))
	rot := existingRot()
	if err := h.rotationAt(rot, now); err != nil {
		t.Fatal(err)
	}
	if n := len(rot.BeginStates()); n != 0 {
		t.Errorf("RotateBegin calls = %d, want 0 while Previous is set", n)
	}
	rs := h.gadget("g").Status.Rotation
	if rs.LastTrigger != "new" || rs.Previous != "gen-0" || rs.RetireAfter == nil {
		t.Errorf("status.rotation = %+v: the trigger is consumed, the grace state kept", rs)
	}
	h.rec.AssertEvent(t, corev1.EventTypeWarning, conditions.EventRotationDeferred, "set a new value")

	// The same reconcile finishes the grace period when it is over, and the
	// consumed value does not start a rotation afterwards.
	if err := h.rotationAt(rot, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := h.rotationAt(rot, now.Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if n := len(rot.BeginStates()); n != 0 || len(rot.FinishStates()) != 1 {
		t.Errorf("RotateBegin %d, RotateFinish %d", n, len(rot.FinishStates()))
	}
	h.rec.AssertReasons(t, conditions.EventRotationDeferred, conditions.EventObserved, conditions.EventRotationFinished)
}

func TestRotationTriggerWhoseGraceEndsInTheSameReconcile(t *testing.T) {
	h := newHarness(t, nil, boundSecret(), gadget(trigger("new"),
		withRotation(common.RotationStatus{Active: "gen-1", Previous: "gen-0", RetireAfter: ts(now.Add(-time.Minute)), LastTrigger: "old"})))
	rot := existingRot()
	if err := h.rotationAt(rot, now); err != nil {
		t.Fatal(err)
	}
	rs := h.gadget("g").Status.Rotation
	if len(rot.BeginStates()) != 0 || len(rot.FinishStates()) != 1 || rs.LastTrigger != "new" || rs.Previous != "" {
		t.Errorf("begin %d finish %d rotation %+v", len(rot.BeginStates()), len(rot.FinishStates()), rs)
	}
}

func TestGraceWithoutRetireAfterFinishesAtOnce(t *testing.T) {
	h := newHarness(t, nil, boundSecret(), gadget(withRotation(common.RotationStatus{Active: "gen-1", Previous: "gen-0", Sequence: 1})))
	rot := existingRot()
	if err := h.rotationAt(rot, now); err != nil {
		t.Fatal(err)
	}
	if got := rot.FinishStates(); len(got) != 1 || got[0].Previous != "gen-0" {
		t.Errorf("RotateFinish states = %+v", got)
	}
}

func TestHardCutRotation(t *testing.T) {
	h := newHarness(t, nil, boundSecret(), gadget(trigger("go")))
	rot := existingRot()
	rot.HardCutBackend = true
	if err := h.rotationAt(rot, now); err != nil {
		t.Fatal(err)
	}
	g := h.gadget("g")
	if rs := g.Status.Rotation; rs.Active != "gen-1" || rs.Previous != "" || rs.RetireAfter != nil || rs.Sequence != 1 {
		t.Errorf("status.rotation = %+v: a hard cut has no grace period", rs)
	}
	wantCond(t, g.Status.Conditions, synced, metav1.ConditionTrue, conditions.ReconcileSuccess)
	h.rec.AssertReasons(t, conditions.EventRotatedWithoutOverlap)
	if err := h.rotationAt(rot, now.Add(24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if n := len(rot.FinishStates()); n != 0 {
		t.Errorf("RotateFinish calls = %d", n)
	}
}

func TestRotationNeedsUpdate(t *testing.T) {
	tests := []struct {
		name     string
		policies []common.ManagementPolicy
		blocked  bool
	}{
		{"star", nil, false},
		{"Observe+Update", policies("Observe", "Update"), false},
		{"Observe+Create+Delete", policies("Observe", "Create", "Delete"), true},
		{"Observe only", policies("Observe"), true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t, nil, boundSecret(), gadget(withPolicy(hours(1), nil), trigger("x"), withPolicies(tt.policies...)))
			rot := existingRot()
			if err := h.rotationAt(rot, now); err != nil {
				t.Fatal(err)
			}
			begun := len(rot.BeginStates()) == 1
			if begun == tt.blocked {
				t.Errorf("RotateBegin called = %v, blocked = %v", begun, tt.blocked)
			}
			g := h.gadget("g")
			if tt.blocked {
				wantCond(t, g.Status.Conditions, synced, metav1.ConditionFalse, conditions.ManagementPolicyBlocked)
				wantCond(t, g.Status.Conditions, ready, metav1.ConditionTrue, conditions.Available)
				h.rec.AssertEvent(t, corev1.EventTypeWarning, conditions.ManagementPolicyBlocked, "Update")
			}
		})
	}
}

func TestRotationInGraceStillFinishesWithoutUpdate(t *testing.T) {
	h := newHarness(t, nil, boundSecret(), gadget(withPolicies("Observe", "Create"),
		withRotation(common.RotationStatus{Active: "gen-1", Previous: "gen-0", RetireAfter: ts(now.Add(-time.Minute))})))
	rot := existingRot()
	if err := h.rotationAt(rot, now); err != nil {
		t.Fatal(err)
	}
	if n := len(rot.FinishStates()); n != 1 {
		t.Errorf("RotateFinish calls = %d: a credential must not stay valid for lack of Update", n)
	}
}

func TestRotationUnsupportedByTheBackend(t *testing.T) {
	tests := []struct {
		name string
		mut  []func(*testGadget)
		want bool
	}{
		{"a policy with an interval", []func(*testGadget){withPolicy(hours(1), nil)}, true},
		{"a policy without an interval", []func(*testGadget){withPolicy(nil, nil)}, true},
		{"a trigger annotation", []func(*testGadget){trigger("now")}, true},
		{"a handled trigger annotation", []func(*testGadget){trigger("old"), withRotation(common.RotationStatus{LastTrigger: "old"})}, false},
		{"nothing asked", nil, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t, nil, boundSecret(), gadget(tt.mut...))
			if err := h.rotationAt(existing(), now); err != nil {
				t.Fatal(err)
			}
			g := h.gadget("g")
			wantCond(t, g.Status.Conditions, ready, metav1.ConditionTrue, conditions.Available)
			if tt.want {
				wantCond(t, g.Status.Conditions, synced, metav1.ConditionFalse, conditions.RotationUnsupported)
			} else {
				wantCond(t, g.Status.Conditions, synced, metav1.ConditionTrue, conditions.ReconcileSuccess)
			}
		})
	}
}

func TestRotationFailures(t *testing.T) {
	boom := errors.New("backend refused")
	t.Run("RotateBegin", func(t *testing.T) {
		h := newHarness(t, nil, boundSecret(), gadget(trigger("x")))
		rot := existingRot()
		rot.BeginErr = boom
		if err := h.rotationAt(rot, now); !errors.Is(err, boom) {
			t.Fatalf("err = %v", err)
		}
		g := h.gadget("g")
		wantCond(t, g.Status.Conditions, synced, metav1.ConditionFalse, conditions.ReconcileError)
		if g.Status.Rotation != nil {
			t.Errorf("status.rotation = %+v after a failed begin", g.Status.Rotation)
		}
		h.rec.AssertEvent(t, corev1.EventTypeWarning, conditions.EventRotationFailed, "RotateBegin")
		if got := string(h.secret("g").Data["password"]); got != "OLD" {
			t.Errorf("the binding Secret was rewritten for a failed rotation: %q", got)
		}
	})
	t.Run("RotateBegin unreachable", func(t *testing.T) {
		h := newHarness(t, nil, boundSecret(), gadget(trigger("x")))
		rot := existingRot()
		rot.BeginErr = fmt.Errorf("dial: %w", lifecycle.ErrUnreachable)
		if err := h.rotationAt(rot, now); !errors.Is(err, lifecycle.ErrUnreachable) {
			t.Fatalf("err = %v", err)
		}
		wantCond(t, h.gadget("g").Status.Conditions, synced, metav1.ConditionFalse, conditions.ServerUnreachable)
	})
	t.Run("the binding Secret cannot be written", func(t *testing.T) {
		secretBoom := errors.New("secret update boom")
		h := newHarness(t, &interceptor.Funcs{Update: func(ctx context.Context, c client.WithWatch, o client.Object, opts ...client.UpdateOption) error {
			if _, ok := o.(*corev1.Secret); ok {
				return secretBoom
			}
			return c.Update(ctx, o, opts...)
		}}, boundSecret(), gadget(trigger("x")))
		if err := h.rotationAt(existingRot(), now); !errors.Is(err, secretBoom) {
			t.Fatalf("err = %v", err)
		}
		h.rec.AssertEvent(t, corev1.EventTypeWarning, conditions.EventRotationFailed, "binding Secret")
		if h.gadget("g").Status.Rotation != nil {
			t.Error("a rotation was recorded though its credential was never delivered")
		}
	})
	t.Run("RotateFinish", func(t *testing.T) {
		h := newHarness(t, nil, boundSecret(), gadget(withRotation(common.RotationStatus{Active: "gen-1", Previous: "gen-0", RetireAfter: ts(now.Add(-time.Minute))})))
		rot := existingRot()
		rot.FinishErr = boom
		if err := h.rotationAt(rot, now); !errors.Is(err, boom) {
			t.Fatalf("err = %v", err)
		}
		if rs := h.gadget("g").Status.Rotation; rs.Previous != "gen-0" || rs.RetireAfter == nil {
			t.Errorf("status.rotation = %+v: the grace state must survive a failed finish", rs)
		}
		h.rec.AssertEvent(t, corev1.EventTypeWarning, conditions.EventRotationFailed, "RotateFinish")
	})
}

func TestNoRotationAfterACreateOrUpdate(t *testing.T) {
	for name, ext := range map[string]func() *fakeRot{
		"create": func() *fakeRot { return &fakeRot{NextGeneration: "gen-1"} },
		"update": func() *fakeRot {
			r := &fakeRot{NextGeneration: "gen-1"}
			r.Obs = lifecycle.Observation[obj]{Exists: true, Current: "cur"}
			return r
		},
	} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t, nil, boundSecret(), gadget(trigger("x")))
			rot := ext()
			rot.CreateData = binding.Data{"password": []byte("p")}
			rot.UpdateData = binding.Data{"password": []byte("p")}
			if err := h.rotationAt(rot, now); err != nil {
				t.Fatal(err)
			}
			if n := len(rot.BeginStates()); n != 0 {
				t.Errorf("RotateBegin calls = %d: rotate in the reconcile after the change", n)
			}
		})
	}
}

// An explicit gracePeriod of 0 is a hard cut: RotateFinish runs in the same
// reconcile, right after the binding Secret is written (ADR 0002 section 8).
func TestZeroGracePeriodRetiresThePreviousGenerationAtOnce(t *testing.T) {
	zero := &metav1.Duration{}
	h := newHarness(t, nil, boundSecret(), gadget(trigger("go"), withPolicy(nil, zero),
		withRotation(common.RotationStatus{Active: "gen-0"})))
	rot := existingRot()
	res, err := h.reconcileGadget(rot, h.credOpts())
	wantResult(t, res, err, poll) // never a zero RequeueAfter

	if got := rot.Calls(); !slices.Equal(got, []string{"Observe", "RotateBegin", "RotateFinish"}) {
		t.Errorf("calls = %v", got)
	}
	if got := rot.FinishStates(); len(got) != 1 || got[0] != (lifecycle.RotationState{Active: "gen-1", Previous: "gen-0", Sequence: 1}) {
		t.Errorf("RotateFinish state = %+v", got)
	}
	g := h.gadget("g")
	if rs := g.Status.Rotation; rs.Active != "gen-1" || rs.Previous != "" || rs.RetireAfter != nil || rs.Sequence != 1 || rs.LastTrigger != "go" {
		t.Errorf("status.rotation = %+v", rs)
	}
	if got := string(h.secret("g").Data["password"]); got != "NEW-PASSWORD" {
		t.Errorf("binding password = %q", got)
	}
	wantCond(t, g.Status.Conditions, synced, metav1.ConditionTrue, conditions.ReconcileSuccess)
	h.rec.AssertReasons(t, conditions.EventRotationStarted, conditions.EventRotationFinished)
}

func TestZeroGracePeriodFinishFailureIsRetried(t *testing.T) {
	boom := errors.New("finish refused")
	h := newHarness(t, nil, boundSecret(), gadget(trigger("go"), withPolicy(nil, &metav1.Duration{}),
		withRotation(common.RotationStatus{Active: "gen-0"})))
	rot := existingRot()
	rot.FinishErr = boom
	if _, err := h.reconcileGadget(rot, h.credOpts()); !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
	if rs := h.gadget("g").Status.Rotation; rs.Active != "gen-1" || rs.Previous != "gen-0" || rs.RetireAfter == nil {
		t.Fatalf("status.rotation = %+v: the due grace state must be kept", rs)
	}
	rot.FinishErr = nil
	if _, err := h.reconcileGadget(rot, h.credOpts()); err != nil {
		t.Fatal(err)
	}
	if rs := h.gadget("g").Status.Rotation; rs.Previous != "" || rs.RetireAfter != nil {
		t.Errorf("status.rotation = %+v", rs)
	}
	if n := len(rot.BeginStates()); n != 1 || len(rot.FinishStates()) != 2 {
		t.Errorf("RotateBegin %d, RotateFinish %d", n, len(rot.FinishStates()))
	}
}

func TestZeroGracePeriodOnAHardCutBackendNeedsNoFinish(t *testing.T) {
	h := newHarness(t, nil, boundSecret(), gadget(trigger("go"), withPolicy(nil, &metav1.Duration{})))
	rot := existingRot()
	rot.HardCutBackend = true
	if _, err := h.reconcileGadget(rot, h.credOpts()); err != nil {
		t.Fatal(err)
	}
	if n := len(rot.FinishStates()); n != 0 {
		t.Errorf("RotateFinish calls = %d", n)
	}
}

// A trigger without Update is consumed and rejected once; it neither repeats
// every poll nor rotates when Update is added later.
func TestTriggerWithoutUpdateIsConsumedOnce(t *testing.T) {
	h := newHarness(t, nil, boundSecret(), gadget(trigger("x"), withPolicies("Observe", "Create")))
	rot := existingRot()
	deferred := func() int {
		n := 0
		for _, r := range h.rec.Reasons() {
			if r == conditions.EventRotationDeferred {
				n++
			}
		}
		return n
	}
	for range 3 {
		if _, err := h.reconcileGadget(rot, h.credOpts()); err != nil {
			t.Fatal(err)
		}
	}
	if deferred() != 1 {
		t.Errorf("RotationDeferred events = %d, want 1", deferred())
	}
	h.rec.AssertEvent(t, corev1.EventTypeWarning, conditions.EventRotationDeferred, "lacks Update")
	g := h.gadget("g")
	if g.Status.Rotation == nil || g.Status.Rotation.LastTrigger != "x" {
		t.Errorf("status.rotation = %+v: the trigger is consumed", g.Status.Rotation)
	}
	wantCond(t, g.Status.Conditions, synced, metav1.ConditionTrue, conditions.ReconcileSuccess)

	g.Spec.ManagementPolicies = []common.ManagementPolicy{"Observe", "Create", "Update"}
	if err := h.c.Update(h.ctx, g); err != nil {
		t.Fatal(err)
	}
	if _, err := h.reconcileGadget(rot, h.credOpts()); err != nil {
		t.Fatal(err)
	}
	if n := len(rot.BeginStates()); n != 0 {
		t.Errorf("RotateBegin calls = %d: the consumed trigger must not rotate later", n)
	}
}
