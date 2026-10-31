// Package lifecycle is the reconcile framework every operator controller calls:
// finalizer, deletionPolicy, managementPolicies, drift polling, two-phase
// credential rotation and the Ready, Synced and ServerReachable conditions
// (ADR 0002). It holds the interfaces below; NO1b implements Reconcile.
package lifecycle

import (
	"context"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/craftainer-labs/lib-operator-go/pkg/binding"
	"github.com/craftainer-labs/lib-operator-go/pkg/server"
)

// DeletionPolicy decides whether the external object outlives the claim.
type DeletionPolicy string

// ManagementPolicy limits what the operator may do to the external object.
type ManagementPolicy string

// Values of DeletionPolicy and ManagementPolicy.
const (
	DeletionDelete DeletionPolicy = "Delete"
	DeletionRetain DeletionPolicy = "Retain"

	// ManagementAll is "*": create, update and delete.
	ManagementAll ManagementPolicy = "*"
	// ManagementObserve adopts an existing object and never writes to it.
	ManagementObserve ManagementPolicy = "Observe"
)

// Observation is what an External reports about the external object.
type Observation[O any] struct {
	Exists, UpToDate bool
	Current          O
}

// External is the backend-specific part of a controller. Every method is
// idempotent: the framework retries after any error and after a crash.
type External[S, O any] interface {
	Observe(ctx context.Context, spec S) (Observation[O], error)
	Create(ctx context.Context, spec S) (binding.Data, error)
	Update(ctx context.Context, spec S, cur O) (binding.Data, error)
	Delete(ctx context.Context, cur O) error
}

// Generation names one credential generation on the backend: "a" or "b" for a
// two-slot backend, a service-account or client id for the others. The
// framework stores it in the claim's status; the External chooses it.
type Generation string

// RotationState is what the framework persisted in status.rotation.
type RotationState struct {
	// Active is the generation the binding Secret holds; empty before the
	// first rotation.
	Active Generation
	// Previous is still valid on the backend and awaits RotateFinish; empty
	// outside a grace period.
	Previous Generation
	// Sequence counts completed RotateBegin calls and lets an External derive
	// a deterministic next generation, so a retry after a crash issues the
	// same one instead of leaking another.
	Sequence int64
}

// Rotator is implemented by an External whose backend can rotate credentials.
// It replaces the single Rotate method of the first draft of I1 (NR4 point 3).
// An External that is not a Rotator cannot rotate: a claim that sets
// rotationPolicy gets Synced=False, reason RotationUnsupported.
type Rotator[S, O any] interface {
	External[S, O]
	// RotateBegin issues a new generation and leaves the active one valid. It
	// returns the new generation's binding data and its Generation. It must be
	// idempotent for the same RotationState.
	RotateBegin(ctx context.Context, spec S, cur O, st RotationState) (binding.Data, Generation, error)
	// RotateFinish retires st.Previous once the grace period has elapsed. It
	// must succeed when st.Previous is already gone.
	RotateFinish(ctx context.Context, spec S, cur O, st RotationState) error
}

// RotationPolicy is the claim's spec.rotationPolicy, read by the framework.
type RotationPolicy struct {
	// Interval rotates on a schedule; zero means only on the trigger annotation.
	Interval time.Duration
	// GracePeriod is how long Previous stays valid after RotateBegin.
	GracePeriod time.Duration
}

// RotationStatus is the claim's status.rotation, written by the framework.
type RotationStatus struct {
	State       RotationState
	RetireAfter *metav1.Time
	LastRotated *metav1.Time
	// LastTrigger is the last handled value of the craftainer.dev/rotate
	// annotation.
	LastTrigger string
}

// Claim is implemented by every claim kind's API type.
type Claim interface {
	client.Object
	GetServerRef() server.Ref
	GetDeletionPolicy() DeletionPolicy
	GetManagementPolicies() []ManagementPolicy
	GetConditions() *[]metav1.Condition
	SetBindingName(string)
	// GetRotationPolicy returns nil for a kind that has no rotationPolicy.
	GetRotationPolicy() *RotationPolicy
	GetRotationStatus() RotationStatus
	SetRotationStatus(RotationStatus)
}

// Options configure Reconcile for one controller.
type Options struct {
	// BindingType is <type> in servicebinding.io/<type>; Provider is the
	// provider key.
	BindingType, Provider string
	// FinalizerName defaults to craftainer.dev/finalizer.
	FinalizerName string
	// DefaultPollInterval defaults to 10m; the craftainer.dev/poll-interval
	// annotation overrides it per resource.
	DefaultPollInterval time.Duration
	Recorder            record.EventRecorder
}

// ReconcileFunc is the signature of Reconcile[S, O], which NO1b implements as
// a generic function and then removes this type.
type ReconcileFunc[S, O any] func(ctx context.Context, c client.Client, claim Claim,
	spec S, ext External[S, O], opts Options) (ctrl.Result, error)
