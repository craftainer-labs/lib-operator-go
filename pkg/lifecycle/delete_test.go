package lifecycle_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/craftainer-labs/lib-operator-go/pkg/apis/common"
	"github.com/craftainer-labs/lib-operator-go/pkg/conditions"
	"github.com/craftainer-labs/lib-operator-go/pkg/lifecycle"
	"github.com/craftainer-labs/lib-operator-go/pkg/server"
)

const (
	retained = conditions.EventRetained
	deleted  = conditions.EventDeleted
	orphaned = conditions.EventExternalObjectOrphaned
	repinned = conditions.EventDeletionPolicyRepinned
)

// ADR 0002 section 4: every row of the deletion outcome table. P is whether
// managementPolicies permits Delete.
func TestDeletionOutcomeTable(t *testing.T) {
	const data, credential = "data", "credential"
	spec := func(p common.DeletionPolicy) func(*testWidget) {
		return func(w *testWidget) { w.Spec.DeletionPolicy = p }
	}
	pol := func(p ...common.ManagementPolicy) func(*testWidget) {
		return func(w *testWidget) { w.Spec.ManagementPolicies = p }
	}
	both := func(fs ...func(*testWidget)) func(*testWidget) {
		return func(w *testWidget) {
			for _, f := range fs {
				f(w)
			}
		}
	}
	tests := []struct {
		name       string
		kind       string
		mut        func(*testWidget) // data kinds
		policies   []common.ManagementPolicy
		wantCalls  []string
		wantEvents []string
	}{
		// Row 1: credential, P, always deleted.
		{name: "credential, P: deleted", kind: credential, wantCalls: []string{"Observe", "Delete"}, wantEvents: []string{deleted}},
		{name: "credential, P through an explicit list: deleted", kind: credential, policies: policies("Observe", "Delete"),
			wantCalls: []string{"Observe", "Delete"}, wantEvents: []string{deleted}},
		// Row 2: data, P, effective Delete.
		{name: "data, P, pinned Delete: deleted", kind: data, mut: pinned(common.DeletionDelete, common.SourceOperatorDefault),
			wantCalls: []string{"Observe", "Delete"}, wantEvents: []string{deleted}},
		{name: "data, P, spec Delete wins over a pinned Retain", kind: data,
			mut:       both(pinned(common.DeletionRetain, common.SourceOperatorDefault), spec(common.DeletionDelete)),
			wantCalls: []string{"Observe", "Delete"}, wantEvents: []string{deleted}},
		{name: "data, P through an explicit list: deleted", kind: data, mut: both(pinned(common.DeletionDelete, common.SourceOperatorDefault), pol("Observe", "Create", "Delete")),
			wantCalls: []string{"Observe", "Delete"}, wantEvents: []string{deleted}},
		// Row 3: data, P, effective Retain.
		{name: "data, P, pinned Retain: left in place", kind: data, mut: pinned(common.DeletionRetain, common.SourceOperatorDefault),
			wantEvents: []string{retained}},
		{name: "data, P, spec Retain wins over a pinned Delete", kind: data,
			mut:        both(pinned(common.DeletionDelete, common.SourceOperatorDefault), spec(common.DeletionRetain)),
			wantEvents: []string{retained}},
		{name: "data, P, an unknown spec value is not Delete", kind: data, mut: spec("Maybe"), wantEvents: []string{retained}},
		// Row 4: data, status lost, P.
		{name: "data, status lost: Retain by fallback, with a Warning", kind: data,
			wantEvents: []string{repinned, retained}},
		{name: "data, status lost but spec Delete: the spec wins, no repin warning", kind: data, mut: spec(common.DeletionDelete),
			wantCalls: []string{"Observe", "Delete"}, wantEvents: []string{deleted}},
		{name: "data, status lost but spec Retain", kind: data, mut: spec(common.DeletionRetain), wantEvents: []string{retained}},
		// Row 5: not P, the list has Create or Update.
		{name: "data, not P, Create+Update: orphaned", kind: data,
			mut: pinned(common.DeletionDelete, common.SourceOperatorDefault), policies: policies("Observe", "Create", "Update"),
			wantEvents: []string{orphaned}},
		{name: "data, not P, Create only: orphaned", kind: data,
			mut: pinned(common.DeletionDelete, common.SourceOperatorDefault), policies: policies("Observe", "Create"),
			wantEvents: []string{orphaned}},
		{name: "data, not P, Update only: orphaned", kind: data,
			mut: pinned(common.DeletionDelete, common.SourceOperatorDefault), policies: policies("Observe", "Update"),
			wantEvents: []string{orphaned}},
		{name: "credential, not P, Create+Update: orphaned", kind: credential, policies: policies("Observe", "Create", "Update"),
			wantEvents: []string{orphaned}},
		{name: "data, status lost and not P: both Warnings, in order", kind: data, policies: policies("Observe", "Create", "Update"),
			wantEvents: []string{repinned, orphaned}},
		// Row 6: not P, exactly ["Observe"].
		{name: "data, Observe only: never ours, Retained", kind: data, mut: pinned(common.DeletionDelete, common.SourceOperatorDefault),
			policies: policies("Observe"), wantEvents: []string{retained}},
		{name: "credential, Observe only: Retained", kind: credential, policies: policies("Observe"), wantEvents: []string{retained}},
		{name: "Observe+LateInitialize is not ours either", kind: credential, policies: policies("Observe", "LateInitialize"),
			wantEvents: []string{retained}},
		// A stored list the CEL rules would reject fails closed.
		{name: "credential, invalid list: nothing is deleted", kind: credential, policies: policies("Create"), wantEvents: []string{retained}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var h *harness
			var res func() (resultErr error)
			ext := existing()
			if tt.kind == credential {
				g := gadget(terminatingGadget, func(g *testGadget) { g.Spec.ManagementPolicies = tt.policies; g.Status.ExternalName = "ext-name" })
				h = newHarness(t, nil, g)
				res = func() error { _, err := h.reconcileGadget(ext, h.credOpts()); return err }
			} else {
				h = newHarness(t, nil, widget(terminating, func(w *testWidget) { w.Spec.ManagementPolicies = tt.policies }, mutOrNop(tt.mut)))
				res = func() error { _, err := h.reconcileWidget(ext, h.dataOpts()); return err }
			}
			if err := res(); err != nil {
				t.Fatal(err)
			}
			if got := ext.Calls(); !slices.Equal(got, tt.wantCalls) {
				t.Errorf("calls = %v, want %v", got, tt.wantCalls)
			}
			h.rec.AssertReasons(t, tt.wantEvents...)
			for _, e := range h.rec.Events() {
				wantType := corev1.EventTypeNormal
				if e.Reason == repinned || e.Reason == orphaned {
					wantType = corev1.EventTypeWarning
				}
				if e.Type != wantType {
					t.Errorf("event %s has type %s, want %s", e.Reason, e.Type, wantType)
				}
			}
			// The finalizer is gone, so the object is deleted.
			var err error
			if tt.kind == credential {
				err = h.c.Get(h.ctx, client.ObjectKey{Namespace: "ns", Name: "g"}, &testGadget{})
			} else {
				err = h.c.Get(h.ctx, client.ObjectKey{Namespace: "ns", Name: "w"}, &testWidget{})
			}
			if !apierrors.IsNotFound(err) {
				t.Errorf("the claim is still there: %v", err)
			}
		})
	}
}

func mutOrNop(f func(*testWidget)) func(*testWidget) {
	if f == nil {
		return func(*testWidget) {}
	}
	return f
}

func TestOrphanWarningNamesTheObjectAndTheValidCredential(t *testing.T) {
	for kind, wantCredential := range map[string]bool{"credential": true, "data": false} {
		t.Run(kind, func(t *testing.T) {
			var h *harness
			if wantCredential {
				h = newHarness(t, nil, gadget(terminatingGadget, func(g *testGadget) {
					g.Spec.ManagementPolicies = policies("Observe", "Create", "Update")
					g.Status.ExternalName = "team-a_orders_gdeqxvirep"
				}))
				if _, err := h.reconcileGadget(existing(), h.credOpts()); err != nil {
					t.Fatal(err)
				}
			} else {
				h = newHarness(t, nil, widget(terminating, func(w *testWidget) {
					w.Spec.ManagementPolicies = policies("Observe", "Create", "Update")
					w.Status.ExternalName = "team-a_orders_gdeqxvirep"
				}))
				if _, err := h.reconcileWidget(existing(), h.dataOpts()); err != nil {
					t.Fatal(err)
				}
			}
			h.rec.AssertEvent(t, corev1.EventTypeWarning, orphaned, "team-a_orders_gdeqxvirep")
			var note string
			for _, e := range h.rec.Events() {
				note = e.Note
			}
			if got := strings.Contains(note, "credential is still valid"); got != wantCredential {
				t.Errorf("note = %q, mentions the valid credential: %v, want %v", note, got, wantCredential)
			}
		})
	}
}

func TestPausedClaimIsStillDeleted(t *testing.T) {
	h := newHarness(t, nil, widget(terminating, pinned(common.DeletionDelete, common.SourceOperatorDefault),
		func(w *testWidget) { w.Annotations = map[string]string{lifecycle.AnnotationPaused: "true"} }))
	ext := existing()
	if _, err := h.reconcileWidget(ext, h.dataOpts()); err != nil {
		t.Fatal(err)
	}
	if got := ext.Calls(); !slices.Equal(got, []string{"Observe", "Delete"}) {
		t.Errorf("calls = %v", got)
	}
}

func TestDeletionWithNothingExternalToDelete(t *testing.T) {
	h := newHarness(t, nil, widget(terminating, pinned(common.DeletionDelete, common.SourceOperatorDefault)))
	ext := missing()
	if _, err := h.reconcileWidget(ext, h.dataOpts()); err != nil {
		t.Fatal(err)
	}
	if got := ext.Calls(); !slices.Equal(got, []string{"Observe"}) {
		t.Errorf("calls = %v", got)
	}
	h.rec.AssertReasons(t) // nothing was deleted, so no Deleted event
	if err := h.c.Get(h.ctx, client.ObjectKey{Namespace: "ns", Name: "w"}, &testWidget{}); !apierrors.IsNotFound(err) {
		t.Errorf("the finalizer was not released: %v", err)
	}
}

func TestDeletionBlocksOnTheFinalizer(t *testing.T) {
	boom := errors.New("backend said no")
	tests := []struct {
		name    string
		setup   func(*fakeExt)
		wantErr bool
		reason  string
		reach   string
	}{
		{"unreachable server", func(f *fakeExt) { f.ObserveErr = fmt.Errorf("dial: %w", lifecycle.ErrUnreachable) }, true, conditions.ServerUnreachable, "False"},
		{"observe fails", func(f *fakeExt) { f.ObserveErr = boom }, true, conditions.ReconcileError, ""},
		{"delete fails", func(f *fakeExt) { f.DeleteErr = boom }, true, conditions.ReconcileError, ""},
		{"delete would take another claim's object", func(f *fakeExt) { f.ObserveErr = fmt.Errorf("owned by uid-x: %w", lifecycle.ErrNameCollision) }, false, conditions.NameCollision, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t, nil, widget(terminating, pinned(common.DeletionDelete, common.SourceOperatorDefault)))
			ext := existing()
			tt.setup(ext)
			res, err := h.reconcileWidget(ext, h.dataOpts())
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if !tt.wantErr && res.RequeueAfter != poll {
				t.Errorf("requeue = %v", res.RequeueAfter)
			}
			w := h.widget("w") // still there
			if !hasFinalizer(w) {
				t.Error("the finalizer was dropped")
			}
			wantCond(t, w.Status.Conditions, synced, metav1.ConditionFalse, tt.reason)
			if tt.reach != "" {
				wantCond(t, w.Status.Conditions, reach, metav1.ConditionFalse, tt.reason)
			}
			h.rec.AssertEvent(t, corev1.EventTypeWarning, tt.reason, "")
			if slices.Contains(h.rec.Reasons(), deleted) {
				t.Error("a Deleted event for an object that was not deleted")
			}
		})
	}
}

func TestDeletionWhenTheServerCannotBeResolved(t *testing.T) {
	t.Run("final errors requeue without an error", func(t *testing.T) {
		h := newHarness(t, nil, widget(terminating, pinned(common.DeletionDelete, common.SourceOperatorDefault)))
		opts := h.dataOpts()
		opts.ResolveErr = fmt.Errorf("x: %w", server.ErrServerNotFound)
		res, err := h.reconcileWidget(nil, opts)
		wantResult(t, res, err, poll)
		w := h.widget("w")
		if !hasFinalizer(w) {
			t.Error("the finalizer was dropped")
		}
		wantCond(t, w.Status.Conditions, synced, metav1.ConditionFalse, conditions.ServerNotFound)
	})
	t.Run("other errors back off", func(t *testing.T) {
		h := newHarness(t, nil, widget(terminating, pinned(common.DeletionDelete, common.SourceOperatorDefault)))
		opts := h.dataOpts()
		opts.ResolveErr = errors.New("timeout")
		if _, err := h.reconcileWidget(nil, opts); err == nil {
			t.Error("expected an error")
		}
		wantCond(t, h.widget("w").Status.Conditions, synced, metav1.ConditionFalse, conditions.ServerUnreachable)
	})
	t.Run("a Retain needs no server", func(t *testing.T) {
		h := newHarness(t, nil, widget(terminating, pinned(common.DeletionRetain, common.SourceOperatorDefault)))
		opts := h.dataOpts()
		opts.ResolveErr = errors.New("timeout")
		if _, err := h.reconcileWidget(nil, opts); err != nil {
			t.Fatal(err)
		}
		h.rec.AssertReasons(t, retained)
		if err := h.c.Get(h.ctx, client.ObjectKey{Namespace: "ns", Name: "w"}, &testWidget{}); !apierrors.IsNotFound(err) {
			t.Errorf("claim still there: %v", err)
		}
	})
	t.Run("no External and no resolve error is a configuration error", func(t *testing.T) {
		h := newHarness(t, nil, widget(terminating, pinned(common.DeletionDelete, common.SourceOperatorDefault)))
		if _, err := h.reconcileWidget(nil, h.dataOpts()); !errors.Is(err, lifecycle.ErrInvalidOptions) {
			t.Errorf("err = %v", err)
		}
		if !hasFinalizer(h.widget("w")) {
			t.Error("the finalizer was dropped")
		}
	})
}

func TestOnOrphan(t *testing.T) {
	orphaning := func(w *testWidget) { w.Spec.ManagementPolicies = policies("Observe", "Create", "Update") }
	t.Run("runs when the object is orphaned", func(t *testing.T) {
		h := newHarness(t, nil, widget(terminating, orphaning))
		var got string
		opts := h.dataOpts()
		opts.OnOrphan = func(_ context.Context, c lifecycle.Claim) error { got = c.GetName(); return nil }
		if _, err := h.reconcileWidget(existing(), opts); err != nil {
			t.Fatal(err)
		}
		if got != "w" {
			t.Errorf("OnOrphan saw %q", got)
		}
	})
	t.Run("an error keeps the finalizer", func(t *testing.T) {
		h := newHarness(t, nil, widget(terminating, orphaning))
		opts := h.dataOpts()
		opts.OnOrphan = func(context.Context, lifecycle.Claim) error { return errors.New("relabel failed") }
		if _, err := h.reconcileWidget(existing(), opts); err == nil {
			t.Error("expected an error")
		}
		if !hasFinalizer(h.widget("w")) {
			t.Error("the finalizer was dropped")
		}
	})
	t.Run("does not run for an object that was never ours", func(t *testing.T) {
		h := newHarness(t, nil, widget(terminating, func(w *testWidget) { w.Spec.ManagementPolicies = policies("Observe") }))
		opts := h.dataOpts()
		opts.OnOrphan = func(context.Context, lifecycle.Claim) error { t.Error("OnOrphan ran"); return nil }
		if _, err := h.reconcileWidget(existing(), opts); err != nil {
			t.Fatal(err)
		}
	})
}

func TestDeletionOfAClaimWithoutOurFinalizerIsLeftAlone(t *testing.T) {
	h := newHarness(t, nil)
	ts := metav1.NewTime(now)
	w := widget(func(w *testWidget) { w.DeletionTimestamp = &ts; w.Finalizers = []string{"someone.else/finalizer"} })
	ext := existing()
	_, _, _, _ = h.audit.Observe("uid-w", spec{})
	res, err := lifecycle.Reconcile(h.ctx, h.c, w, spec{}, lifecycle.External[spec, obj](ext), h.dataOpts())
	wantResult(t, res, err, 0)
	if len(ext.Calls()) != 0 || len(h.rec.Events()) != 0 {
		t.Errorf("touched: %v %v", ext.Calls(), h.rec.Reasons())
	}
	if _, old, _, _ := h.audit.Observe("uid-w", spec{}); old != "" {
		t.Error("the audit tracker still holds a claim that goes without release()")
	}
}

func TestFinalizerRemovalToleratesAClaimThatIsAlreadyGone(t *testing.T) {
	h := newHarness(t, nil) // the claim is not in the API server
	ts := metav1.NewTime(now)
	w := widget(func(w *testWidget) {
		w.DeletionTimestamp, w.Finalizers = &ts, []string{lifecycle.DefaultFinalizer}
		w.Spec.ManagementPolicies = policies("Observe")
	})
	_, _, _, _ = h.audit.Observe("uid-w", spec{})
	res, err := lifecycle.Reconcile(h.ctx, h.c, w, spec{}, lifecycle.External[spec, obj](existing()), h.dataOpts())
	wantResult(t, res, err, 0)
	if _, old, _, _ := h.audit.Observe("uid-w", spec{}); old != "" {
		t.Error("the audit tracker still holds a claim that was already gone")
	}
}

func TestFinalizerRemovalFailureIsReturned(t *testing.T) {
	boom := errors.New("patch boom")
	h := newHarness(t, &interceptor.Funcs{Patch: func(context.Context, client.WithWatch, client.Object, client.Patch, ...client.PatchOption) error {
		return boom
	}}, widget(terminating, pinned(common.DeletionRetain, common.SourceOperatorDefault)))
	if _, err := h.reconcileWidget(existing(), h.dataOpts()); !errors.Is(err, boom) {
		t.Errorf("err = %v", err)
	}
	if !hasFinalizer(h.widget("w")) {
		t.Error("finalizer gone")
	}
}

func TestDeletionIsAudited(t *testing.T) {
	h := newHarness(t, nil, widget(terminating, pinned(common.DeletionDelete, common.SourceOperatorDefault)))
	if _, err := h.reconcileWidget(existing(), h.dataOpts()); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, l := range h.logLines() {
		found = found || (strings.Contains(l, `"msg":"audit"`) && strings.Contains(l, `"action":"Delete"`) && strings.Contains(l, `"result":"success"`))
	}
	if !found {
		t.Errorf("no audit line for the deletion: %v", h.logLines())
	}
}
