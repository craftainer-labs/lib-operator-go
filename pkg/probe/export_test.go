package probe

import (
	"context"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

// SetClock replaces the clock and the jitter source of r.
func (r *Reconciler) SetClock(now func() time.Time, jitter func() float64) {
	r.now, r.jitter = now, jitter
}

// ProbeServer probes srv, an already read server.
func (r *Reconciler) ProbeServer(ctx context.Context, srv Server) (ctrl.Result, error) {
	return r.probeServer(ctx, srv)
}

// Dependents returns the servers that use obj, a Secret or a ConfigMap.
func (r *Reconciler) Dependents(ctx context.Context, obj client.Object) ([]reconcile.Request, error) {
	newList, err := listFor(r.Client.Scheme(), r.New())
	if err != nil {
		return nil, err
	}
	uses := usesConfigMap
	if _, ok := obj.(*corev1.Secret); ok {
		uses = usesSecret
	}
	return r.dependents(ctx, newList(), obj, uses), nil
}

// ListFor reports whether the scheme can list the kind of obj.
func ListFor(s *runtime.Scheme, obj Server) error {
	_, err := listFor(s, obj)
	return err
}

// PredicatesForTest returns the event filter of the server watch.
func PredicatesForTest() predicate.Predicate { return predicates() }
