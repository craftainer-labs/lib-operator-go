package testenv_test

import (
	"context"
	"slices"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/craftainer-labs/lib-operator-go/pkg/apis/common"
	"github.com/craftainer-labs/lib-operator-go/pkg/binding"
	"github.com/craftainer-labs/lib-operator-go/pkg/conditions"
	"github.com/craftainer-labs/lib-operator-go/pkg/internal/testapi"
	"github.com/craftainer-labs/lib-operator-go/pkg/lifecycle"
	"github.com/craftainer-labs/lib-operator-go/pkg/lifecycle/lifecycletest"
	"github.com/craftainer-labs/lib-operator-go/pkg/testenv"
)

type (
	spec     struct{}
	external string
)

// The lifecycle against a real API server: schema validation of everything the
// framework writes, the status subresource, finalizers and optimistic locking.
func TestDataClaimLifecycleAgainstAnAPIServer(t *testing.T) {
	ctx := context.Background()
	ns := env.Namespace(t, nil)
	rec := &testenv.Recorder{}
	opts := lifecycle.Options{Class: lifecycle.ClassData, Recorder: rec, ManagedBy: "operator-test",
		DefaultDeletionPolicy: common.DeletionDelete}

	w := &testapi.Widget{}
	w.Name, w.Namespace = "orders", ns
	w.Spec.ServerRef = common.ServerRef{Kind: "ClusterWidgetServer", Name: "shared"}
	if err := env.Client.Create(ctx, w); err != nil {
		t.Fatal(err)
	}
	ext := &lifecycletest.Fake[spec, external]{}
	key := client.ObjectKeyFromObject(w)
	reconcile := func() {
		t.Helper()
		var cur testapi.Widget
		if err := env.Client.Get(ctx, key, &cur); err != nil {
			t.Fatal(err)
		}
		if _, err := lifecycle.Reconcile(ctx, env.Client, &cur, spec{}, lifecycle.External[spec, external](ext), opts); err != nil {
			t.Fatal(err)
		}
	}

	reconcile()
	var got testapi.Widget
	if err := env.Client.Get(ctx, key, &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Finalizers) != 1 || got.Finalizers[0] != lifecycle.DefaultFinalizer {
		t.Errorf("finalizers = %v", got.Finalizers)
	}
	if got.Status.EffectiveDeletionPolicy != common.DeletionDelete || got.Status.DeletionPolicySource != common.SourceOperatorDefault {
		t.Errorf("pin = %q/%q", got.Status.EffectiveDeletionPolicy, got.Status.DeletionPolicySource)
	}
	for _, typ := range []string{conditions.Ready, conditions.Synced, conditions.ServerReachable} {
		if c := meta.FindStatusCondition(got.Status.Conditions, typ); c == nil || c.Status != metav1.ConditionTrue || c.ObservedGeneration != got.Generation {
			t.Errorf("condition %s = %+v", typ, c)
		}
	}
	reconcile() // steady state: nothing new
	rec.AssertReasons(t, conditions.EventDeletionPolicyResolved, conditions.EventCreated)

	// Deleting: the finalizer deletes the external object and goes.
	if err := env.Client.Delete(ctx, &got); err != nil {
		t.Fatal(err)
	}
	reconcile()
	if err := env.Client.Get(ctx, key, &got); !apierrors.IsNotFound(err) {
		t.Errorf("the claim is still there: %v", err)
	}
	if calls := ext.Calls(); !slices.Contains(calls, "Delete") {
		t.Errorf("calls = %v", calls)
	}
	rec.AssertEvent(t, "Normal", conditions.EventDeleted, "")
}

func TestCredentialClaimLifecycleAgainstAnAPIServer(t *testing.T) {
	ctx := context.Background()
	ns := env.Namespace(t, nil)
	rec := &testenv.Recorder{}
	opts := lifecycle.Options{Class: lifecycle.ClassCredential, BindingType: binding.TypePostgreSQL, Provider: binding.DefaultProvider,
		Recorder: rec, ManagedBy: "operator-test"}

	g := &testapi.Gadget{}
	g.Name, g.Namespace = "orders-app", ns
	g.Spec.ServerRef = common.ServerRef{Kind: "ClusterWidgetServer", Name: "shared"}
	if err := env.Client.Create(ctx, g); err != nil {
		t.Fatal(err)
	}
	ext := &lifecycletest.FakeRotator[spec, external]{
		NextData: binding.Data{"password": []byte("rotated")}, NextGeneration: "gen-1",
	}
	ext.CreateData = binding.Data{"host": []byte("db.example"), "password": []byte("initial")}
	key := client.ObjectKeyFromObject(g)
	reconcile := func() {
		t.Helper()
		var cur testapi.Gadget
		if err := env.Client.Get(ctx, key, &cur); err != nil {
			t.Fatal(err)
		}
		if _, err := lifecycle.Reconcile(ctx, env.Client, &cur, spec{}, lifecycle.External[spec, external](ext), opts); err != nil {
			t.Fatal(err)
		}
	}
	secret := func() map[string][]byte {
		t.Helper()
		var s corev1.Secret
		if err := env.Client.Get(ctx, client.ObjectKey{Namespace: ns, Name: "orders-app"}, &s); err != nil {
			t.Fatal(err)
		}
		return s.Data
	}

	reconcile()
	if data := secret(); string(data["password"]) != "initial" || string(data["type"]) != "postgresql" {
		t.Errorf("binding data = %v", data)
	}
	var got testapi.Gadget
	if err := env.Client.Get(ctx, key, &got); err != nil {
		t.Fatal(err)
	}
	if got.Status.Binding == nil || got.Status.Binding.Name != "orders-app" {
		t.Errorf("status.binding = %+v", got.Status.Binding)
	}

	// The trigger annotation starts a rotation; the status passes the schema.
	got.Annotations = map[string]string{lifecycle.AnnotationRotate: "now"}
	if err := env.Client.Update(ctx, &got); err != nil {
		t.Fatal(err)
	}
	reconcile()
	if data := secret(); string(data["password"]) != "rotated" {
		t.Errorf("binding data after rotation = %v", data)
	}
	if err := env.Client.Get(ctx, key, &got); err != nil {
		t.Fatal(err)
	}
	if rs := got.Status.Rotation; rs == nil || rs.Active != "gen-1" || rs.Sequence != 1 || rs.RetireAfter == nil || rs.LastTrigger != "now" {
		t.Errorf("status.rotation = %+v", rs)
	}
	if c := meta.FindStatusCondition(got.Status.Conditions, conditions.Synced); c == nil || c.Reason != conditions.Rotating {
		t.Errorf("Synced = %+v", c)
	}
	rec.AssertNoSecret(t, "initial", "rotated")

	// Deleting a credential kind deletes the external credential.
	if err := env.Client.Delete(ctx, &got); err != nil {
		t.Fatal(err)
	}
	reconcile()
	if err := env.Client.Get(ctx, key, &got); !apierrors.IsNotFound(err) {
		t.Errorf("the claim is still there: %v", err)
	}
	if ext.Obs.Exists {
		t.Error("the external object was not deleted")
	}
}
