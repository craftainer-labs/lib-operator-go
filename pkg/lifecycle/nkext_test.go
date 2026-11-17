package lifecycle_test

import (
	"context"
	"errors"
	"slices"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/craftainer-labs/lib-operator-go/pkg/binding"
	"github.com/craftainer-labs/lib-operator-go/pkg/conditions"
	"github.com/craftainer-labs/lib-operator-go/pkg/lifecycle"
)

// nonRot is an External that is not a Rotator and declares rotation not applicable.
type nonRot struct {
	*fakeExt
	applies bool
}

func (n nonRot) RotationNotApplicable() bool { return n.applies }

// rebuilder is an External whose binding holds no secret.
type rebuilder struct {
	*fakeExt
	data  binding.Data
	err   error
	calls *int
}

func (b rebuilder) RebuildBinding(context.Context, spec, obj) (binding.Data, error) {
	*b.calls++
	return b.data, b.err
}

// rotRebuilder is a Rotator that also declares its binding rebuildable and
// rotation not applicable: the rebuild wins, rotation stays a Rotator's.
type rotRebuilder struct {
	*fakeRot
	rebuilder rebuilder
}

func (r rotRebuilder) RebuildBinding(ctx context.Context, s spec, o obj) (binding.Data, error) {
	return r.rebuilder.RebuildBinding(ctx, s, o)
}
func (rotRebuilder) RotationNotApplicable() bool { return true }

func TestNonRotatorAcknowledgesTheRotateAnnotation(t *testing.T) {
	h := newHarness(t, nil, boundSecret(), gadget(trigger("now"), withPolicy(hours(1), nil)))
	if err := h.rotationAt(nonRot{fakeExt: existing(), applies: true}, now); err != nil {
		t.Fatal(err)
	}
	g := h.gadget("g")
	wantCond(t, g.Status.Conditions, ready, metav1.ConditionTrue, conditions.Available)
	wantCond(t, g.Status.Conditions, synced, metav1.ConditionTrue, conditions.ReconcileSuccess)
	if rs := g.Status.Rotation; rs == nil || rs.LastTrigger != "now" || rs.Active != "" || rs.Sequence != 0 {
		t.Errorf("status.rotation = %+v: the trigger is recorded and nothing rotated", rs)
	}
	h.rec.AssertEvent(t, corev1.EventTypeWarning, conditions.EventRotationNotApplicable, "does not apply")

	// The same value again is already handled: no second Event.
	h2 := newHarness(t, nil, boundSecret(), gadget(trigger("now")))
	if err := h2.rotationAt(nonRot{fakeExt: existing(), applies: true}, now); err != nil {
		t.Fatal(err)
	}
	if err := h2.rotationAt(nonRot{fakeExt: existing(), applies: true}, now); err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, r := range h2.rec.Reasons() {
		if r == conditions.EventRotationNotApplicable {
			n++
		}
	}
	if n != 1 {
		t.Errorf("RotationNotApplicable events = %d, want 1", n)
	}
}

func TestNonRotatorWithoutATriggerStaysInSync(t *testing.T) {
	h := newHarness(t, nil, boundSecret(), gadget(withPolicy(hours(1), nil)))
	if err := h.rotationAt(nonRot{fakeExt: existing(), applies: true}, now); err != nil {
		t.Fatal(err)
	}
	wantCond(t, h.gadget("g").Status.Conditions, synced, metav1.ConditionTrue, conditions.ReconcileSuccess)
}

func TestNonRotatorThatSaysFalseIsStillUnsupported(t *testing.T) {
	h := newHarness(t, nil, boundSecret(), gadget(trigger("now")))
	if err := h.rotationAt(nonRot{fakeExt: existing(), applies: false}, now); err != nil {
		t.Fatal(err)
	}
	wantCond(t, h.gadget("g").Status.Conditions, synced, metav1.ConditionFalse, conditions.RotationUnsupported)
}

func TestRotatorIsNeverAskedIfRotationIsNotApplicable(t *testing.T) {
	h := newHarness(t, nil, boundSecret(), gadget(trigger("now")))
	rot := rotRebuilder{fakeRot: existingRot()}
	if err := h.rotationAt(rot, now); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(rot.Calls(), "RotateBegin") {
		t.Errorf("calls = %v: a Rotator keeps rotating", rot.Calls())
	}
	h.rec.AssertReasons(t, conditions.EventRotationStarted)
}

func TestMissingBindingSecretIsRebuilt(t *testing.T) {
	calls := 0
	ext := rebuilder{fakeExt: existing(), data: binding.Data{"iamResourceArn": []byte("arn:x")}, calls: &calls}
	h := newHarness(t, nil, gadget())
	res, err := h.reconcileGadget(ext, h.credOpts())
	wantResult(t, res, err, poll)
	if calls != 1 {
		t.Errorf("RebuildBinding calls = %d, want 1", calls)
	}
	if got := ext.Calls(); !slices.Equal(got, []string{"Observe"}) {
		t.Errorf("backend calls = %v: nothing is issued", got)
	}
	if s := h.secret("g"); string(s.Data["iamResourceArn"]) != "arn:x" || string(s.Data["type"]) != "postgresql" {
		t.Errorf("secret data = %v", s.Data)
	}
	g := h.gadget("g")
	if g.Status.Binding == nil || g.Status.Binding.Name != "g" {
		t.Errorf("status.binding = %+v", g.Status.Binding)
	}
	wantCond(t, g.Status.Conditions, ready, metav1.ConditionTrue, conditions.Available)
	wantCond(t, g.Status.Conditions, synced, metav1.ConditionTrue, conditions.ReconcileSuccess)
	h.rec.AssertEvent(t, corev1.EventTypeNormal, conditions.EventBindingRepaired, "rebuilt")
}

func TestRebuilderNeedsNoUpdatePolicyAndBeatsTheRotatorPath(t *testing.T) {
	calls := 0
	rot := rotRebuilder{fakeRot: existingRot(), rebuilder: rebuilder{data: binding.Data{"k": []byte("v")}, calls: &calls}}
	h := newHarness(t, nil, gadget(withPolicies("Observe", "Create")))
	if _, err := h.reconcileGadget(rot, h.credOpts()); err != nil {
		t.Fatal(err)
	}
	if calls != 1 || slices.Contains(rot.Calls(), "RotateBegin") {
		t.Errorf("rebuild calls = %d, backend calls = %v", calls, rot.Calls())
	}
	if string(h.secret("g").Data["k"]) != "v" {
		t.Errorf("secret = %v", h.secret("g").Data)
	}
}

func TestRebuilderLeavesAnExistingSecretAlone(t *testing.T) {
	calls := 0
	ext := rebuilder{fakeExt: existing(), data: binding.Data{"k": []byte("v")}, calls: &calls}
	h := newHarness(t, nil, boundSecret(), gadget())
	res, err := h.reconcileGadget(ext, h.credOpts())
	wantResult(t, res, err, poll)
	if calls != 0 {
		t.Errorf("RebuildBinding called %d times for a Secret that exists", calls)
	}
}

func TestRebuildFailures(t *testing.T) {
	boom := errors.New("boom")
	tests := []struct {
		name string
		data binding.Data
		err  error
		want error
	}{
		{"an error", nil, boom, boom},
		{"no data", nil, nil, lifecycle.ErrNoBindingData},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			calls := 0
			ext := rebuilder{fakeExt: existing(), data: tt.data, err: tt.err, calls: &calls}
			h := newHarness(t, nil, gadget())
			_, err := h.reconcileGadget(ext, h.credOpts())
			if !errors.Is(err, tt.want) {
				t.Fatalf("err = %v, want %v", err, tt.want)
			}
			wantCond(t, h.gadget("g").Status.Conditions, synced, metav1.ConditionFalse, conditions.ReconcileError)
		})
	}
}

func TestRebuildWriteFailure(t *testing.T) {
	boom := errors.New("api down")
	funcs := &interceptor.Funcs{Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
		if _, ok := obj.(*corev1.Secret); ok {
			return boom
		}
		return c.Create(ctx, obj, opts...)
	}}
	calls := 0
	ext := rebuilder{fakeExt: existing(), data: binding.Data{"k": []byte("v")}, calls: &calls}
	h := newHarness(t, funcs, gadget())
	if _, err := h.reconcileGadget(ext, h.credOpts()); !errors.Is(err, boom) {
		t.Fatalf("err = %v, want %v", err, boom)
	}
}
