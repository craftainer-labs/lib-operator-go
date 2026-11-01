package conditions_test

import (
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/craftainer-labs/lib-operator-go/pkg/conditions"
)

func TestSetRecordsGenerationAndTransition(t *testing.T) {
	var conds []metav1.Condition
	if !conditions.Set(&conds, 3, conditions.Ready, metav1.ConditionTrue, conditions.Available, "ok") {
		t.Fatal("first Set reported no change")
	}
	got := meta.FindStatusCondition(conds, conditions.Ready)
	if got.ObservedGeneration != 3 || got.Reason != conditions.Available || got.Message != "ok" || got.LastTransitionTime.IsZero() {
		t.Errorf("condition = %+v", got)
	}
	if conditions.Set(&conds, 3, conditions.Ready, metav1.ConditionTrue, conditions.Available, "ok") {
		t.Error("an identical Set reported a change")
	}
}

func TestSetCutsLongMessages(t *testing.T) {
	var conds []metav1.Condition
	conditions.Set(&conds, 1, conditions.Synced, metav1.ConditionFalse, conditions.ReconcileError, strings.Repeat("x", 5000))
	if got := len(conds[0].Message); got != conditions.MaxMessageLength {
		t.Errorf("message length = %d, want %d", got, conditions.MaxMessageLength)
	}
}

func TestEnsureSetsAllThreeOnce(t *testing.T) {
	var conds []metav1.Condition
	conditions.Set(&conds, 1, conditions.Ready, metav1.ConditionTrue, conditions.Available, "kept")
	conditions.Ensure(&conds, 2)
	if len(conds) != 3 {
		t.Fatalf("conditions = %+v, want 3", conds)
	}
	for _, typ := range []string{conditions.Ready, conditions.Synced, conditions.ServerReachable} {
		if meta.FindStatusCondition(conds, typ) == nil {
			t.Errorf("%s missing", typ)
		}
	}
	if ready := meta.FindStatusCondition(conds, conditions.Ready); ready.Status != metav1.ConditionTrue || ready.Message != "kept" {
		t.Errorf("Ensure changed an existing condition: %+v", ready)
	}
	synced := meta.FindStatusCondition(conds, conditions.Synced)
	if synced.Status != metav1.ConditionUnknown || synced.Reason != conditions.Reconciling || synced.ObservedGeneration != 2 {
		t.Errorf("new condition = %+v", synced)
	}
}
