package probe

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

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
	msgNotResolvable  = "the connection settings of the server could not be read"
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

// Options configure Reconcile. The zero value is usable for a namespaced kind.
type Options struct {
	// OperatorNamespace is the namespace of a cluster server's admin Secret
	// and CA bundle (server.WithOperatorNamespace); empty falls back to
	// POD_NAMESPACE.
	OperatorNamespace string
	// DefaultInterval is the probe interval of a server that sets no
	// healthCheckInterval; default DefaultInterval. A server's own value is
	// bounded to 10s..1h by the CRD and by server.HealthCheckInterval.
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
	// Now defaults to time.Now; Jitter returns a number in [0, 1) and defaults
	// to a crypto/rand draw. Both exist for tests.
	Now    func() time.Time
	Jitter func() float64
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
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Jitter == nil {
		o.Jitter = cryptoJitter
	}
	return o
}

func cryptoJitter() float64 {
	var b [8]byte
	_, _ = rand.Read(b[:]) // never fails since Go 1.24
	return float64(binary.BigEndian.Uint64(b[:])>>11) / (1 << 53)
}

// Reconcile probes srv once and records the result in its status. srv must be
// the freshly read object.
//
//   - A server that is being deleted is left alone.
//   - The connection settings come from server.ConnectionFor. A server whose
//     admin Secret is in a forbidden namespace gets Ready=False and
//     ServerReachable=False, reason AdminSecretNamespaceForbidden, and is
//     requeued at the interval without an error (editing the server triggers
//     it). Settings that are missing or invalid (a missing Secret or
//     ConfigMap, an invalid proxy URL or CA reference, an unset operator
//     namespace) get Ready=False, ServerReachable=Unknown, reason
//     InvalidServer, and are returned. Any other failure (an API server error,
//     say) is returned alone and leaves the status as it was.
//   - A probe error sets Ready=False and ServerReachable=False, reason
//     ServerUnreachable, keeps the last known serverVersion and is returned,
//     so controller-runtime backs off. Success sets both True (reason
//     Available), updates serverVersion and requeues after the interval plus
//     up to 10% jitter.
//
// The status is written only if something changed (see
// Options.RefreshInterval). Every condition carries the server's generation,
// and status.observedGeneration is set with them.
func Reconcile(ctx context.Context, c client.Client, srv Server, p Prober, opts Options) (ctrl.Result, error) {
	if !srv.GetDeletionTimestamp().IsZero() {
		return ctrl.Result{}, nil
	}
	opts = opts.withDefaults()
	interval := server.HealthCheckInterval(srv.GetCommon().HealthCheckInterval, opts.DefaultInterval)
	every := ctrl.Result{RequeueAfter: interval + time.Duration(float64(interval)*jitterFraction*opts.Jitter())}
	st, gen := srv.GetServerStatus(), srv.GetGeneration()
	name := srv.GetName()

	conn, err := server.ConnectionFor(ctx, c, srv, server.WithOperatorNamespace(opts.OperatorNamespace))
	if err != nil {
		if !misconfigured(err) { // an API hiccup says nothing about the server
			return ctrl.Result{}, fmt.Errorf("server %q: %w", name, err)
		}
		changed := observe(st, gen)
		if errors.Is(err, server.ErrAdminSecretNamespaceForbidden) {
			changed = setConditions(st, gen, metav1.ConditionFalse, metav1.ConditionFalse,
				conditions.AdminSecretNamespaceForbidden, msgForbidden, msgForbidden) || changed
			log.FromContext(ctx).Error(err, "server not probed", "server", name)
			return every, save(ctx, c, srv, changed)
		}
		changed = setConditions(st, gen, metav1.ConditionUnknown, metav1.ConditionFalse,
			conditions.InvalidServer, msgNotResolvable, msgNotResolvedRdy) || changed
		return ctrl.Result{}, errors.Join(fmt.Errorf("server %q: %w", name, err), save(ctx, c, srv, changed))
	}

	pctx, cancel := context.WithTimeout(ctx, opts.Timeout)
	defer cancel()
	version, perr := p.Probe(pctx, srv, conn)
	changed := observe(st, gen)
	if perr != nil {
		changed = setConditions(st, gen, metav1.ConditionFalse, metav1.ConditionFalse,
			conditions.ServerUnreachable, msgUnreachable, msgNotReady) || changed
	} else {
		changed = setConditions(st, gen, metav1.ConditionTrue, metav1.ConditionTrue,
			conditions.Available, msgReachable, msgReady) || changed
		if v := cleanVersion(version); v != st.ServerVersion {
			st.ServerVersion, changed = v, true
		}
	}
	now := opts.Now()
	if changed || st.LastProbeTime == nil || now.Sub(st.LastProbeTime.Time) >= opts.RefreshInterval {
		st.LastProbeTime, changed = &metav1.Time{Time: now}, true
	}
	serr := save(ctx, c, srv, changed)
	if perr != nil {
		return ctrl.Result{}, errors.Join(fmt.Errorf("probe server %q: %w", name, perr), serr)
	}
	return every, serr
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

// save writes the status if changed. A server that is gone is not an error.
func save(ctx context.Context, c client.Client, srv Server, changed bool) error {
	if !changed {
		return nil
	}
	if err := c.Status().Update(ctx, srv); err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("update status of server %q: %w", srv.GetName(), err)
	}
	return nil
}

// Predicates is the event filter of a server controller: creation and a spec
// change (metadata.generation) trigger a probe; the helper's own status writes
// do not.
func Predicates() predicate.Predicate { return predicate.GenerationChangedPredicate{} }

// Reconciler is a controller-runtime reconciler of one server kind.
type Reconciler struct {
	Client client.Client
	// New returns an empty object of the kind, for example
	// func() probe.Server { return &v1alpha1.DatabaseServer{} }. It serves a
	// cluster-scoped and a namespaced kind alike.
	New     func() Server
	Prober  Prober
	Options Options
}

// Reconcile reads the server named by req and calls Reconcile. A server that
// no longer exists is not an error.
func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	srv := r.New()
	if err := r.Client.Get(ctx, req.NamespacedName, srv); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	return Reconcile(ctx, r.Client, srv, r.Prober, r.Options)
}

// SetupWithManager registers the reconciler as the controller of the kind New
// returns, filtered by Predicates. The controller is named after the kind, so
// an operator with several server kinds calls it once for each.
func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).For(r.New()).WithEventFilter(Predicates()).Complete(r)
}
