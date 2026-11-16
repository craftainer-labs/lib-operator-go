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
	var kind client.Object
	_, isSecret := obj.(*corev1.Secret)
	var uses func(client.Object, client.Object) bool
	if r.NewCloud != nil {
		kind, uses = r.NewCloud(), cloudUses(usesCloudConfigMap)
		if isSecret {
			uses = cloudUses(usesCloudSecret)
		}
	} else {
		kind, uses = r.New(), serverUses(usesConfigMap)
		if isSecret {
			uses = serverUses(usesSecret)
		}
	}
	newList, err := listFor(r.Client.Scheme(), kind)
	if err != nil {
		return nil, err
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

// ProbeCloudServer probes srv, an already read cloud server.
func (r *Reconciler) ProbeCloudServer(ctx context.Context, srv CloudServer) (ctrl.Result, error) {
	return r.probeCloudServer(ctx, srv)
}

// CloudUsesForTest and ServerUsesForTest run the adapters of dependents on any
// object.
func CloudUsesForTest(srv, obj client.Object) bool { return cloudUses(usesCloudSecret)(srv, obj) }

// ServerUsesForTest is CloudUsesForTest for a Server.
func ServerUsesForTest(srv, obj client.Object) bool { return serverUses(usesSecret)(srv, obj) }
