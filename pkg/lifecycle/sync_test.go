package lifecycle_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/craftainer-labs/lib-operator-go/pkg/apis/common"
	"github.com/craftainer-labs/lib-operator-go/pkg/audit"
	"github.com/craftainer-labs/lib-operator-go/pkg/binding"
	"github.com/craftainer-labs/lib-operator-go/pkg/conditions"
	"github.com/craftainer-labs/lib-operator-go/pkg/lifecycle"
	"github.com/craftainer-labs/lib-operator-go/pkg/lifecycle/lifecycletest"
	"github.com/craftainer-labs/lib-operator-go/pkg/server"
)

func TestFirstReconcileOfADataClaim(t *testing.T) {
	h := newHarness(t, nil, widget())
	ext := missing()

	res, err := h.reconcileWidget(ext, h.dataOpts())
	wantResult(t, res, err, poll)

	w := h.widget("w")
	if !hasFinalizer(w) {
		t.Error("finalizer not added")
	}
	if w.Status.EffectiveDeletionPolicy != common.DeletionRetain || w.Status.DeletionPolicySource != common.SourceOperatorDefault {
		t.Errorf("pin = %q/%q, want Retain/OperatorDefault", w.Status.EffectiveDeletionPolicy, w.Status.DeletionPolicySource)
	}
	wantCond(t, w.Status.Conditions, ready, metav1.ConditionTrue, conditions.Available)
	wantCond(t, w.Status.Conditions, synced, metav1.ConditionTrue, conditions.ReconcileSuccess)
	wantCond(t, w.Status.Conditions, reach, metav1.ConditionTrue, conditions.Available)
	for _, c := range w.Status.Conditions {
		if c.ObservedGeneration != 1 || c.LastTransitionTime.IsZero() {
			t.Errorf("condition %s lacks observedGeneration or lastTransitionTime: %+v", c.Type, c)
		}
	}
	if w.Status.ObservedGeneration != 1 || w.Status.LastReconcileTime == nil || !w.Status.LastReconcileTime.Time.Equal(now) {
		t.Errorf("status = %+v", w.Status)
	}
	if got := ext.Calls(); !slices.Equal(got, []string{"Observe", "Create"}) {
		t.Errorf("calls = %v", got)
	}
	h.rec.AssertReasons(t, conditions.EventDeletionPolicyResolved, conditions.EventCreated)
	h.rec.AssertEvent(t, corev1.EventTypeNormal, conditions.EventDeletionPolicyResolved, "Retain")
}

func TestDefaultDeletionPolicyIsPinnedFromTheOptions(t *testing.T) {
	h := newHarness(t, nil, widget())
	opts := h.dataOpts()
	opts.DefaultDeletionPolicy = common.DeletionDelete
	if _, err := h.reconcileWidget(existing(), opts); err != nil {
		t.Fatal(err)
	}
	if w := h.widget("w"); w.Status.EffectiveDeletionPolicy != common.DeletionDelete || w.Status.DeletionPolicySource != common.SourceOperatorDefault {
		t.Errorf("pin = %q/%q", w.Status.EffectiveDeletionPolicy, w.Status.DeletionPolicySource)
	}
}

// ADR 0002 section 4: the status write comes first, then the finalizer patch.
func TestPinIsWrittenBeforeTheFinalizer(t *testing.T) {
	var order []string
	h := newHarness(t, &interceptor.Funcs{
		SubResourceUpdate: func(ctx context.Context, c client.Client, sub string, o client.Object, opts ...client.SubResourceUpdateOption) error {
			order = append(order, "status")
			return c.SubResource(sub).Update(ctx, o, opts...)
		},
		Patch: func(ctx context.Context, c client.WithWatch, o client.Object, p client.Patch, opts ...client.PatchOption) error {
			order = append(order, "patch")
			return c.Patch(ctx, o, p, opts...)
		},
	}, widget())
	if _, err := h.reconcileWidget(existing(), h.dataOpts()); err != nil {
		t.Fatal(err)
	}
	if len(order) < 2 || order[0] != "status" || order[1] != "patch" {
		t.Errorf("write order = %v, want status then patch", order)
	}
}

func TestPinStatesOfADataClaim(t *testing.T) {
	tests := []struct {
		name        string
		claim       *testWidget
		def         common.DeletionPolicy
		wantPolicy  common.DeletionPolicy
		wantSource  common.DeletionPolicySource
		wantReasons []string
	}{
		{"a pin without a finalizer (crash between the writes) is kept and the finalizer added",
			widget(pinned(common.DeletionDelete, common.SourceOperatorDefault)), common.DeletionRetain,
			common.DeletionDelete, common.SourceOperatorDefault, []string{conditions.EventObserved}},
		{"a recorded pin is never rewritten when the default changes",
			widget(withFinalizer, pinned(common.DeletionDelete, common.SourceOperatorDefault)), common.DeletionRetain,
			common.DeletionDelete, common.SourceOperatorDefault, []string{conditions.EventObserved}},
		{"a fallback pin stays a fallback",
			widget(withFinalizer, pinned(common.DeletionRetain, common.SourceFallback)), common.DeletionDelete,
			common.DeletionRetain, common.SourceFallback, []string{conditions.EventObserved}},
		{"lost status: finalizer present, no pin, pinned Retain whatever the default says",
			widget(withFinalizer), common.DeletionDelete,
			common.DeletionRetain, common.SourceFallback, []string{conditions.EventDeletionPolicyRepinned, conditions.EventObserved}},
		{"lost status with spec.deletionPolicy set is repinned too",
			widget(withFinalizer, func(w *testWidget) { w.Spec.DeletionPolicy = common.DeletionDelete }), common.DeletionDelete,
			common.DeletionRetain, common.SourceFallback, []string{conditions.EventDeletionPolicyRepinned, conditions.EventObserved}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t, nil, tt.claim)
			opts := h.dataOpts()
			opts.DefaultDeletionPolicy = tt.def
			if _, err := h.reconcileWidget(existing(), opts); err != nil {
				t.Fatal(err)
			}
			w := h.widget("w")
			if w.Status.EffectiveDeletionPolicy != tt.wantPolicy || w.Status.DeletionPolicySource != tt.wantSource {
				t.Errorf("pin = %q/%q, want %q/%q", w.Status.EffectiveDeletionPolicy, w.Status.DeletionPolicySource, tt.wantPolicy, tt.wantSource)
			}
			if !hasFinalizer(w) {
				t.Error("finalizer missing")
			}
			h.rec.AssertReasons(t, tt.wantReasons...)
		})
	}
}

func TestPinFailuresStopTheReconcile(t *testing.T) {
	boom := errors.New("boom")
	t.Run("status write", func(t *testing.T) {
		h := newHarness(t, &interceptor.Funcs{SubResourceUpdate: func(context.Context, client.Client, string, client.Object, ...client.SubResourceUpdateOption) error {
			return boom
		}}, widget())
		ext := missing()
		if _, err := h.reconcileWidget(ext, h.dataOpts()); !errors.Is(err, boom) {
			t.Errorf("err = %v", err)
		}
		if hasFinalizer(h.widget("w")) || len(ext.Calls()) != 0 {
			t.Error("the finalizer was added or the backend touched after a failed pin")
		}
	})
	t.Run("finalizer patch", func(t *testing.T) {
		h := newHarness(t, &interceptor.Funcs{Patch: func(context.Context, client.WithWatch, client.Object, client.Patch, ...client.PatchOption) error {
			return boom
		}}, widget())
		ext := missing()
		if _, err := h.reconcileWidget(ext, h.dataOpts()); !errors.Is(err, boom) || len(ext.Calls()) != 0 {
			t.Errorf("err = %v, calls = %v", err, ext.Calls())
		}
	})
}

func TestPausedClaimIsLeftAlone(t *testing.T) {
	h := newHarness(t, nil, widget(func(w *testWidget) { w.Annotations = map[string]string{lifecycle.AnnotationPaused: "true"} }))
	ext := missing()
	res, err := h.reconcileWidget(ext, h.dataOpts())
	wantResult(t, res, err, poll)
	if len(ext.Calls()) != 0 || hasFinalizer(h.widget("w")) || len(h.rec.Events()) != 0 {
		t.Errorf("paused claim was touched: calls %v, events %v", ext.Calls(), h.rec.Reasons())
	}
}

func TestOnlyTheStringTrueMeansPaused(t *testing.T) {
	h := newHarness(t, nil, widget(func(w *testWidget) { w.Annotations = map[string]string{lifecycle.AnnotationPaused: "false"} }))
	ext := missing()
	if _, err := h.reconcileWidget(ext, h.dataOpts()); err != nil || len(ext.Calls()) == 0 {
		t.Errorf("err = %v, calls = %v", err, ext.Calls())
	}
}

func TestPollInterval(t *testing.T) {
	tests := []struct {
		annotation string
		set        bool
		want       time.Duration
		warns      bool
	}{
		{"", false, poll, false},
		{"30s", true, 30 * time.Second, false},
		{"2h", true, 2 * time.Hour, false},
		{"24h", true, 24 * time.Hour, false},
		{"29s", true, poll, true},
		{"25h", true, poll, true},
		{"soon", true, poll, true},
		{"", true, poll, true},
	}
	for _, tt := range tests {
		t.Run(tt.annotation, func(t *testing.T) {
			h := newHarness(t, nil, widget(func(w *testWidget) {
				w.Annotations = map[string]string{lifecycle.AnnotationPaused: "true"}
				if tt.set {
					w.Annotations[lifecycle.AnnotationPollInterval] = tt.annotation
				}
			}))
			res, err := h.reconcileWidget(nil, h.dataOpts())
			wantResult(t, res, err, tt.want)
			if got := slices.Contains(h.rec.Reasons(), conditions.EventInvalidPollInterval); got != tt.warns {
				t.Errorf("InvalidPollInterval event = %v, want %v", got, tt.warns)
			}
		})
	}
}

func TestDefaultPollIntervalAndJitter(t *testing.T) {
	paused := widget(func(w *testWidget) { w.Annotations = map[string]string{lifecycle.AnnotationPaused: "true"} })
	h := newHarness(t, nil, paused)
	opts := h.dataOpts()
	opts.DefaultPollInterval = time.Hour
	opts.Jitter = func() float64 { return 0.5 }
	res, err := h.reconcileWidget(nil, opts)
	wantResult(t, res, err, time.Hour+3*time.Minute) // up to 10% jitter: 0.5 * 10% of 1h

	// The default clock and jitter source stay within [poll, 1.1 * poll).
	opts = h.dataOpts()
	opts.Now, opts.Jitter = nil, nil
	for range 50 {
		res, err = h.reconcileWidget(nil, opts)
		if err != nil || res.RequeueAfter < poll || res.RequeueAfter >= poll+poll/10 {
			t.Fatalf("result = %+v, %v", res, err)
		}
	}
}

// ADR 0002 section 5: the policy rows.
func TestManagementPolicyRows(t *testing.T) {
	const (
		missingState = "missing"
		driftedState = "drifted"
		okState      = "ok"
	)
	states := map[string]func() *fakeExt{missingState: missing, driftedState: drifted, okState: existing}
	tests := []struct {
		name      string
		policies  []common.ManagementPolicy
		state     string
		wantCalls []string
		synced    metav1.ConditionStatus
		reason    string
		ready     metav1.ConditionStatus
		events    []string
	}{
		{"default creates", nil, missingState, []string{"Observe", "Create"}, metav1.ConditionTrue, conditions.ReconcileSuccess, metav1.ConditionTrue, []string{"Created"}},
		{"star creates", policies("*"), missingState, []string{"Observe", "Create"}, metav1.ConditionTrue, conditions.ReconcileSuccess, metav1.ConditionTrue, []string{"Created"}},
		{"star updates", policies("*"), driftedState, []string{"Observe", "Update"}, metav1.ConditionTrue, conditions.ReconcileSuccess, metav1.ConditionTrue, []string{"Updated"}},
		{"star observes", policies("*"), okState, []string{"Observe"}, metav1.ConditionTrue, conditions.ReconcileSuccess, metav1.ConditionTrue, []string{"Observed"}},

		{"Observe: missing is ObserveOnly and quiet", policies("Observe"), missingState, []string{"Observe"}, metav1.ConditionFalse, conditions.ObserveOnly, metav1.ConditionFalse, nil},
		{"Observe: drift is reported, not fixed", policies("Observe"), driftedState, []string{"Observe"}, metav1.ConditionFalse, conditions.ObserveOnly, metav1.ConditionFalse, nil},
		{"Observe: in sync", policies("Observe"), okState, []string{"Observe"}, metav1.ConditionTrue, conditions.ReconcileSuccess, metav1.ConditionTrue, []string{"Observed"}},

		{"Observe+Update: missing is blocked", policies("Observe", "Update"), missingState, []string{"Observe"}, metav1.ConditionFalse, conditions.ManagementPolicyBlocked, metav1.ConditionFalse, []string{"ManagementPolicyBlocked"}},
		{"Observe+Update: drift is fixed", policies("Update", "Observe"), driftedState, []string{"Observe", "Update"}, metav1.ConditionTrue, conditions.ReconcileSuccess, metav1.ConditionTrue, []string{"Updated"}},
		{"Observe+Create: missing is created", policies("Observe", "Create"), missingState, []string{"Observe", "Create"}, metav1.ConditionTrue, conditions.ReconcileSuccess, metav1.ConditionTrue, []string{"Created"}},
		{"Observe+Create: drift is blocked", policies("Observe", "Create"), driftedState, []string{"Observe"}, metav1.ConditionFalse, conditions.ManagementPolicyBlocked, metav1.ConditionFalse, []string{"ManagementPolicyBlocked"}},
		{"never change after creation: drift is blocked", policies("Observe", "Create", "Delete"), driftedState, []string{"Observe"}, metav1.ConditionFalse, conditions.ManagementPolicyBlocked, metav1.ConditionFalse, []string{"ManagementPolicyBlocked"}},
		{"never delete: missing is created", policies("Observe", "Create", "Update"), missingState, []string{"Observe", "Create"}, metav1.ConditionTrue, conditions.ReconcileSuccess, metav1.ConditionTrue, []string{"Created"}},
		{"never delete: drift is updated", policies("Observe", "Create", "Update"), driftedState, []string{"Observe", "Update"}, metav1.ConditionTrue, conditions.ReconcileSuccess, metav1.ConditionTrue, []string{"Updated"}},
		{"Observe+Delete: missing is blocked", policies("Observe", "Delete"), missingState, []string{"Observe"}, metav1.ConditionFalse, conditions.ManagementPolicyBlocked, metav1.ConditionFalse, []string{"ManagementPolicyBlocked"}},
		{"Observe+LateInitialize: in sync", policies("Observe", "LateInitialize"), okState, []string{"Observe"}, metav1.ConditionTrue, conditions.ReconcileSuccess, metav1.ConditionTrue, []string{"Observed"}},

		{"invalid: no Observe", policies("Create"), okState, []string{"Observe"}, metav1.ConditionFalse, conditions.ManagementPolicyBlocked, metav1.ConditionFalse, []string{"ManagementPolicyBlocked"}},
		{"invalid: star with others", policies("*", "Observe"), missingState, []string{"Observe"}, metav1.ConditionFalse, conditions.ManagementPolicyBlocked, metav1.ConditionFalse, []string{"ManagementPolicyBlocked"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t, nil, widget(withFinalizer, pinned(common.DeletionRetain, common.SourceOperatorDefault),
				func(w *testWidget) { w.Spec.ManagementPolicies = tt.policies }))
			ext := states[tt.state]()
			res, err := h.reconcileWidget(ext, h.dataOpts())
			wantResult(t, res, err, poll)
			if got := ext.Calls(); !slices.Equal(got, tt.wantCalls) {
				t.Errorf("calls = %v, want %v", got, tt.wantCalls)
			}
			w := h.widget("w")
			wantCond(t, w.Status.Conditions, synced, tt.synced, tt.reason)
			wantCond(t, w.Status.Conditions, ready, tt.ready, map[bool]string{true: conditions.Available, false: tt.reason}[tt.ready == metav1.ConditionTrue])
			wantCond(t, w.Status.Conditions, reach, metav1.ConditionTrue, conditions.Available)
			h.rec.AssertReasons(t, tt.events...)
		})
	}
}

func TestInvalidPoliciesNameTheProblem(t *testing.T) {
	h := newHarness(t, nil, widget(withFinalizer, pinned(common.DeletionRetain, common.SourceOperatorDefault),
		func(w *testWidget) { w.Spec.ManagementPolicies = policies("Create") }))
	if _, err := h.reconcileWidget(existing(), h.dataOpts()); err != nil {
		t.Fatal(err)
	}
	if msg := cond(h.widget("w").Status.Conditions, synced).Message; !strings.Contains(msg, "must contain Observe") {
		t.Errorf("message = %q", msg)
	}
}

func TestObservedIsEmittedOnceUntilSyncFails(t *testing.T) {
	h := newHarness(t, nil, widget(withFinalizer, pinned(common.DeletionRetain, common.SourceOperatorDefault)))
	ext := existing()
	for range 3 {
		if _, err := h.reconcileWidget(ext, h.dataOpts()); err != nil {
			t.Fatal(err)
		}
	}
	h.rec.AssertReasons(t, conditions.EventObserved)

	// A failure resets it: the next success is a state change again.
	ext.ObserveErr = errors.New("flaky")
	_, _ = h.reconcileWidget(ext, h.dataOpts())
	ext.ObserveErr = nil
	if _, err := h.reconcileWidget(ext, h.dataOpts()); err != nil {
		t.Fatal(err)
	}
	h.rec.AssertReasons(t, conditions.EventObserved, conditions.ReconcileError, conditions.EventObserved)
}

func TestExternalErrors(t *testing.T) {
	boom := errors.New("backend said no")
	tests := []struct {
		name      string
		setup     func(*fakeExt)
		fresh     bool
		wantErr   bool
		reason    string
		reachable metav1.ConditionStatus
		prefix    string
	}{
		{"observe", func(f *fakeExt) { f.ObserveErr = boom }, false, true, conditions.ReconcileError, metav1.ConditionUnknown, "Observe: "},
		{"observe unreachable", func(f *fakeExt) { f.ObserveErr = fmt.Errorf("dial: %w", lifecycle.ErrUnreachable) }, false, true, conditions.ServerUnreachable, metav1.ConditionFalse, "Observe: "},
		{"observe name collision", func(f *fakeExt) { f.ObserveErr = fmt.Errorf("owned by uid-x: %w", lifecycle.ErrNameCollision) }, false, false, conditions.NameCollision, metav1.ConditionUnknown, "Observe: "},
		{"create", func(f *fakeExt) { f.CreateErr = boom }, false, true, conditions.ReconcileError, metav1.ConditionTrue, "Create: "},
		{"update", func(f *fakeExt) { f.Obs.UpToDate = false; f.UpdateErr = boom }, true, true, conditions.ReconcileError, metav1.ConditionTrue, "Update: "},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t, nil, widget(withFinalizer, pinned(common.DeletionRetain, common.SourceOperatorDefault)))
			ext := missing()
			if tt.fresh {
				ext = existing()
			}
			tt.setup(ext)
			res, err := h.reconcileWidget(ext, h.dataOpts())
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if tt.wantErr && res.RequeueAfter != 0 {
				t.Errorf("an error result must not also requeue: %+v", res)
			}
			if !tt.wantErr && res.RequeueAfter != poll {
				t.Errorf("collision requeue = %v, want the poll interval", res.RequeueAfter)
			}
			w := h.widget("w")
			wantCond(t, w.Status.Conditions, synced, metav1.ConditionFalse, tt.reason)
			wantCond(t, w.Status.Conditions, ready, metav1.ConditionFalse, tt.reason)
			if c := cond(w.Status.Conditions, reach); c.Status != tt.reachable && tt.reachable != metav1.ConditionUnknown {
				t.Errorf("ServerReachable = %s, want %s", c.Status, tt.reachable)
			}
			if msg := cond(w.Status.Conditions, synced).Message; !strings.HasPrefix(msg, tt.prefix) {
				t.Errorf("message = %q, want prefix %q", msg, tt.prefix)
			}
			h.rec.AssertEvent(t, corev1.EventTypeWarning, tt.reason, tt.prefix)
		})
	}
}

func TestResolveErrors(t *testing.T) {
	tests := []struct {
		name      string
		err       error
		reason    string
		reachable metav1.ConditionStatus
		wantErr   bool
	}{
		{"namespace not allowed", fmt.Errorf("team-b: %w", server.ErrNamespaceNotAllowed), conditions.NamespaceNotAllowed, metav1.ConditionUnknown, false},
		{"server not found", fmt.Errorf("x: %w", server.ErrServerNotFound), conditions.ServerNotFound, metav1.ConditionFalse, false},
		{"admin secret namespace", fmt.Errorf("x: %w", server.ErrAdminSecretNamespaceForbidden), conditions.AdminSecretNamespaceForbidden, metav1.ConditionFalse, false},
		{"anything else is an unreachable server", errors.New("get secret: timeout"), conditions.ServerUnreachable, metav1.ConditionFalse, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t, nil, widget())
			opts := h.dataOpts()
			opts.ResolveErr = tt.err
			res, err := h.reconcileWidget(nil, opts)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if tt.wantErr && !errors.Is(err, tt.err) {
				t.Errorf("err = %v, want the resolve error", err)
			}
			wantRequeue := poll
			if tt.wantErr {
				wantRequeue = 0
			}
			if res.RequeueAfter != wantRequeue {
				t.Errorf("requeue = %v, want %v", res.RequeueAfter, wantRequeue)
			}
			w := h.widget("w")
			wantCond(t, w.Status.Conditions, synced, metav1.ConditionFalse, tt.reason)
			wantCond(t, w.Status.Conditions, ready, metav1.ConditionFalse, tt.reason)
			wantCond(t, w.Status.Conditions, reach, tt.reachable, tt.reason)
			h.rec.AssertEvent(t, corev1.EventTypeWarning, tt.reason, "")
			if !hasFinalizer(w) || w.Status.EffectiveDeletionPolicy == "" {
				t.Error("the pin and the finalizer come before the server is needed")
			}
		})
	}
}

func TestMissingExternalWithoutResolveError(t *testing.T) {
	h := newHarness(t, nil, widget(withFinalizer, pinned(common.DeletionRetain, common.SourceOperatorDefault)))
	if _, err := h.reconcileWidget(nil, h.dataOpts()); !errors.Is(err, lifecycle.ErrInvalidOptions) {
		t.Errorf("err = %v, want ErrInvalidOptions", err)
	}
}

func TestStatusWriteFailureIsReturnedWithTheCause(t *testing.T) {
	boom := errors.New("status boom")
	cause := errors.New("observe boom")
	h := newHarness(t, &interceptor.Funcs{SubResourceUpdate: func(context.Context, client.Client, string, client.Object, ...client.SubResourceUpdateOption) error {
		return boom
	}}, widget(withFinalizer, pinned(common.DeletionRetain, common.SourceOperatorDefault)))
	ext := missing()
	ext.ObserveErr = cause
	if _, err := h.reconcileWidget(ext, h.dataOpts()); !errors.Is(err, boom) || !errors.Is(err, cause) {
		t.Errorf("err = %v, want both errors", err)
	}
}

func TestInvalidOptions(t *testing.T) {
	h := newHarness(t, nil, widget())
	tests := []struct {
		name string
		mut  func(*lifecycle.Options)
	}{
		{"no class", func(o *lifecycle.Options) { o.Class = 0 }},
		{"unknown class", func(o *lifecycle.Options) { o.Class = 9 }},
		{"no recorder", func(o *lifecycle.Options) { o.Recorder = nil }},
		{"credential without binding type", func(o *lifecycle.Options) { *o = h.credOpts(); o.BindingType = "" }},
		{"credential without provider", func(o *lifecycle.Options) { *o = h.credOpts(); o.Provider = "" }},
		{"unknown default policy", func(o *lifecycle.Options) { o.DefaultDeletionPolicy = "Maybe" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opts := h.dataOpts()
			tt.mut(&opts)
			if _, err := h.reconcileWidget(missing(), opts); !errors.Is(err, lifecycle.ErrInvalidOptions) {
				t.Errorf("err = %v, want ErrInvalidOptions", err)
			}
		})
	}
	if hasFinalizer(h.widget("w")) {
		t.Error("an invalid configuration changed the claim")
	}
}

func TestCustomFinalizerName(t *testing.T) {
	h := newHarness(t, nil, widget())
	opts := h.dataOpts()
	opts.FinalizerName = "example.com/custom"
	if _, err := h.reconcileWidget(existing(), opts); err != nil {
		t.Fatal(err)
	}
	if got := h.widget("w").Finalizers; !slices.Equal(got, []string{"example.com/custom"}) {
		t.Errorf("finalizers = %v", got)
	}
}

func TestBindingSecretOfACredentialKind(t *testing.T) {
	h := newHarness(t, nil, gadget())
	ext := missing()
	ext.CreateData = binding.Data{"host": []byte("db.example"), "password": []byte("S3CR3T")}

	res, err := h.reconcileGadget(ext, h.credOpts())
	wantResult(t, res, err, poll)

	g := h.gadget("g")
	if !hasFinalizer(g) || g.GetEffectiveDeletionPolicy() != "" {
		t.Errorf("finalizer %v, pin %q: a credential kind has a finalizer and no pin", hasFinalizer(g), g.GetEffectiveDeletionPolicy())
	}
	if g.Status.Binding == nil || g.Status.Binding.Name != "g" {
		t.Errorf("status.binding = %+v", g.Status.Binding)
	}
	s := h.secret("g")
	if s.Type != "servicebinding.io/postgresql" || string(s.Data["password"]) != "S3CR3T" || string(s.Data["provider"]) != "craftainer.dev" ||
		string(s.Data["type"]) != "postgresql" || s.Labels[binding.LabelManagedBy] != "operator-test" {
		t.Errorf("secret = %+v", s)
	}
	if ref := metav1.GetControllerOf(s); ref == nil || ref.UID != "uid-g" {
		t.Errorf("owner = %+v", ref)
	}
	h.rec.AssertReasons(t, conditions.EventCreated)

	// An up-to-date object with no new data leaves the Secret alone.
	before := h.secret("g").ResourceVersion
	if _, err := h.reconcileGadget(ext, h.credOpts()); err != nil {
		t.Fatal(err)
	}
	if h.secret("g").ResourceVersion != before {
		t.Error("the Secret was rewritten without new data")
	}
}

func TestBindingSecretManagedByDefaults(t *testing.T) {
	h := newHarness(t, nil, gadget())
	ext := missing()
	ext.CreateData = binding.Data{"host": []byte("db")}
	opts := h.credOpts()
	opts.ManagedBy = ""
	if _, err := h.reconcileGadget(ext, opts); err != nil {
		t.Fatal(err)
	}
	if got := h.secret("g").Labels[binding.LabelManagedBy]; got != binding.DefaultManagedBy {
		t.Errorf("managed-by = %q, want %q", got, binding.DefaultManagedBy)
	}
}

func TestBindingSecretUsesTheSpecName(t *testing.T) {
	h := newHarness(t, nil, gadget(func(g *testGadget) { g.Spec.Binding.SecretName = "orders-db" }))
	ext := missing()
	ext.CreateData = binding.Data{"host": []byte("db")}
	if _, err := h.reconcileGadget(ext, h.credOpts()); err != nil {
		t.Fatal(err)
	}
	h.secret("orders-db")
	if g := h.gadget("g"); g.Status.Binding.Name != "orders-db" {
		t.Errorf("binding name = %q", g.Status.Binding.Name)
	}
}

func TestUpdateRewritesTheBindingSecret(t *testing.T) {
	h := newHarness(t, nil, gadget())
	ext := drifted()
	ext.UpdateData = binding.Data{"password": []byte("P2")}
	if _, err := h.reconcileGadget(ext, h.credOpts()); err != nil {
		t.Fatal(err)
	}
	if got := string(h.secret("g").Data["password"]); got != "P2" {
		t.Errorf("password = %q", got)
	}
	h.rec.AssertReasons(t, conditions.EventUpdated)
}

func TestDataKindsWriteNoSecret(t *testing.T) {
	h := newHarness(t, nil, widget())
	ext := missing()
	ext.CreateData = binding.Data{"host": []byte("db")}
	if _, err := h.reconcileWidget(ext, h.dataOpts()); err != nil {
		t.Fatal(err)
	}
	var secrets corev1.SecretList
	if err := h.c.List(h.ctx, &secrets); err != nil || len(secrets.Items) != 0 {
		t.Errorf("secrets = %d, %v", len(secrets.Items), err)
	}
}

func TestBindingSecretFailures(t *testing.T) {
	t.Run("a foreign Secret of that name is a name collision", func(t *testing.T) {
		foreign := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "g", Namespace: "ns"}, Data: map[string][]byte{"k": []byte("v")}}
		h := newHarness(t, nil, gadget(), foreign)
		ext := missing()
		ext.CreateData = binding.Data{"password": []byte("S3CR3T")}
		res, err := h.reconcileGadget(ext, h.credOpts())
		wantResult(t, res, err, poll)
		wantCond(t, h.gadget("g").Status.Conditions, synced, metav1.ConditionFalse, conditions.NameCollision)
		h.rec.AssertEvent(t, corev1.EventTypeWarning, conditions.NameCollision, "WriteSecret")
		if string(h.secret("g").Data["k"]) != "v" || h.secret("g").Data["password"] != nil {
			t.Error("the foreign Secret was modified")
		}
	})
	t.Run("an API failure is an error", func(t *testing.T) {
		boom := errors.New("secret boom")
		h := newHarness(t, &interceptor.Funcs{Create: func(ctx context.Context, c client.WithWatch, o client.Object, opts ...client.CreateOption) error {
			if _, ok := o.(*corev1.Secret); ok {
				return boom
			}
			return c.Create(ctx, o, opts...)
		}}, gadget())
		ext := missing()
		ext.CreateData = binding.Data{"password": []byte("x")}
		if _, err := h.reconcileGadget(ext, h.credOpts()); !errors.Is(err, boom) {
			t.Errorf("err = %v", err)
		}
		wantCond(t, h.gadget("g").Status.Conditions, synced, metav1.ConditionFalse, conditions.ReconcileError)
	})
}

func TestLateInitialize(t *testing.T) {
	boom := errors.New("late boom")
	policyWith := func(p ...common.ManagementPolicy) func(*testWidget) {
		return func(w *testWidget) { w.Spec.ManagementPolicies = p }
	}
	late := func(values map[string]string, err error) *fakeLate {
		f := &fakeLate{Values: values, LateErr: err}
		f.Obs = lifecycle.Observation[obj]{Exists: true, UpToDate: true, Current: "cur"}
		return f
	}
	tests := []struct {
		name      string
		mut       func(*testWidget)
		ext       func() lifecycle.External[spec, obj]
		wantCalls []string
		wantValue map[string]string
		wantErr   bool
	}{
		{"recorded under *", policyWith("*"), func() lifecycle.External[spec, obj] { return late(map[string]string{"region": "eu"}, nil) },
			[]string{"Observe", "LateInitialize"}, map[string]string{"region": "eu"}, false},
		{"recorded under an explicit list", policyWith("Observe", "LateInitialize"), func() lifecycle.External[spec, obj] { return late(map[string]string{"a": "b"}, nil) },
			[]string{"Observe", "LateInitialize"}, map[string]string{"a": "b"}, false},
		{"not asked for without the policy", policyWith("Observe", "Create", "Update"), func() lifecycle.External[spec, obj] { return late(map[string]string{"a": "b"}, nil) },
			[]string{"Observe"}, nil, false},
		{"not asked for before the object exists", policyWith("*"), func() lifecycle.External[spec, obj] {
			f := late(map[string]string{"a": "b"}, nil)
			f.Obs = lifecycle.Observation[obj]{}
			return f
		}, []string{"Observe", "Create"}, nil, false},
		{"an External without it accepts the policy and does nothing", policyWith("*"), func() lifecycle.External[spec, obj] { return existing() },
			[]string{"Observe"}, nil, false},
		{"an error fails the reconcile", policyWith("*"), func() lifecycle.External[spec, obj] { return late(nil, boom) },
			[]string{"Observe", "LateInitialize"}, nil, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t, nil, widget(withFinalizer, pinned(common.DeletionRetain, common.SourceOperatorDefault), tt.mut))
			ext := tt.ext()
			_, err := h.reconcileWidget(ext, h.dataOpts())
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			var calls []string
			switch f := ext.(type) {
			case *fakeLate:
				calls = f.Calls()
			case *fakeExt:
				calls = f.Calls()
			}
			if !slices.Equal(calls, tt.wantCalls) {
				t.Errorf("calls = %v, want %v", calls, tt.wantCalls)
			}
			got := h.widget("w").Status.LateInitialized
			if len(got) != len(tt.wantValue) || (len(got) > 0 && got["region"] != tt.wantValue["region"] && got["a"] != tt.wantValue["a"]) {
				t.Errorf("status.lateInitialized = %v, want %v", got, tt.wantValue)
			}
		})
	}
}

func TestAuditLog(t *testing.T) {
	h := newHarness(t, nil, widget(withFinalizer, pinned(common.DeletionRetain, common.SourceOperatorDefault)))
	opts := h.dataOpts()
	opts.Audit = audit.NewTracker()
	opts.ServerHost = "backend.example"
	ext := missing()

	if _, err := lifecycle.Reconcile(h.ctx, h.c, h.widget("w"), spec{Size: "s"}, ext, opts); err != nil {
		t.Fatal(err)
	}
	// A steady Observe is not a state change and writes no audit line.
	if _, err := lifecycle.Reconcile(h.ctx, h.c, h.widget("w"), spec{Size: "s"}, ext, opts); err != nil {
		t.Fatal(err)
	}
	// A spec change that the backend then applies is.
	ext.Obs.UpToDate = false
	if _, err := lifecycle.Reconcile(h.ctx, h.c, h.widget("w"), spec{Size: "m"}, ext, opts); err != nil {
		t.Fatal(err)
	}

	var lines []string
	for _, l := range h.logLines() {
		if strings.Contains(l, `"msg":"audit"`) {
			lines = append(lines, l)
		}
	}
	if len(lines) != 2 {
		t.Fatalf("audit lines = %d: %v", len(lines), lines)
	}
	for _, want := range []string{`"action":"Create"`, `"result":"success"`, `"kind":"Widget"`, `"namespace":"ns"`, `"name":"w"`,
		`"uid":"uid-w"`, `"controller":"operator-test"`, `"serverRef":"ClusterWidgetServer/shared"`, `"serverHost":"backend.example"`,
		`"changedFields":["size"]`} {
		if !strings.Contains(lines[0], want) {
			t.Errorf("create line lacks %s: %s", want, lines[0])
		}
	}
	if !strings.Contains(lines[1], `"action":"Update"`) || !strings.Contains(lines[1], `"changedFields":["size"]`) ||
		strings.Contains(lines[1], `"oldSpecSHA256":""`) {
		t.Errorf("update line = %s", lines[1])
	}
	if strings.Contains(lines[1], `"m"`) || strings.Contains(lines[1], `"s"`) {
		t.Errorf("the audit line carries spec values: %s", lines[1])
	}
}

func TestAuditFailureLine(t *testing.T) {
	h := newHarness(t, nil, widget(withFinalizer, pinned(common.DeletionRetain, common.SourceOperatorDefault)))
	ext := missing()
	ext.ObserveErr = errors.New("boom")
	_, _ = h.reconcileWidget(ext, h.dataOpts())
	found := false
	for _, l := range h.logLines() {
		if strings.Contains(l, `"msg":"audit"`) && strings.Contains(l, `"result":"ReconcileError"`) && strings.Contains(l, `"action":"Observe"`) {
			found = true
		}
	}
	if !found {
		t.Errorf("no audit line for the failure: %v", h.logLines())
	}
}

func TestAuditCannotFingerprintAnUnmarshalableSpec(t *testing.T) {
	h := newHarness(t, nil, widget(withFinalizer, pinned(common.DeletionRetain, common.SourceOperatorDefault)))
	ext := &lifecycletest.Fake[chan int, obj]{}
	if _, err := lifecycle.Reconcile(h.ctx, h.c, h.widget("w"), make(chan int), lifecycle.External[chan int, obj](ext), h.dataOpts()); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, l := range h.logLines() {
		found = found || strings.Contains(l, "cannot fingerprint the spec")
	}
	if !found {
		t.Errorf("no fingerprint error in the log: %v", h.logLines())
	}
}

// ADR 0002 section 11: credentials, tokens and Secret data are never in
// Events, logs, conditions or status.
func TestNoSecretDataInEventsLogsConditionsOrStatus(t *testing.T) {
	secrets := []string{"S3CR3T-password", "S3CR3T-clientSecret"}
	h := newHarness(t, nil, gadget())
	ext := missing()
	ext.CreateData = binding.Data{"password": []byte(secrets[0]), "clientSecret": []byte(secrets[1])}
	if _, err := h.reconcileGadget(ext, h.credOpts()); err != nil {
		t.Fatal(err)
	}
	// Reconcile again with a drift update, and once more in steady state.
	ext.Obs.UpToDate = false
	ext.UpdateData = binding.Data{"password": []byte(secrets[0])}
	_, _ = h.reconcileGadget(ext, h.credOpts())
	_, _ = h.reconcileGadget(ext, h.credOpts())

	h.rec.AssertNoSecret(t, secrets...)
	status, err := json.Marshal(h.gadget("g").Status)
	if err != nil {
		t.Fatal(err)
	}
	everything := string(status) + strings.Join(h.logLines(), "\n")
	for _, s := range secrets {
		if strings.Contains(everything, s) {
			t.Errorf("secret value %q reached the status or the log", s)
		}
	}
	if string(h.secret("g").Data["password"]) != secrets[0] {
		t.Error("the binding Secret itself must hold the value")
	}
}
