package probe

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"maps"
	"math/rand/v2"
	"slices"
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
	// MaxDiscovered and MaxDiscoveredLength are the schema's bounds of
	// ServerStatus.Discovered: at most this many entries, each value at most
	// this many bytes.
	MaxDiscovered       = 10
	MaxDiscoveredLength = 512
	// MaxDiscoveredKeyLength is the longest key kept, in bytes.
	MaxDiscoveredKeyLength = 63

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

	msgCredsForbidden   = "credentials.secretRef names a Secret in a namespace this server may not use"
	msgCredsUnavailable = "the cloud credentials of the server are unavailable"
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

// CloudServer is a cloud server kind (server.CloudServer, the kind embeds
// common.CloudServerCommon) that has a status the helper can write.
type CloudServer interface {
	server.CloudServer
	// GetServerStatus returns the kind's status field.
	GetServerStatus() *common.ServerStatus
}

// CloudResult is what a CloudProber reads from a cloud server.
type CloudResult struct {
	// Version is the version the backend reported, "" if it has none.
	Version string
	// Discovered becomes ServerStatus.Discovered: endpoint, port, resourceId,
	// accountId, region. It is cut to the schema (MaxDiscovered entries, the
	// first by key, each value at most MaxDiscoveredLength bytes of valid
	// UTF-8), and a nil or empty map clears the field. A failed probe or an
	// unreadable connection leaves the last known value, as it does for the
	// version; the Ready condition says the server is not usable.
	Discovered map[string]string
}

// CloudProber is the Prober of a cloud server, which has no admin Secret to
// build a Connection from: it receives the CloudConnection (with the cloud
// credentials Secret's data for cloud.Egress, and the admin Secret's data if
// the server names one) and returns what it read. Probe and ProbeCloud are
// separate interfaces, so every existing Prober keeps working unchanged.
type CloudProber interface {
	ProbeCloud(ctx context.Context, srv server.CloudServer, conn server.CloudConnection) (CloudResult, error)
}

// CloudProberFunc adapts a function to CloudProber.
type CloudProberFunc func(ctx context.Context, srv server.CloudServer, conn server.CloudConnection) (CloudResult, error)

// ProbeCloud calls f.
func (f CloudProberFunc) ProbeCloud(ctx context.Context, srv server.CloudServer, conn server.CloudConnection) (CloudResult, error) {
	return f(ctx, srv, conn)
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
	// NewCloud and CloudProber replace New and Prober for a cloud server kind
	// (one that embeds common.CloudServerCommon, so it may have no admin
	// Secret). When NewCloud is set the reconciler reads the connection with
	// server.ConnectionForCloud, probes with CloudProber and writes
	// ServerStatus.Discovered with the version; New and Prober are not used.
	// Everything else works as for New, with a credentials Secret in place of
	// (or beside) the admin Secret: one that is in a forbidden namespace gives
	// Ready=False, ServerReachable=False, reason CredentialsNamespaceForbidden;
	// one that is missing gives Ready=False, ServerReachable=Unknown, reason
	// CredentialsUnavailable. NewCloud needs CloudProber, and replaces New:
	// SetupWithManager fails if CloudProber is nil or New is set too.
	NewCloud    func() CloudServer
	CloudProber CloudProber
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
	if r.NewCloud != nil {
		srv := r.NewCloud()
		if err := r.Client.Get(ctx, req.NamespacedName, srv); err != nil {
			return ctrl.Result{}, client.IgnoreNotFound(err)
		}
		return r.probeCloudServer(ctx, srv)
	}
	srv := r.New()
	if err := r.Client.Get(ctx, req.NamespacedName, srv); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	return r.probeServer(ctx, srv)
}

func (r *Reconciler) probeServer(ctx context.Context, srv Server) (ctrl.Result, error) {
	o := r.Options.withDefaults()
	var conn server.Connection
	return r.run(ctx, target{
		obj: srv, st: srv.GetServerStatus(),
		connect: func() (time.Duration, error) {
			var err error
			conn, err = server.ConnectionFor(ctx, r.Client, srv,
				server.WithOperatorNamespace(o.OperatorNamespace), server.WithHealthCheckInterval(o.DefaultInterval))
			return conn.HealthCheckInterval, err
		},
		probe: func(pctx context.Context) (CloudResult, error) {
			v, err := r.Prober.Probe(pctx, srv, conn)
			return CloudResult{Version: v}, err
		},
	})
}

func (r *Reconciler) probeCloudServer(ctx context.Context, srv CloudServer) (ctrl.Result, error) {
	o := r.Options.withDefaults()
	var conn server.CloudConnection
	return r.run(ctx, target{
		obj: srv, st: srv.GetServerStatus(), discovered: true,
		connect: func() (time.Duration, error) {
			var err error
			conn, err = server.ConnectionForCloud(ctx, r.Client, srv,
				server.WithOperatorNamespace(o.OperatorNamespace), server.WithHealthCheckInterval(o.DefaultInterval))
			return conn.HealthCheckInterval, err
		},
		probe: func(pctx context.Context) (CloudResult, error) {
			return r.CloudProber.ProbeCloud(pctx, srv, conn)
		},
	})
}

// target is one server to probe, whatever its kind: the object and its status,
// how to read its connection (returning the interval, which is set even on an
// error) and how to probe it. discovered says the probe's CloudResult.Discovered
// is written to the status; a self-hosted server never touches the field.
type target struct {
	obj        client.Object
	st         *common.ServerStatus
	discovered bool
	connect    func() (time.Duration, error)
	probe      func(ctx context.Context) (CloudResult, error)
}

func (r *Reconciler) run(ctx context.Context, t target) (ctrl.Result, error) {
	srv, st := t.obj, t.st
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
	gen := srv.GetGeneration()
	name := srv.GetName()
	base := srv.DeepCopyObject().(client.Object) // the unmodified object the patch is computed against

	interval, err := t.connect()
	every := ctrl.Result{RequeueAfter: interval + time.Duration(float64(interval)*jitterFraction*jitter())}
	if err != nil {
		if !misconfigured(err) { // an API hiccup says nothing about the server
			return ctrl.Result{}, fmt.Errorf("server %q: %w", name, err)
		}
		changed := observe(st, gen)
		reachable, reason, reachableMsg, readyMsg := metav1.ConditionUnknown, conditions.InvalidServer, msgNotResolvable, msgNotResolvedRdy
		switch {
		case errors.Is(err, server.ErrAdminSecretNamespaceForbidden):
			reachable, reason, reachableMsg, readyMsg = metav1.ConditionFalse, conditions.AdminSecretNamespaceForbidden, msgForbidden, msgForbidden
		case errors.Is(err, server.ErrCredentialsSecretNamespaceForbidden):
			reachable, reason, reachableMsg, readyMsg = metav1.ConditionFalse, conditions.CredentialsNamespaceForbidden, msgCredsForbidden, msgCredsForbidden
		case errors.Is(err, server.ErrCredentialsUnavailable):
			reason, reachableMsg = conditions.CredentialsUnavailable, msgCredsUnavailable
		}
		moved := setConditions(st, gen, reachable, metav1.ConditionFalse, reason, reachableMsg, readyMsg)
		logFailure(ctx, moved, err, "server not probed", name)
		return every, save(ctx, r.Client, srv, base, changed || moved)
	}

	pctx, cancel := context.WithTimeout(ctx, o.Timeout)
	defer cancel()
	res, perr := t.probe(pctx)
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
		if v := cleanVersion(res.Version); v != st.ServerVersion {
			st.ServerVersion, changed = v, true
		}
		if d := cleanDiscovered(res.Discovered); t.discovered && !maps.Equal(d, st.Discovered) {
			st.Discovered, changed = d, true
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
		errors.Is(err, server.ErrCredentialsSecretNamespaceForbidden) ||
		errors.Is(err, server.ErrCredentialsUnavailable) ||
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
func cleanVersion(v string) string { return truncate(strings.ToValidUTF8(v, ""), MaxVersionLength) }

// truncate cuts a valid UTF-8 string to at most n bytes at a character
// boundary.
func truncate(v string, n int) string {
	if len(v) <= n {
		return v
	}
	for !utf8.RuneStart(v[n]) {
		n--
	}
	return v[:n]
}

// cleanDiscovered makes a discovered map fit the schema: keys and values valid
// UTF-8 (a key at most MaxDiscoveredKeyLength bytes, a value
// MaxDiscoveredLength), an empty key dropped, and at most MaxDiscovered entries
// (the first by key, so the same input always gives the same output). An empty
// result is nil.
func cleanDiscovered(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, min(len(in), MaxDiscovered))
	for _, k := range slices.Sorted(maps.Keys(in)) {
		key := truncate(strings.ToValidUTF8(k, ""), MaxDiscoveredKeyLength)
		if _, taken := out[key]; taken || key == "" || len(out) >= MaxDiscovered {
			continue // the first key in order wins a collision after cleaning
		}
		out[key] = truncate(strings.ToValidUTF8(in[k], ""), MaxDiscoveredLength)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// save patches the status against base if changed. A merge patch has no
// resourceVersion precondition, so a concurrent write to another field never
// makes it fail. A server that is gone is not an error.
func save(ctx context.Context, c client.Client, srv, base client.Object, changed bool) error {
	if !changed {
		return nil
	}
	if err := c.Status().Patch(ctx, srv, client.MergeFrom(base)); err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("patch status of server %q: %w", srv.GetName(), err)
	}
	return nil
}

// predicates is the event filter of the server watch: creation and a spec
// change (metadata.generation) trigger a probe; the helper's own status writes
// do not. SetupWithManager applies it to the server only, never to the Secrets
// and ConfigMaps it also watches.
func predicates() predicate.Predicate { return predicate.GenerationChangedPredicate{} }

// SetupWithManager registers the reconciler as the controller of the kind New
// (or NewCloud) returns. It watches the servers (filtered by a generation predicate) and the Secrets and
// ConfigMaps they name (the admin Secret, the cloud credentials Secret, the CA bundle), so creating or
// fixing one probes the servers that use it at once. The operator needs get,
// list and watch on Secrets and ConfigMaps (its cached client does already)
// and get, list, watch and patch on the servers' status. The controller is
// named after the kind, so an operator with several server kinds calls it once
// for each.
func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	if r.NewCloud != nil && (r.CloudProber == nil || r.New != nil) {
		return errors.New("probe: NewCloud needs a CloudProber and replaces New")
	}
	var kind client.Object
	uses, usesCM := serverUses(usesSecret), serverUses(usesConfigMap)
	if r.NewCloud != nil {
		kind, uses, usesCM = r.NewCloud(), cloudUses(usesCloudSecret), cloudUses(usesCloudConfigMap)
	} else {
		kind = r.New()
	}
	newList, err := listFor(mgr.GetScheme(), kind)
	if err != nil {
		return err
	}
	mapper := func(uses func(client.Object, client.Object) bool) handler.EventHandler {
		return handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, obj client.Object) []reconcile.Request {
			return r.dependents(ctx, newList(), obj, uses)
		})
	}
	return ctrl.NewControllerManagedBy(mgr).
		For(kind, builder.WithPredicates(predicates())).
		Watches(&corev1.Secret{}, mapper(uses)).
		Watches(&corev1.ConfigMap{}, mapper(usesCM)).
		WithOptions(r.Controller).
		Complete(r)
}

// listFor returns a constructor of the list type of obj's kind, which the
// scheme must know.
func listFor(s *runtime.Scheme, obj client.Object) (func() client.ObjectList, error) {
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

// serverUses adapts a test on a Server to dependents; an item of another type
// uses nothing.
func serverUses(f func(Server, client.Object) bool) func(client.Object, client.Object) bool {
	return func(srv, obj client.Object) bool { s, ok := srv.(Server); return ok && f(s, obj) }
}

// cloudUses is serverUses for a CloudServer.
func cloudUses(f func(CloudServer, client.Object) bool) func(client.Object, client.Object) bool {
	return func(srv, obj client.Object) bool { s, ok := srv.(CloudServer); return ok && f(s, obj) }
}

// dependents returns the servers that use obj (a Secret or ConfigMap that was
// created, changed or deleted). It may name a server whose reference only
// matches by name, never misses one, and a spurious probe is cheap.
func (r *Reconciler) dependents(ctx context.Context, list client.ObjectList, obj client.Object, uses func(client.Object, client.Object) bool) []reconcile.Request {
	if err := r.Client.List(ctx, list); err != nil {
		log.FromContext(ctx).Error(err, "cannot list the servers that use a changed object", "object", client.ObjectKeyFromObject(obj))
		return nil
	}
	var out []reconcile.Request
	_ = meta.EachListItem(list, func(o runtime.Object) error { // cannot fail: list is a list and the callback returns nil
		if srv, ok := o.(client.Object); ok && uses(srv, obj) {
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

// usesCloudSecret is usesSecret for a cloud server: the admin Secret (when
// set), the credentials Secret and the CA bundle.
func usesCloudSecret(srv CloudServer, secret client.Object) bool {
	cm := srv.GetCloudCommon()
	if ref := cm.AdminCredentialsSecretRef; ref != nil && named(secret, cmp.Or(srv.GetNamespace(), ref.Namespace), ref.Name) {
		return true
	}
	if name, ns := srv.GetCredentialsSecret(); name != "" && named(secret, cmp.Or(srv.GetNamespace(), ns), name) {
		return true
	}
	return cm.TLS != nil && cm.TLS.CABundle != nil && cm.TLS.CABundle.SecretRef != nil &&
		named(secret, srv.GetNamespace(), cm.TLS.CABundle.SecretRef.Name)
}

// usesCloudConfigMap is usesConfigMap for a cloud server.
func usesCloudConfigMap(srv CloudServer, cfgMap client.Object) bool {
	cm := srv.GetCloudCommon()
	return cm.TLS != nil && cm.TLS.CABundle != nil && cm.TLS.CABundle.ConfigMapRef != nil &&
		named(cfgMap, srv.GetNamespace(), cm.TLS.CABundle.ConfigMapRef.Name)
}

// named reports whether obj is called name and lives in namespace ("" matches
// any).
func named(obj client.Object, namespace, name string) bool {
	return obj.GetName() == name && (namespace == "" || obj.GetNamespace() == namespace)
}
