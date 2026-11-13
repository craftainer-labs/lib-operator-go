package probe

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"strings"
	"time"
	"unicode/utf8"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/apiutil"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/craftainer-labs/lib-operator-go/pkg/apis/common"
	"github.com/craftainer-labs/lib-operator-go/pkg/conditions"
	"github.com/craftainer-labs/lib-operator-go/pkg/server"
)

// Defaults of Options, and the longest serverVersion that is kept (the schema's
// maxLength of common.ServerStatus).
const (
	// DefaultInterval is the probe interval of a server without
	// healthCheckInterval.
	DefaultInterval = time.Minute
	// DefaultRefreshInterval is how old lastProbeTime may get while nothing
	// else changes.
	DefaultRefreshInterval = 10 * time.Minute
	// DefaultTimeout bounds one call of Prober.Probe.
	DefaultTimeout = 30 * time.Second
	// MaxVersionLength is the longest serverVersion written, in bytes.
	MaxVersionLength = 64

	// jitterFraction is the largest share added to the probe interval.
	jitterFraction = 0.1
)

// Fixed condition messages: the status says what is wrong, the log says why.
const (
	msgReachable      = "the server answered its health probe"
	msgReady          = "the server is ready"
	msgUnreachable    = "the server did not answer its health probe"
	msgNotReady       = "the server is not reachable"
	msgForbidden      = "adminCredentialsSecretRef names a Secret in a namespace this server may not use"
	msgNotResolvable  = "the connection settings of the server are missing or invalid"
	msgNotResolvedRdy = "the server cannot be probed"
)

// Prober is what an operator implements for its backend. Probe checks that the
// server answers, using the operator's own cached client for it (built from
// conn, whose Admin credentials and CA bundle the helper has already read), and
// returns the version the backend reported, "" if it has none. The error is
// logged, not shown in the status. srv is the server object being probed; a
// Prober that needs the kind's own fields asserts it to the operator's type.
type Prober interface {
	Probe(ctx context.Context, srv server.Server, conn server.Connection) (version string, err error)
}

// ProberFunc adapts a function to Prober.
type ProberFunc func(ctx context.Context, srv server.Server, conn server.Connection) (string, error)

// Probe calls f.
func (f ProberFunc) Probe(ctx context.Context, srv server.Server, conn server.Connection) (string, error) {
	return f(ctx, srv, conn)
}

// Server is a server kind that has a status the helper can write: the
// server.Server every kind implements for Resolve, plus access to its status.
type Server interface {
	server.Server
	// GetServerStatus returns the kind's status field.
	GetServerStatus() *common.ServerStatus
}

// Options configure a Reconciler. The zero value is usable.
type Options struct {
	// OperatorNamespace is the namespace of a cluster server's admin Secret
	// and CA bundle (server.WithOperatorNamespace); empty falls back to
	// POD_NAMESPACE.
	OperatorNamespace string
	// DefaultInterval is the probe interval of a server that sets no
	// healthCheckInterval; default DefaultInterval. A server's own value is
	// bounded to 10s..1h by the CRD and by server.ConnectionFor.
	DefaultInterval time.Duration
	// RefreshInterval is how often lastProbeTime is rewritten while the result
	// does not change; default DefaultRefreshInterval. The status is written
	// when the result changes (a condition, serverVersion, observedGeneration)
	// and otherwise only once per RefreshInterval. So lastProbeTime is the
	// time of the last probe whose result was recorded, at most
	// RefreshInterval old while the operator runs, and a healthy fleet does
	// not write one status per server per minute.
	RefreshInterval time.Duration
	// Timeout bounds one Probe call; default DefaultTimeout.
	Timeout time.Duration
}

func (o Options) withDefaults() Options {
	if o.DefaultInterval <= 0 {
		o.DefaultInterval = DefaultInterval
	}
	if o.RefreshInterval <= 0 {
		o.RefreshInterval = DefaultRefreshInterval
	}
	if o.Timeout <= 0 {
		o.Timeout = DefaultTimeout
	}
	return o
}

// drawJitter returns a number in [0, 1). Spreading probes does not need a
// cryptographic source.
func drawJitter() float64 {
	return rand.Float64() //nolint:gosec // requeue jitter, not security-relevant
}

// Reconciler is a controller-runtime reconciler of one server kind, and the
// package's only entry point: wire it with SetupWithManager.
//
// A probe is a health check on a schedule, as in Kubernetes: it runs every
// healthCheckInterval (plus up to 10% jitter) whether the last one succeeded
// or not. A failed probe is recorded in the status (ServerUnreachable) and
// logged, and is not a reconcile error, so controller-runtime's error backoff
// never stretches the interval. Reconcile returns an error only when the
// connection settings cannot be read for a reason other than the server's
// configuration (an API server error) or when the status write fails.
//
//   - A server that is being deleted is left alone.
//   - The connection settings come from server.ConnectionFor. A server whose
//     admin Secret is in a forbidden namespace gets Ready=False and
//     ServerReachable=False, reason AdminSecretNamespaceForbidden. Settings
//     that are missing or invalid (a missing Secret or ConfigMap, an invalid
//     proxy URL or CA reference, an unset operator namespace) get Ready=False,
//     ServerReachable=Unknown, reason InvalidServer. Both are logged and
//     requeued at the interval; editing the server or creating the Secret or
//     ConfigMap it names probes at once.
//   - A probe error sets Ready=False and ServerReachable=False, reason
//     ServerUnreachable, and keeps the last known serverVersion. Success sets
//     both True (reason Available) and updates serverVersion.
//   - If the context is cancelled (the manager is stopping) while probing,
//     nothing is recorded.
//
// The status is written, as a merge patch, only if something changed (see
// Options.RefreshInterval). Every condition carries the server's generation,
// and status.observedGeneration is set with them. A failure is logged at Error
// when the condition changes and at V(1) while it stays.
type Reconciler struct {
	Client client.Client
	// New returns an empty object of the kind, for example
	// func() probe.Server { return &v1alpha1.DatabaseServer{} }. It serves a
	// cluster-scoped and a namespaced kind alike. The kind and its list type
	// must be in the manager's scheme.
	New     func() Server
	Prober  Prober
	Options Options
	// Controller are the options of the controller SetupWithManager builds,
	// for example MaxConcurrentReconciles. The zero value keeps the defaults.
	Controller controller.Options

	// now and jitter are tests' seams; nil means time.Now and a math/rand draw.
	now    func() time.Time
	jitter func() float64
}

// Reconcile reads the server named by req and probes it. A server that no
// longer exists is not an error.
func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	srv := r.New()
	if err := r.Client.Get(ctx, req.NamespacedName, srv); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	return r.probeServer(ctx, srv)
}

func (r *Reconciler) probeServer(ctx context.Context, srv Server) (ctrl.Result, error) {
	if !srv.GetDeletionTimestamp().IsZero() {
		return ctrl.Result{}, nil
	}
	o := r.Options.withDefaults()
	now, jitter := r.now, r.jitter
	if now == nil {
		now = time.Now
	}
	if jitter == nil {
		jitter = drawJitter
	}
	st, gen := srv.GetServerStatus(), srv.GetGeneration()
	name := srv.GetName()
	base := srv.DeepCopyObject().(client.Object) // the unmodified object the patch is computed against

	conn, err := server.ConnectionFor(ctx, r.Client, srv,
		server.WithOperatorNamespace(o.OperatorNamespace), server.WithHealthCheckInterval(o.DefaultInterval))
	every := ctrl.Result{RequeueAfter: conn.HealthCheckInterval + time.Duration(float64(conn.HealthCheckInterval)*jitterFraction*jitter())}
	if err != nil {
		if !misconfigured(err) { // an API hiccup says nothing about the server
			return ctrl.Result{}, fmt.Errorf("server %q: %w", name, err)
		}
		changed := observe(st, gen)
		reachable, reason, reachableMsg, readyMsg := metav1.ConditionUnknown, conditions.InvalidServer, msgNotResolvable, msgNotResolvedRdy
		if errors.Is(err, server.ErrAdminSecretNamespaceForbidden) {
			reachable, reason, reachableMsg, readyMsg = metav1.ConditionFalse, conditions.AdminSecretNamespaceForbidden, msgForbidden, msgForbidden
		}
		moved := setConditions(st, gen, reachable, metav1.ConditionFalse, reason, reachableMsg, readyMsg)
		logFailure(ctx, moved, err, "server not probed", name)
		return every, save(ctx, r.Client, srv, base, changed || moved)
	}

	pctx, cancel := context.WithTimeout(ctx, o.Timeout)
	defer cancel()
	version, perr := r.Prober.Probe(pctx, srv, conn)
	if ctx.Err() != nil { // the manager is stopping: a probe cut short is not a failure, and the write would fail
		return ctrl.Result{}, nil
	}
	changed := observe(st, gen)
	if perr != nil {
		moved := setConditions(st, gen, metav1.ConditionFalse, metav1.ConditionFalse,
			conditions.ServerUnreachable, msgUnreachable, msgNotReady)
		logFailure(ctx, moved, perr, "server did not answer its health probe", name)
		changed = changed || moved
	} else {
		moved := setConditions(st, gen, metav1.ConditionTrue, metav1.ConditionTrue,
			conditions.Available, msgReachable, msgReady)
		changed = changed || moved
		if v := cleanVersion(version); v != st.ServerVersion {
			st.ServerVersion, changed = v, true
		}
	}
	at := now()
	if changed || st.LastProbeTime == nil || at.Sub(st.LastProbeTime.Time) >= o.RefreshInterval {
		st.LastProbeTime, changed = &metav1.Time{Time: at}, true
	}
	return every, save(ctx, r.Client, srv, base, changed)
}

// logFailure logs why a server is not Ready: at Error when its condition just
// changed, at V(1) while it stays, so a long outage is one error line.
func logFailure(ctx context.Context, transition bool, err error, msg, name string) {
	l := log.FromContext(ctx)
	if transition {
		l.Error(err, msg, "server", name)
		return
	}
	l.V(1).Info(msg, "server", name, "error", err.Error())
}

// misconfigured reports whether err from server.ConnectionFor is a property of
// the server's configuration (so it belongs in the status) and not a transient
// failure such as an API server timeout, which is only returned.
func misconfigured(err error) bool {
	return errors.Is(err, server.ErrAdminSecretNamespaceForbidden) ||
		errors.Is(err, server.ErrInvalidServer) ||
		errors.Is(err, server.ErrOperatorNamespaceUnset) ||
		apierrors.IsNotFound(err)
}

// observe records the generation the status is about and reports a change.
func observe(st *common.ServerStatus, gen int64) bool {
	if st.ObservedGeneration == gen {
		return false
	}
	st.ObservedGeneration = gen
	return true
}

// setConditions sets ServerReachable and Ready with one reason, and reports
// whether either changed.
func setConditions(st *common.ServerStatus, gen int64, reachable, ready metav1.ConditionStatus, reason, reachableMsg, readyMsg string) bool {
	a := conditions.Set(&st.Conditions, gen, conditions.ServerReachable, reachable, reason, reachableMsg)
	b := conditions.Set(&st.Conditions, gen, conditions.Ready, ready, reason, readyMsg)
	return a || b
}

// cleanVersion makes a reported version fit the schema: valid UTF-8, at most
// MaxVersionLength bytes, cut at a character boundary. The same input always
// gives the same output, so an unchanged backend never looks changed.
func cleanVersion(v string) string {
	v = strings.ToValidUTF8(v, "")
	if len(v) <= MaxVersionLength {
		return v
	}
	cut := MaxVersionLength
	for !utf8.RuneStart(v[cut]) {
		cut--
	}
	return v[:cut]
}

// save patches the status against base if changed. A merge patch has no
// resourceVersion precondition, so a concurrent write to another field never
// makes it fail. A server that is gone is not an error.
func save(ctx context.Context, c client.Client, srv Server, base client.Object, changed bool) error {
	if !changed {
		return nil
	}
	if err := c.Status().Patch(ctx, srv, client.MergeFrom(base)); err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("patch status of server %q: %w", srv.GetName(), err)
	}
	return nil
}

// Predicates is the event filter of the server watch: creation and a spec
// change (metadata.generation) trigger a probe; the helper's own status writes
// do not. SetupWithManager applies it to the server only, never to the Secrets
// and ConfigMaps it also watches.
func Predicates() predicate.Predicate { return predicate.GenerationChangedPredicate{} }

// SetupWithManager registers the reconciler as the controller of the kind New
// returns. It watches the servers (filtered by Predicates) and the Secrets and
// ConfigMaps they name (the admin Secret, the CA bundle), so creating or
// fixing one probes the servers that use it at once. The operator needs get,
// list and watch on Secrets and ConfigMaps (its cached client does already)
// and get, list, watch and patch on the servers' status. The controller is
// named after the kind, so an operator with several server kinds calls it once
// for each.
func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	newList, err := listFor(mgr.GetScheme(), r.New())
	if err != nil {
		return err
	}
	mapper := func(uses func(Server, client.Object) bool) handler.EventHandler {
		return handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, obj client.Object) []reconcile.Request {
			return r.dependents(ctx, newList(), obj, uses)
		})
	}
	return ctrl.NewControllerManagedBy(mgr).
		For(r.New(), builder.WithPredicates(Predicates())).
		Watches(&corev1.Secret{}, mapper(usesSecret)).
		Watches(&corev1.ConfigMap{}, mapper(usesConfigMap)).
		WithOptions(r.Controller).
		Complete(r)
}

// listFor returns a constructor of the list type of obj's kind, which the
// scheme must know.
func listFor(s *runtime.Scheme, obj Server) (func() client.ObjectList, error) {
	gvk, err := apiutil.GVKForObject(obj, s)
	if err != nil {
		return nil, err
	}
	listGVK := gvk.GroupVersion().WithKind(gvk.Kind + "List")
	newList := func() client.ObjectList {
		l, _ := s.New(listGVK)
		list, _ := l.(client.ObjectList)
		return list
	}
	if newList() == nil {
		return nil, fmt.Errorf("the scheme has no list type %s", listGVK)
	}
	return newList, nil
}

// dependents returns the servers that use obj (a Secret or ConfigMap that was
// created, changed or deleted). It may name a server whose reference only
// matches by name, never misses one, and a spurious probe is cheap.
func (r *Reconciler) dependents(ctx context.Context, list client.ObjectList, obj client.Object, uses func(Server, client.Object) bool) []reconcile.Request {
	if err := r.Client.List(ctx, list); err != nil {
		log.FromContext(ctx).Error(err, "cannot list the servers that use a changed object", "object", client.ObjectKeyFromObject(obj))
		return nil
	}
	var out []reconcile.Request
	_ = meta.EachListItem(list, func(o runtime.Object) error { // cannot fail: list is a list and the callback returns nil
		if srv, ok := o.(Server); ok && uses(srv, obj) {
			out = append(out, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(srv)})
		}
		return nil
	})
	return out
}

// usesSecret reports whether srv names the Secret as its admin credentials or
// CA bundle. A namespaced server's Secrets are in its namespace; a cluster
// server's are in the operator namespace, which is not known here, so a
// cluster server matches by name alone (or by the namespace its admin
// reference gives).
func usesSecret(srv Server, secret client.Object) bool {
	cm := srv.GetCommon()
	ref := cm.AdminCredentialsSecretRef
	if named(secret, cmp.Or(srv.GetNamespace(), ref.Namespace), ref.Name) {
		return true
	}
	return cm.TLS != nil && cm.TLS.CABundle != nil && cm.TLS.CABundle.SecretRef != nil &&
		named(secret, srv.GetNamespace(), cm.TLS.CABundle.SecretRef.Name)
}

// usesConfigMap reports whether srv names the ConfigMap as its CA bundle.
func usesConfigMap(srv Server, cfgMap client.Object) bool {
	cm := srv.GetCommon()
	return cm.TLS != nil && cm.TLS.CABundle != nil && cm.TLS.CABundle.ConfigMapRef != nil &&
		named(cfgMap, srv.GetNamespace(), cm.TLS.CABundle.ConfigMapRef.Name)
}

// named reports whether obj is called name and lives in namespace ("" matches
// any).
func named(obj client.Object, namespace, name string) bool {
	return obj.GetName() == name && (namespace == "" || obj.GetNamespace() == namespace)
}
