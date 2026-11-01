package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/apiutil"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/craftainer-labs/lib-operator-go/pkg/apis/common"
	"github.com/craftainer-labs/lib-operator-go/pkg/audit"
	"github.com/craftainer-labs/lib-operator-go/pkg/binding"
	"github.com/craftainer-labs/lib-operator-go/pkg/conditions"
	"github.com/craftainer-labs/lib-operator-go/pkg/server"
)

// run is one Reconcile call.
type run[S, O any] struct {
	ctx    context.Context
	c      client.Client
	claim  Claim
	spec   S
	ext    External[S, O]
	opts   Options
	now    time.Time
	pol    common.Policies
	polErr error
	poll   time.Duration
}

// Reconcile drives one claim towards its spec (ADR 0002): it pins the default
// deletion policy of a data kind and adds the finalizer; observes the external
// object through ext; creates, updates or leaves it according to
// managementPolicies; writes the binding Secret of a credential kind; runs the
// two-phase rotation; and keeps the Ready, Synced and ServerReachable
// conditions. On deletion it runs the finalizer's outcome table (section 4).
// It requeues at the drift-poll interval plus up to 10% jitter.
//
// spec is what ext needs from the claim. ext may be nil only when
// opts.ResolveErr is set. claim must be the freshly read object; Reconcile
// updates its status and metadata through c.
func Reconcile[S, O any](ctx context.Context, c client.Client, claim Claim,
	spec S, ext External[S, O], opts Options) (ctrl.Result, error) {
	opts, err := opts.resolve()
	if err != nil {
		return ctrl.Result{}, err
	}
	r := &run[S, O]{ctx: ctx, c: c, claim: claim, spec: spec, ext: ext, opts: opts, now: opts.Now()}
	r.pol, r.polErr = common.ParseManagementPolicies(claim.GetManagementPolicies())
	r.poll = r.pollInterval()
	if !claim.GetDeletionTimestamp().IsZero() {
		return r.finalize()
	}
	if claim.GetAnnotations()[AnnotationPaused] == "true" {
		return r.requeue(), nil
	}
	if err := r.pin(); err != nil {
		return ctrl.Result{}, err
	}
	return r.sync()
}

func (r *run[S, O]) event(eventType, reason, action, note string, args ...any) {
	r.opts.Recorder.Eventf(r.claim, nil, eventType, reason, action, note, args...)
}

func (r *run[S, O]) set(typ string, status metav1.ConditionStatus, reason, msg string) {
	conditions.Set(r.claim.GetConditions(), r.claim.GetGeneration(), typ, status, reason, msg)
}

// pollInterval returns the annotation's value, or the default (with a Warning
// Event) when the annotation is not a duration between 30s and 24h.
func (r *run[S, O]) pollInterval() time.Duration {
	uid := r.claim.GetUID()
	v, ok := r.claim.GetAnnotations()[AnnotationPollInterval]
	if !ok {
		r.opts.Audit.Clear(uid, noticePollInterval)
		return r.opts.DefaultPollInterval
	}
	d, err := time.ParseDuration(v)
	if err != nil || d < MinPollInterval || d > MaxPollInterval {
		// Warn when the value changes, not on every reconcile.
		if r.opts.Audit.Notice(uid, noticePollInterval, v) {
			r.event(corev1.EventTypeWarning, conditions.EventInvalidPollInterval, "Reconcile",
				"annotation %s=%q is not a duration between %s and %s; using %s",
				AnnotationPollInterval, v, MinPollInterval, MaxPollInterval, r.opts.DefaultPollInterval)
		}
		return r.opts.DefaultPollInterval
	}
	r.opts.Audit.Clear(uid, noticePollInterval)
	return d
}

// noticePollInterval is the audit.Tracker notice key of an invalid poll interval.
const noticePollInterval = "pollInterval"

func (r *run[S, O]) requeue() ctrl.Result {
	extra := time.Duration(float64(r.poll) * jitterFraction * r.opts.Jitter())
	return ctrl.Result{RequeueAfter: r.poll + extra}
}

// persist stamps a completed reconcile (observedGeneration, lastReconcileTime)
// and writes the status. The caller has set the conditions.
func (r *run[S, O]) persist() error {
	r.claim.SetObservedGeneration(r.claim.GetGeneration())
	r.claim.SetLastReconcileTime(metav1.NewTime(r.now))
	return r.save()
}

// save writes the status as it is. The deletion-policy pin uses it: a pin is
// not a completed reconcile and must not claim one.
func (r *run[S, O]) save() error {
	return r.gone(r.c.Status().Update(r.ctx, r.claim))
}

// gone forgets what the audit tracker holds when err says the claim no longer
// exists, and returns err.
func (r *run[S, O]) gone(err error) error {
	if apierrors.IsNotFound(err) {
		r.opts.Audit.Forget(r.claim.GetUID())
	}
	return err
}

// done persists the status, writes the audit line for a state change or a
// failure, and returns the result.
func (r *run[S, O]) done(action, result string, res ctrl.Result, err error) (ctrl.Result, error) {
	// Audit first: a status write that finds the claim gone then forgets it.
	r.audit(action, result, action != "Observe" || result != "success")
	if perr := r.persist(); perr != nil {
		err = errors.Join(err, fmt.Errorf("update status: %w", perr))
	}
	return res, err
}

// audit fingerprints the spec on every reconcile, so the tracker always knows
// the previous spec, and writes the audit line only when write is true.
func (r *run[S, O]) audit(action, result string, write bool) {
	cl := r.claim
	changed, oldSum, newSum, err := r.opts.Audit.Observe(cl.GetUID(), r.spec)
	if err != nil {
		log.FromContext(r.ctx).Error(err, "audit: cannot fingerprint the spec")
	}
	if !write {
		return
	}
	gvk, _ := apiutil.GVKForObject(cl, r.c.Scheme())
	ref := cl.GetServerRef()
	audit.Log(r.ctx, audit.Entry{
		Controller:      r.opts.ManagedBy,
		LastManager:     audit.LastManager(cl),
		Kind:            gvk.Kind,
		Namespace:       cl.GetNamespace(),
		Name:            cl.GetName(),
		UID:             cl.GetUID(),
		Generation:      cl.GetGeneration(),
		ExternalName:    cl.GetExternalName(),
		Action:          action,
		Result:          result,
		ServerRef:       ref.Kind + "/" + ref.Name,
		ServerHost:      r.opts.ServerHost,
		ChangedFields:   changed,
		PreviousUnknown: oldSum == "",
		OldSpecSHA256:   oldSum,
		NewSpecSHA256:   newSum,
	})
}

// pin records the default deletion policy of a data kind in the status, and
// only then adds the finalizer (ADR 0002, section 4): a crash between the two
// leaves a pin without a finalizer, which this call completes. A finalizer
// already present with no pin means the status was lost.
func (r *run[S, O]) pin() error {
	cl := r.claim
	hasFinalizer := controllerutil.ContainsFinalizer(cl, r.opts.FinalizerName)
	if r.opts.Class == ClassData && cl.GetEffectiveDeletionPolicy() == "" {
		if hasFinalizer {
			cl.SetEffectiveDeletionPolicy(common.DeletionRetain, common.SourceFallback)
		} else {
			cl.SetEffectiveDeletionPolicy(r.opts.DefaultDeletionPolicy, common.SourceOperatorDefault)
		}
		conditions.Ensure(cl.GetConditions(), cl.GetGeneration())
		if err := r.save(); err != nil {
			return fmt.Errorf("pin the deletion policy: %w", err)
		}
		if hasFinalizer {
			r.event(corev1.EventTypeWarning, conditions.EventDeletionPolicyRepinned, "Reconcile",
				"status.effectiveDeletionPolicy was lost; pinned %s (source %s) instead of re-reading %s",
				common.DeletionRetain, common.SourceFallback, EnvDefaultDataDeletionPolicy)
		} else {
			r.event(corev1.EventTypeNormal, conditions.EventDeletionPolicyResolved, "Reconcile",
				"deletion policy %s pinned from the operator default", r.opts.DefaultDeletionPolicy)
		}
	}
	if hasFinalizer {
		return nil
	}
	orig := cl.DeepCopyObject().(client.Object)
	controllerutil.AddFinalizer(cl, r.opts.FinalizerName)
	if err := r.gone(r.c.Patch(r.ctx, cl, client.MergeFromWithOptions(orig, client.MergeFromWithOptimisticLock{}))); err != nil {
		return fmt.Errorf("add the finalizer: %w", err)
	}
	return nil
}

// sync is the reconcile of a claim that is not being deleted and not paused.
func (r *run[S, O]) sync() (ctrl.Result, error) {
	cl := r.claim
	conditions.Ensure(cl.GetConditions(), cl.GetGeneration())
	if r.opts.ResolveErr != nil {
		reason, final := r.resolveFailure()
		return r.resolveResult("Observe", reason, final)
	}
	if r.ext == nil {
		return ctrl.Result{}, fmt.Errorf("%w: neither an External nor ResolveErr", ErrInvalidOptions)
	}
	wasSynced := meta.IsStatusConditionTrue(*cl.GetConditions(), conditions.Synced)
	obs, err := r.ext.Observe(r.ctx, r.spec)
	if err != nil {
		return r.failed("Observe", err)
	}
	r.set(conditions.ServerReachable, metav1.ConditionTrue, conditions.Available, "server reachable")
	if r.polErr != nil {
		return r.blocked(conditions.ManagementPolicyBlocked, r.polErr.Error(), true)
	}

	var data binding.Data
	var did string
	switch {
	case !obs.Exists:
		if !r.pol.Create {
			return r.blockedByPolicy("external object not found")
		}
		d, err := r.ext.Create(r.ctx, r.spec)
		if err != nil {
			return r.failed("Create", err)
		}
		data, did = d, "Create"
		r.event(corev1.EventTypeNormal, conditions.EventCreated, "Create", "created the external object")
	case !obs.UpToDate:
		if !r.pol.Update {
			return r.blockedByPolicy("external object differs from the spec")
		}
		d, err := r.ext.Update(r.ctx, r.spec, obs.Current)
		if err != nil {
			return r.failed("Update", err)
		}
		data, did = d, "Update"
		r.event(corev1.EventTypeNormal, conditions.EventUpdated, "Update", "updated the external object")
	}
	if r.opts.Class == ClassCredential && did != "" {
		if len(data) == 0 {
			// The credential was created or changed on the backend but never
			// delivered: say so instead of reporting Ready.
			return r.failed(did, ErrNoBindingData)
		}
		if err := r.writeBinding(data); err != nil {
			return r.failed("WriteSecret", err)
		}
	}
	if li, ok := r.ext.(LateInitializer[S, O]); ok && r.pol.LateInitialize {
		cur, exists := obs.Current, obs.Exists
		if did != "" { // the observation above predates the change: look again
			again, err := r.ext.Observe(r.ctx, r.spec)
			if err != nil {
				return r.failed("Observe", err)
			}
			cur, exists = again.Current, again.Exists
		}
		if exists {
			values, err := li.LateInitialize(r.ctx, r.spec, cur)
			if err != nil {
				return r.failed("LateInitialize", err)
			}
			cl.SetLateInitialized(values)
		}
	}

	var rot rotationOutcome
	if did == "" && r.opts.Class == ClassCredential {
		if r.pol.Create || r.pol.Update { // an Observe-only claim has no credentials of its own
			if rot, err = r.ensureBinding(obs.Current); err != nil {
				return r.failed("Binding", err)
			}
		}
		if !rot.rotated && !rot.bindingMissing {
			if rot, err = r.rotate(obs.Current); err != nil {
				return r.failed("Rotate", err)
			}
		}
	}
	return r.succeeded(did, wasSynced, rot)
}

func (r *run[S, O]) succeeded(did string, wasSynced bool, rot rotationOutcome) (ctrl.Result, error) {
	r.set(conditions.Ready, metav1.ConditionTrue, conditions.Available, "the external object is up to date")
	res := r.requeue()
	action, healthy := "Observe", true
	if did != "" {
		action = did
	}
	if rot.rotated || rot.finished {
		action = "Rotate"
	}
	switch {
	case rot.bindingMissing:
		healthy = false
		r.set(conditions.Ready, metav1.ConditionFalse, conditions.BindingSecretMissing, rot.missingWhy)
		r.set(conditions.Synced, metav1.ConditionFalse, conditions.BindingSecretMissing, rot.missingWhy)
		r.event(corev1.EventTypeWarning, conditions.BindingSecretMissing, "Reconcile", "%s", rot.missingWhy)
	case rot.unsupported:
		healthy = false
		r.set(conditions.Synced, metav1.ConditionFalse, conditions.RotationUnsupported,
			"the backend of this kind cannot rotate credentials")
	case rot.blocked:
		healthy = false
		r.set(conditions.Synced, metav1.ConditionFalse, conditions.ManagementPolicyBlocked,
			"rotation needs the Update management policy")
		r.event(corev1.EventTypeWarning, conditions.ManagementPolicyBlocked, "Rotate",
			"rotation is due but managementPolicies lacks Update")
	case rot.grace:
		r.set(conditions.Synced, metav1.ConditionTrue, conditions.Rotating,
			"the previous credential generation is still valid during the grace period")
		res.RequeueAfter = min(res.RequeueAfter, rot.retireIn)
	default:
		r.set(conditions.Synced, metav1.ConditionTrue, conditions.ReconcileSuccess, "in sync")
	}
	if healthy && did == "" && !rot.rotated && !rot.finished && !wasSynced {
		r.event(corev1.EventTypeNormal, conditions.EventObserved, "Observe", "observed the external object")
	}
	return r.done(action, "success", res, nil)
}

// blockedByPolicy reports an object that is missing or has drifted while the
// policy forbids the fix: reason ObserveOnly for exactly ["Observe"],
// ManagementPolicyBlocked otherwise.
func (r *run[S, O]) blockedByPolicy(msg string) (ctrl.Result, error) {
	if r.pol.ObserveOnly {
		return r.blocked(conditions.ObserveOnly, msg, false)
	}
	return r.blocked(conditions.ManagementPolicyBlocked, msg, true)
}

func (r *run[S, O]) blocked(reason, msg string, warn bool) (ctrl.Result, error) {
	r.set(conditions.Synced, metav1.ConditionFalse, reason, msg)
	r.set(conditions.Ready, metav1.ConditionFalse, reason, msg)
	if warn {
		r.event(corev1.EventTypeWarning, reason, "Reconcile", "%s", msg)
	}
	return r.done("Observe", reason, r.requeue(), nil)
}

// failed records an error from the External and decides how to retry: a name
// collision is requeued at the poll interval without error (backoff will not
// fix it); everything else is returned so controller-runtime backs off.
func (r *run[S, O]) failed(action string, err error) (ctrl.Result, error) {
	msg := action + ": " + err.Error()
	switch {
	case errors.Is(err, ErrNameCollision):
		r.set(conditions.Synced, metav1.ConditionFalse, conditions.NameCollision, msg)
		r.set(conditions.Ready, metav1.ConditionFalse, conditions.NameCollision, msg)
		r.event(corev1.EventTypeWarning, conditions.NameCollision, action, "%s", msg)
		return r.done(action, conditions.NameCollision, r.requeue(), nil)
	case errors.Is(err, ErrUnreachable):
		r.set(conditions.ServerReachable, metav1.ConditionFalse, conditions.ServerUnreachable, msg)
		r.set(conditions.Synced, metav1.ConditionFalse, conditions.ServerUnreachable, msg)
		r.set(conditions.Ready, metav1.ConditionFalse, conditions.ServerUnreachable, msg)
		r.event(corev1.EventTypeWarning, conditions.ServerUnreachable, action, "%s", msg)
		return r.done(action, conditions.ServerUnreachable, ctrl.Result{}, err)
	default:
		r.set(conditions.Synced, metav1.ConditionFalse, conditions.ReconcileError, msg)
		r.set(conditions.Ready, metav1.ConditionFalse, conditions.ReconcileError, msg)
		r.event(corev1.EventTypeWarning, conditions.ReconcileError, action, "%s", msg)
		return r.done(action, conditions.ReconcileError, ctrl.Result{}, err)
	}
}

// resolveFailure sets the conditions and the Event for opts.ResolveErr. final
// is true for the errors that backoff cannot fix and that a watch on the
// server or the namespace will retrigger: the claim is requeued at the poll
// interval without an error.
func (r *run[S, O]) resolveFailure() (reason string, final bool) {
	err := r.opts.ResolveErr
	reachable := metav1.ConditionFalse
	switch {
	case errors.Is(err, server.ErrNamespaceNotAllowed):
		reason, final, reachable = conditions.NamespaceNotAllowed, true, metav1.ConditionUnknown
	case errors.Is(err, server.ErrServerNotFound):
		reason, final = conditions.ServerNotFound, true
	case errors.Is(err, server.ErrAdminSecretNamespaceForbidden):
		reason, final = conditions.AdminSecretNamespaceForbidden, true
	default:
		reason = conditions.ServerUnreachable
	}
	msg := err.Error()
	r.set(conditions.ServerReachable, reachable, reason, msg)
	r.set(conditions.Synced, metav1.ConditionFalse, reason, msg)
	r.set(conditions.Ready, metav1.ConditionFalse, reason, msg)
	r.event(corev1.EventTypeWarning, reason, "Resolve", "%s", msg)
	return reason, final
}

func (r *run[S, O]) resolveResult(action, reason string, final bool) (ctrl.Result, error) {
	if final {
		return r.done(action, reason, r.requeue(), nil)
	}
	return r.done(action, reason, ctrl.Result{}, r.opts.ResolveErr)
}

func (r *run[S, O]) writeBinding(data binding.Data) error {
	name := r.claim.GetBindingSecretName()
	err := binding.WriteSecret(r.ctx, r.c, r.claim, name, r.opts.BindingType, r.opts.Provider, data, r.writeOpts()...)
	if err != nil {
		return err
	}
	r.claim.SetBindingName(name)
	return nil
}

// writeOpts are the binding options every Secret read and write shares.
func (r *run[S, O]) writeOpts() []binding.WriteOption {
	opts := []binding.WriteOption{binding.WithManagedBy(r.opts.ManagedBy)}
	if r.opts.APIReader != nil {
		opts = append(opts, binding.WithReader(r.opts.APIReader))
	}
	return opts
}
