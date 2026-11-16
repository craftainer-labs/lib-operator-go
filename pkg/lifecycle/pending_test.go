package lifecycle_test

import (
	"errors"
	"fmt"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/craftainer-labs/lib-operator-go/pkg/apis/common"
	"github.com/craftainer-labs/lib-operator-go/pkg/conditions"
	"github.com/craftainer-labs/lib-operator-go/pkg/lifecycle"
)

func TestPendingError(t *testing.T) {
	err := lifecycle.Pending(5*time.Second, conditions.Creating, "the user is creating")
	var p *lifecycle.PendingError
	if !errors.As(fmt.Errorf("wrapped: %w", err), &p) || p.After != 5*time.Second || p.Reason != conditions.Creating {
		t.Fatalf("Pending = %#v", p)
	}
	if got := err.Error(); got != "the user is creating" {
		t.Errorf("Error() = %q", got)
	}
	if got := lifecycle.Pending(0, "", "").Error(); got != "waiting for the backend to finish" {
		t.Errorf("empty Error() = %q", got)
	}
}

// A PendingError requeues sooner than the 30 s poll minimum, with no error,
// from every External method.
func TestPendingRequeuesWithoutAnError(t *testing.T) {
	tests := []struct {
		name   string
		setup  func(*fakeExt)
		fresh  bool // the External starts with an existing, up to date object
		delete bool
		want   time.Duration
		reason string
	}{
		{"observe", func(f *fakeExt) { f.ObserveErr = lifecycle.Pending(10*time.Second, conditions.Updating, "modifying") }, false, false, 10 * time.Second, conditions.Updating},
		{"create wrapped", func(f *fakeExt) {
			f.CreateErr = fmt.Errorf("x: %w", lifecycle.Pending(3*time.Second, conditions.Creating, "creating"))
		}, false, false, 3 * time.Second, conditions.Creating},
		{"update", func(f *fakeExt) {
			f.Obs.UpToDate = false
			f.UpdateErr = lifecycle.Pending(7*time.Second, conditions.Updating, "modifying")
		}, true, false, 7 * time.Second, conditions.Updating},
		{"delete", func(f *fakeExt) { f.DeleteErr = lifecycle.Pending(10*time.Second, conditions.Deleting, "deleting") }, true, true, 10 * time.Second, conditions.Deleting},
		{"too short", func(f *fakeExt) { f.CreateErr = lifecycle.Pending(-time.Second, "", "") }, false, false, lifecycle.MinPendingInterval, conditions.Reconciling},
		{"too long", func(f *fakeExt) { f.CreateErr = lifecycle.Pending(24*time.Hour, conditions.Creating, "creating") }, false, false, poll, conditions.Creating},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var w *testWidget
			if tt.delete {
				w = widget(terminating, pinned(common.DeletionDelete, common.SourceOperatorDefault))
			} else {
				w = widget(withFinalizer, pinned(common.DeletionRetain, common.SourceOperatorDefault))
			}
			h := newHarness(t, nil, w)
			ext := missing()
			if tt.fresh {
				ext = existing()
			}
			tt.setup(ext)
			res, err := h.reconcileWidget(ext, h.dataOpts())
			wantResult(t, res, err, tt.want)
			got := h.widget("w")
			wantCond(t, got.Status.Conditions, synced, metav1.ConditionTrue, tt.reason)
			wantCond(t, got.Status.Conditions, ready, metav1.ConditionFalse, tt.reason)
			if tt.delete && !hasFinalizer(got) {
				t.Error("a pending deletion must keep the finalizer")
			}
			h.rec.AssertNoEvent(t, conditions.ReconcileError)
			for _, e := range h.rec.Events() {
				if e.Type == corev1.EventTypeWarning {
					t.Errorf("unexpected Warning Event %+v", e)
				}
			}
		})
	}
}
