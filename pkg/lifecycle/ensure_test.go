package lifecycle_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/craftainer-labs/lib-operator-go/pkg/apis/common"
	"github.com/craftainer-labs/lib-operator-go/pkg/binding"
	"github.com/craftainer-labs/lib-operator-go/pkg/conditions"
	"github.com/craftainer-labs/lib-operator-go/pkg/lifecycle"
)

// The binding Secret is level-triggered: an up-to-date external object does not
// mean the Secret is still there or still as the framework wrote it.

func TestMissingBindingSecretIsRecreatedThroughARotation(t *testing.T) {
	h := newHarness(t, nil, gadget())
	rot := existingRot()
	res, err := h.reconcileGadget(rot, h.credOpts())
	wantResult(t, res, err, poll)

	if got := rot.Calls(); !slices.Equal(got, []string{"Observe", "RotateBegin"}) {
		t.Errorf("calls = %v: the backend is asked to rotate, never to Create or Update", got)
	}
	s := h.secret("g")
	if string(s.Data["password"]) != "NEW-PASSWORD" || string(s.Data["type"]) != "postgresql" {
		t.Errorf("secret data = %v", s.Data)
	}
	g := h.gadget("g")
	if rs := g.Status.Rotation; rs == nil || rs.Active != "gen-1" || rs.Sequence != 1 {
		t.Errorf("status.rotation = %+v", rs)
	}
	if g.Status.Binding == nil || g.Status.Binding.Name != "g" {
		t.Errorf("status.binding = %+v", g.Status.Binding)
	}
	wantCond(t, g.Status.Conditions, ready, metav1.ConditionTrue, conditions.Available)
	wantCond(t, g.Status.Conditions, synced, metav1.ConditionTrue, conditions.Rotating)
	h.rec.AssertReasons(t, conditions.EventRotationStarted, conditions.EventBindingRepaired)
}

func TestMissingBindingSecretThatCannotBeRecreated(t *testing.T) {
	tests := []struct {
		name  string
		ext   func() lifecycle.External[spec, obj]
		mut   []func(*testGadget)
		wants string
	}{
		{"the External is not a Rotator", func() lifecycle.External[spec, obj] { return existing() }, nil, "not a Rotator"},
		{"managementPolicies lacks Update", func() lifecycle.External[spec, obj] { return existingRot() },
			[]func(*testGadget){withPolicies("Observe", "Create")}, "Update management policy"},
		{"a grace period is running", func() lifecycle.External[spec, obj] { return existingRot() },
			[]func(*testGadget){withRotation(common.RotationStatus{Active: "gen-1", Previous: "gen-0", RetireAfter: ts(now.Add(time.Hour))})},
			"grace period is running"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t, nil, gadget(tt.mut...))
			ext := tt.ext()
			res, err := h.reconcileGadget(ext, h.credOpts())
			wantResult(t, res, err, poll)
			g := h.gadget("g")
			wantCond(t, g.Status.Conditions, ready, metav1.ConditionFalse, conditions.BindingSecretMissing)
			wantCond(t, g.Status.Conditions, synced, metav1.ConditionFalse, conditions.BindingSecretMissing)
			if msg := cond(g.Status.Conditions, synced).Message; !strings.Contains(msg, tt.wants) {
				t.Errorf("message = %q, want it to mention %q", msg, tt.wants)
			}
			h.rec.AssertEvent(t, corev1.EventTypeWarning, conditions.BindingSecretMissing, tt.wants)
			if err := h.c.Get(h.ctx, client.ObjectKey{Namespace: "ns", Name: "g"}, &corev1.Secret{}); !apierrors.IsNotFound(err) {
				t.Errorf("a Secret was written: %v", err)
			}
			for _, call := range callsOf(ext) {
				if call == "Create" || call == "Update" || call == "RotateBegin" {
					t.Errorf("the backend was asked to %s", call)
				}
			}
		})
	}
}

func callsOf(ext lifecycle.External[spec, obj]) []string {
	switch e := ext.(type) {
	case *fakeRot:
		return e.Calls()
	case *fakeExt:
		return e.Calls()
	}
	return nil
}

func TestObserveOnlyClaimHasNoBindingSecretToKeep(t *testing.T) {
	h := newHarness(t, nil, gadget(withPolicies("Observe")))
	if _, err := h.reconcileGadget(existing(), h.credOpts()); err != nil {
		t.Fatal(err)
	}
	wantCond(t, h.gadget("g").Status.Conditions, synced, metav1.ConditionTrue, conditions.ReconcileSuccess)
}

func TestEditedBindingSecretIsRepairedWithoutTheBackend(t *testing.T) {
	edited := boundSecret()
	delete(edited.Labels, binding.LabelClaim)
	edited.Data["provider"] = []byte("evil.example")
	h := newHarness(t, nil, gadget(), edited)
	ext := existing()
	if _, err := h.reconcileGadget(ext, h.credOpts()); err != nil {
		t.Fatal(err)
	}
	if got := ext.Calls(); !slices.Equal(got, []string{"Observe"}) {
		t.Errorf("calls = %v", got)
	}
	s := h.secret("g")
	if s.Labels[binding.LabelClaim] != "g" || string(s.Data["provider"]) != "craftainer.dev" || string(s.Data["password"]) != "OLD" {
		t.Errorf("secret = labels %v data %v: metadata restored, credentials kept", s.Labels, s.Data)
	}
	h.rec.AssertReasons(t, conditions.EventBindingRepaired, conditions.EventObserved)
	wantCond(t, h.gadget("g").Status.Conditions, synced, metav1.ConditionTrue, conditions.ReconcileSuccess)
}

func TestCurrentBindingSecretIsLeftAlone(t *testing.T) {
	h := newHarness(t, nil, gadget(), boundSecret())
	before := h.secret("g").ResourceVersion
	if _, err := h.reconcileGadget(existing(), h.credOpts()); err != nil {
		t.Fatal(err)
	}
	if h.secret("g").ResourceVersion != before {
		t.Error("a current Secret was rewritten")
	}
}

func TestForeignBindingSecretIsANameCollision(t *testing.T) {
	foreign := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "g", Namespace: "ns"}, Data: map[string][]byte{"k": []byte("v")}}
	h := newHarness(t, nil, gadget(), foreign)
	res, err := h.reconcileGadget(existingRot(), h.credOpts())
	wantResult(t, res, err, poll)
	wantCond(t, h.gadget("g").Status.Conditions, synced, metav1.ConditionFalse, conditions.NameCollision)
	if string(h.secret("g").Data["k"]) != "v" || len(h.secret("g").OwnerReferences) != 0 {
		t.Error("the foreign Secret was modified")
	}
}

func TestBindingSecretInspectionAndRepairFailures(t *testing.T) {
	boom := errors.New("secret boom")
	onSecret := func(f interceptor.Funcs) *interceptor.Funcs { return &f }
	t.Run("the Secret cannot be read", func(t *testing.T) {
		h := newHarness(t, onSecret(interceptor.Funcs{Get: func(ctx context.Context, c client.WithWatch, k client.ObjectKey, o client.Object, opts ...client.GetOption) error {
			if _, ok := o.(*corev1.Secret); ok {
				return boom
			}
			return c.Get(ctx, k, o, opts...)
		}}), gadget())
		if _, err := h.reconcileGadget(existing(), h.credOpts()); !errors.Is(err, boom) {
			t.Errorf("err = %v", err)
		}
		wantCond(t, h.gadget("g").Status.Conditions, synced, metav1.ConditionFalse, conditions.ReconcileError)
	})
	t.Run("the repair cannot be written", func(t *testing.T) {
		edited := boundSecret()
		delete(edited.Labels, binding.LabelClaim)
		h := newHarness(t, onSecret(interceptor.Funcs{Update: func(ctx context.Context, c client.WithWatch, o client.Object, opts ...client.UpdateOption) error {
			if _, ok := o.(*corev1.Secret); ok {
				return boom
			}
			return c.Update(ctx, o, opts...)
		}}), gadget(), edited)
		if _, err := h.reconcileGadget(existing(), h.credOpts()); !errors.Is(err, boom) {
			t.Errorf("err = %v", err)
		}
	})
}

// The operator's Secret cache holds only Secrets with its managed-by label, so a
// user's Secret of the same name is invisible through it.
func TestBindingSecretBehindAFilteredCache(t *testing.T) {
	hide := &interceptor.Funcs{Get: func(ctx context.Context, c client.WithWatch, k client.ObjectKey, o client.Object, opts ...client.GetOption) error {
		if _, ok := o.(*corev1.Secret); ok {
			return apierrors.NewNotFound(schema.GroupResource{Resource: "secrets"}, k.Name)
		}
		return c.Get(ctx, k, o, opts...)
	}}
	foreign := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "g", Namespace: "ns"}, Data: map[string][]byte{"k": []byte("v")}}

	t.Run("without an API reader the collision shows up when the Secret is created", func(t *testing.T) {
		h := newHarness(t, hide, gadget(), foreign)
		res, err := h.reconcileGadget(existingRot(), h.credOpts())
		wantResult(t, res, err, poll)
		wantCond(t, h.gadget("g").Status.Conditions, synced, metav1.ConditionFalse, conditions.NameCollision)
	})
	t.Run("with an API reader it is seen before anything is written", func(t *testing.T) {
		h := newHarness(t, hide, gadget(), foreign)
		rot := existingRot()
		opts := h.credOpts()
		opts.APIReader = fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(foreign.DeepCopy()).Build()
		res, err := h.reconcileGadget(rot, opts)
		wantResult(t, res, err, poll)
		wantCond(t, h.gadget("g").Status.Conditions, synced, metav1.ConditionFalse, conditions.NameCollision)
		if got := rot.Calls(); !slices.Equal(got, []string{"Observe"}) {
			t.Errorf("calls = %v: no credential may be issued into a Secret that is not ours", got)
		}
	})
	t.Run("with an API reader an owned Secret outside the cache is current", func(t *testing.T) {
		h := newHarness(t, hide, gadget())
		opts := h.credOpts()
		opts.APIReader = fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(boundSecret()).Build()
		rot := existingRot()
		if _, err := h.reconcileGadget(rot, opts); err != nil {
			t.Fatal(err)
		}
		if got := rot.Calls(); !slices.Equal(got, []string{"Observe"}) {
			t.Errorf("calls = %v", got)
		}
	})
}
