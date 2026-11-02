package lifecycle_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

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

// Findings NO1b's review deferred to NO1c.

// A binding Secret whose type was edited is recreated once, not reported as
// drift and failed on every reconcile.
func TestBindingSecretOfAnotherTypeIsRecreated(t *testing.T) {
	odd := boundSecret()
	odd.Type = corev1.SecretTypeOpaque
	h := newHarness(t, nil, gadget(), odd)
	ext := existing()
	res, err := h.reconcileGadget(ext, h.credOpts())
	wantResult(t, res, err, poll)
	s := h.secret("g")
	if s.Type != "servicebinding.io/postgresql" || string(s.Data["password"]) != "OLD" {
		t.Errorf("secret = type %q data %v: the type is restored and the credentials are kept", s.Type, s.Data)
	}
	if got := ext.Calls(); !slices.Equal(got, []string{"Observe"}) {
		t.Errorf("calls = %v", got)
	}
	h.rec.AssertEvent(t, corev1.EventTypeNormal, conditions.EventBindingRepaired, "restored")
	wantCond(t, h.gadget("g").Status.Conditions, synced, metav1.ConditionTrue, conditions.ReconcileSuccess)

	// The next reconcile finds nothing to repair.
	h.rec.Reset()
	if _, err := h.reconcileGadget(ext, h.credOpts()); err != nil {
		t.Fatal(err)
	}
	h.rec.AssertNoEvent(t, conditions.EventBindingRepaired)
}

// An Observe-only claim never changes the backend, so a rotation left in its
// grace period is not retired either.
func TestObserveOnlyClaimDoesNotRetireAGeneration(t *testing.T) {
	left := common.RotationStatus{Active: "gen-1", Previous: "gen-0", RetireAfter: ts(now.Add(-time.Minute))}
	h := newHarness(t, nil, gadget(withPolicies("Observe"), withRotation(left), trigger("x")))
	rot := existingRot()
	res, err := h.reconcileGadget(rot, h.credOpts())
	wantResult(t, res, err, poll)
	if got := rot.Calls(); !slices.Equal(got, []string{"Observe"}) {
		t.Errorf("calls = %v: an Observe-only claim only observes", got)
	}
	rs := h.gadget("g").Status.Rotation
	if rs == nil || rs.Previous != "gen-0" || rs.RetireAfter == nil || rs.LastTrigger != "" {
		t.Errorf("status.rotation = %+v: nothing is retired or consumed", rs)
	}
	wantCond(t, h.gadget("g").Status.Conditions, synced, metav1.ConditionTrue, conditions.ReconcileSuccess)
}

// A Secret that goes missing while a grace period runs is reported, and the
// reconcile returns when the grace period ends, not at the next poll.
func TestMissingBindingSecretRequeuesAtTheEndOfTheGracePeriod(t *testing.T) {
	grace := common.RotationStatus{Active: "gen-1", Previous: "gen-0", Sequence: 1, RetireAfter: ts(now.Add(time.Minute))}
	h := newHarness(t, nil, gadget(withRotation(grace)))
	res, err := h.reconcileGadget(existingRot(), h.credOpts())
	wantResult(t, res, err, time.Minute)
	wantCond(t, h.gadget("g").Status.Conditions, synced, metav1.ConditionFalse, conditions.BindingSecretMissing)
}

// Once the grace period is over the previous generation is retired and the
// Secret is recreated by a rotation, in the same reconcile.
func TestMissingBindingSecretAfterTheGracePeriodRetiresThenRecreates(t *testing.T) {
	over := common.RotationStatus{Active: "gen-1", Previous: "gen-0", Sequence: 1, RetireAfter: ts(now.Add(-time.Minute))}
	h := newHarness(t, nil, gadget(withRotation(over)))
	rot := existingRot()
	rot.NextGeneration = "gen-2"
	res, err := h.reconcileGadget(rot, h.credOpts())
	if err != nil {
		t.Fatal(err)
	}
	if res.RequeueAfter <= 0 {
		t.Errorf("RequeueAfter = %v", res.RequeueAfter)
	}
	if got := rot.Calls(); !slices.Equal(got, []string{"Observe", "RotateFinish", "RotateBegin"}) {
		t.Errorf("calls = %v", got)
	}
	if got := rot.BeginStates(); len(got) != 1 || got[0] != (lifecycle.RotationState{Active: "gen-1", Sequence: 1}) {
		t.Errorf("RotateBegin state = %+v: the previous generation was retired first", got)
	}
	if string(h.secret("g").Data["password"]) != "NEW-PASSWORD" {
		t.Errorf("secret = %v", h.secret("g").Data)
	}
	if rs := h.gadget("g").Status.Rotation; rs.Active != "gen-2" || rs.Previous != "gen-1" || rs.Sequence != 2 {
		t.Errorf("status.rotation = %+v", rs)
	}
}

func TestMissingBindingSecretAfterTheGracePeriodFailsWhenTheFinishFails(t *testing.T) {
	over := common.RotationStatus{Active: "gen-1", Previous: "gen-0", RetireAfter: ts(now.Add(-time.Minute))}
	h := newHarness(t, nil, gadget(withRotation(over)))
	rot := existingRot()
	rot.FinishErr = errors.New("finish boom")
	if _, err := h.reconcileGadget(rot, h.credOpts()); !errors.Is(err, rot.FinishErr) {
		t.Fatalf("err = %v", err)
	}
	if len(rot.BeginStates()) != 0 {
		t.Error("a new generation was issued before the previous one was retired")
	}
}

// A blocked claim is audited when it becomes blocked, not on every poll.
func TestBlockedPollsAreAuditedOnStateChangeOnly(t *testing.T) {
	h := newHarness(t, nil, widget(withFinalizer, pinned(common.DeletionRetain, common.SourceOperatorDefault),
		func(w *testWidget) { w.Spec.ManagementPolicies = policies("Observe") }))
	ext := drifted()
	count := func() int {
		n := 0
		for _, l := range h.logLines() {
			if strings.Contains(l, `"msg":"audit"`) {
				n++
			}
		}
		return n
	}
	for range 3 {
		if _, err := h.reconcileWidget(ext, h.dataOpts()); err != nil {
			t.Fatal(err)
		}
	}
	if count() != 1 {
		t.Errorf("audit lines after 3 blocked polls = %d, want 1: %v", count(), h.logLines())
	}

	// It recovers, and the next time it is blocked is a new state.
	ext.Obs.UpToDate = true
	if _, err := h.reconcileWidget(ext, h.dataOpts()); err != nil {
		t.Fatal(err)
	}
	ext.Obs.UpToDate = false
	for range 2 {
		if _, err := h.reconcileWidget(ext, h.dataOpts()); err != nil {
			t.Fatal(err)
		}
	}
	if count() != 2 {
		t.Errorf("audit lines after recovering and blocking again = %d, want 2: %v", count(), h.logLines())
	}
}

// If the status write fails after a rotation, the next reconcile adopts the
// rotation the binding Secret already holds instead of issuing another.
func TestFailedStatusWriteAfterARotationDoesNotRotateAgain(t *testing.T) {
	var failing atomic.Bool
	boom := errors.New("status boom")
	h := newHarness(t, &interceptor.Funcs{SubResourceUpdate: func(ctx context.Context, c client.Client, sub string,
		o client.Object, opts ...client.SubResourceUpdateOption) error {
		if failing.Load() {
			return boom
		}
		return c.SubResource(sub).Update(ctx, o, opts...)
	}}, boundSecret(), gadget(trigger("x")))
	rot := existingRot()

	failing.Store(true)
	if _, err := h.reconcileGadget(rot, h.credOpts()); !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the status error", err)
	}
	if got := string(h.secret("g").Data["password"]); got != "NEW-PASSWORD" {
		t.Fatalf("password = %q: the rotation reached the Secret", got)
	}
	if h.gadget("g").Status.Rotation != nil {
		t.Fatal("the status was written")
	}

	failing.Store(false)
	if _, err := h.reconcileGadget(rot, h.credOpts()); err != nil {
		t.Fatal(err)
	}
	if n := len(rot.BeginStates()); n != 1 {
		t.Errorf("RotateBegin calls = %d, want 1: the rotation was issued again", n)
	}
	rs := h.gadget("g").Status.Rotation
	if rs == nil || rs.Active != "gen-1" || rs.Sequence != 1 || rs.LastTrigger != "x" || rs.Previous != "" {
		t.Errorf("status.rotation = %+v, want the adopted rotation recorded", rs)
	}
	wantCond(t, h.gadget("g").Status.Conditions, synced, metav1.ConditionTrue, conditions.Rotating)

	// Nothing is left to adopt: the third reconcile is quiet.
	if _, err := h.reconcileGadget(rot, h.credOpts()); err != nil {
		t.Fatal(err)
	}
	if n := len(rot.BeginStates()); n != 1 {
		t.Errorf("RotateBegin calls = %d after the status caught up", n)
	}
}

func TestRotationMarkAndAdoptionFailures(t *testing.T) {
	boom := errors.New("secret boom")
	t.Run("the Secret cannot be marked", func(t *testing.T) {
		h := newHarness(t, &interceptor.Funcs{Patch: func(ctx context.Context, c client.WithWatch, o client.Object,
			p client.Patch, opts ...client.PatchOption) error {
			if _, ok := o.(*corev1.Secret); ok {
				return boom
			}
			return c.Patch(ctx, o, p, opts...)
		}}, boundSecret(), gadget(trigger("x")))
		if err := h.rotationAt(existingRot(), now); !errors.Is(err, boom) {
			t.Fatalf("err = %v", err)
		}
		h.rec.AssertEvent(t, corev1.EventTypeWarning, conditions.EventRotationFailed, "marking")
		if h.gadget("g").Status.Rotation != nil {
			t.Error("a rotation was recorded though its Secret was not marked")
		}
	})
	t.Run("the Secret cannot be read before the rotation", func(t *testing.T) {
		var gets atomic.Int32
		h := newHarness(t, &interceptor.Funcs{Get: func(ctx context.Context, c client.WithWatch, k client.ObjectKey,
			o client.Object, opts ...client.GetOption) error {
			if _, ok := o.(*corev1.Secret); ok && gets.Add(1) == 2 { // the first read is Inspect's
				return boom
			}
			return c.Get(ctx, k, o, opts...)
		}}, boundSecret(), gadget(trigger("x")))
		rot := existingRot()
		if err := h.rotationAt(rot, now); !errors.Is(err, boom) {
			t.Fatalf("err = %v", err)
		}
		if len(rot.BeginStates()) != 0 {
			t.Error("the backend was asked to rotate although the Secret could not be read")
		}
	})
}

// A marker of another sequence is not a rotation to adopt.
func TestRotationMarkOfAnotherSequenceIsIgnored(t *testing.T) {
	stale := boundSecret()
	stale.Annotations = map[string]string{
		"craftainer.dev/rotation-sequence":   "7",
		"craftainer.dev/rotation-generation": "gen-7",
	}
	h := newHarness(t, nil, stale, gadget(trigger("x")))
	rot := existingRot()
	if err := h.rotationAt(rot, now); err != nil {
		t.Fatal(err)
	}
	if n := len(rot.BeginStates()); n != 1 {
		t.Errorf("RotateBegin calls = %d, want 1", n)
	}
	if rs := h.gadget("g").Status.Rotation; rs.Active != "gen-1" {
		t.Errorf("status.rotation = %+v", rs)
	}
	if got := h.secret("g").Annotations["craftainer.dev/rotation-sequence"]; got != "1" {
		t.Errorf("sequence mark = %q, want 1", got)
	}
}

// The binding Secret is read through the cache first; the API reader is only
// asked when the cache does not show it.
type failingReader struct{ calls atomic.Int32 }

func (f *failingReader) Get(context.Context, client.ObjectKey, client.Object, ...client.GetOption) error {
	f.calls.Add(1)
	return errors.New("the API reader must not be asked")
}

func (f *failingReader) List(context.Context, client.ObjectList, ...client.ListOption) error {
	return errors.New("unused")
}

func TestBindingSecretIsReadThroughTheCacheFirst(t *testing.T) {
	h := newHarness(t, nil, gadget(), boundSecret())
	opts := h.credOpts()
	reader := &failingReader{}
	opts.APIReader = reader
	res, err := h.reconcileGadget(existing(), opts)
	wantResult(t, res, err, poll)
	if n := reader.calls.Load(); n != 0 {
		t.Errorf("API reader calls = %d, want 0 for a Secret the cache shows", n)
	}
}

func TestMissingBindingSecretIsConfirmedThroughTheAPIReader(t *testing.T) {
	h := newHarness(t, nil, gadget())
	opts := h.credOpts()
	reader := &failingReader{}
	opts.APIReader = reader
	if _, err := h.reconcileGadget(existing(), opts); err == nil {
		t.Error("expected the API reader's error")
	}
	if n := reader.calls.Load(); n != 1 {
		t.Errorf("API reader calls = %d, want 1", n)
	}
}

// A deletion that keeps failing is retried with backoff for an hour; after
// that the finalizer stays, the errors stop, and one Warning names the way out.
func TestDeletionBlockedLongerThanTheWindow(t *testing.T) {
	boom := errors.New("backend boom")
	h := newHarness(t, nil, widget(terminating, pinned(common.DeletionDelete, common.SourceOperatorDefault)))
	ext := existing()
	ext.DeleteErr = boom
	at := func(d time.Duration) lifecycle.Options {
		o := h.dataOpts()
		o.Now = func() time.Time { return now.Add(d) }
		return o
	}
	blocked := func() int {
		n := 0
		for _, r := range h.rec.Reasons() {
			if r == conditions.DeletionBlocked {
				n++
			}
		}
		return n
	}

	if _, err := h.reconcileWidget(ext, at(59*time.Minute)); !errors.Is(err, boom) {
		t.Fatalf("inside the window: err = %v, want the backend error", err)
	}
	wantCond(t, h.widget("w").Status.Conditions, synced, metav1.ConditionFalse, conditions.ReconcileError)

	for range 3 {
		res, err := h.reconcileWidget(ext, at(2*time.Hour))
		wantResult(t, res, err, poll)
	}
	w := h.widget("w")
	if !hasFinalizer(w) {
		t.Error("the finalizer was dropped")
	}
	wantCond(t, w.Status.Conditions, synced, metav1.ConditionFalse, conditions.DeletionBlocked)
	wantCond(t, w.Status.Conditions, ready, metav1.ConditionFalse, conditions.DeletionBlocked)
	msg := cond(w.Status.Conditions, synced).Message
	if !strings.Contains(msg, "backend boom") || !strings.Contains(msg, "remove the finalizer "+lifecycle.DefaultFinalizer) {
		t.Errorf("message = %q", msg)
	}
	if blocked() != 1 {
		t.Errorf("DeletionBlocked events = %d, want 1", blocked())
	}

	// The backend recovers: the deletion completes by itself.
	ext.DeleteErr = nil
	if _, err := h.reconcileWidget(ext, at(3*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := h.c.Get(h.ctx, client.ObjectKey{Namespace: "ns", Name: "w"}, &testWidget{}); !apierrors.IsNotFound(err) {
		t.Errorf("claim still there: %v", err)
	}
}

func TestDeletionBlockedByAFinalResolveErrorAfterTheWindow(t *testing.T) {
	h := newHarness(t, nil, widget(terminating, pinned(common.DeletionDelete, common.SourceOperatorDefault)))
	opts := h.dataOpts()
	opts.Now = func() time.Time { return now.Add(2 * time.Hour) }
	opts.ResolveErr = fmt.Errorf("x: %w", server.ErrServerNotFound)
	res, err := h.reconcileWidget(nil, opts)
	wantResult(t, res, err, poll)
	w := h.widget("w")
	wantCond(t, w.Status.Conditions, synced, metav1.ConditionFalse, conditions.DeletionBlocked)
	if msg := cond(w.Status.Conditions, synced).Message; !strings.Contains(msg, "x: ") {
		t.Errorf("message = %q: it keeps the cause", msg)
	}
	if !hasFinalizer(w) {
		t.Error("the finalizer was dropped")
	}
}
