package lifecycle

import (
	"context"
	"errors"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/craftainer-labs/lib-operator-go/pkg/apis/common"
	"github.com/craftainer-labs/lib-operator-go/pkg/binding"
	"github.com/craftainer-labs/lib-operator-go/pkg/server"
)

// Shared finalizer and annotations (ADR 0002, section 1).
const (
	// DefaultFinalizer holds a claim until its external object is handled.
	DefaultFinalizer = "craftainer.dev/finalizer"
	// AnnotationPollInterval overrides the drift poll interval (a Go duration,
	// 30s to 24h).
	AnnotationPollInterval = "craftainer.dev/poll-interval"
	// AnnotationRotate triggers one rotation when its value differs from
	// status.rotation.lastTrigger.
	AnnotationRotate = "craftainer.dev/rotate"
	// AnnotationPaused set to "true" stops spec changes, drift correction and
	// rotation, but not deletion.
	AnnotationPaused = "craftainer.dev/paused"
)

// Observation is what an External reports about the external object.
type Observation[O any] struct {
	// Exists is false when the external object is not there.
	Exists bool
	// UpToDate is true when the object matches the spec.
	UpToDate bool
	// Current is the observed object, handed back to Update, Delete and the
	// Rotator methods.
	Current O
}

// External is the backend-specific part of a controller. Every method is
// idempotent: the framework retries after any error and after a crash. Errors
// must not contain credentials: Reconcile copies their text into conditions and
// Events. Wrap ErrUnreachable for a backend that cannot be reached and
// ErrNameCollision for an object whose ownership marker names another claim.
type External[S, O any] interface {
	// Observe reads the external object; it never changes it.
	Observe(ctx context.Context, spec S) (Observation[O], error)
	// Create makes the external object. The returned data goes to the binding
	// Secret of a credential kind; a data kind returns nil.
	Create(ctx context.Context, spec S) (binding.Data, error)
	// Update makes the object match the spec. The returned data, if any, goes to
	// the binding Secret.
	Update(ctx context.Context, spec S, cur O) (binding.Data, error)
	// Delete removes the object.
	Delete(ctx context.Context, cur O) error
}

// Generation names one credential generation on the backend: "a" or "b" for a
// two-slot backend, a service-account or client id for the others. The
// External chooses it; the framework stores it in status.rotation.
type Generation string

// RotationState is what the framework persisted in status.rotation.
type RotationState struct {
	// Active is the generation the binding Secret holds; empty before the
	// first rotation.
	Active Generation
	// Previous is still valid on the backend and awaits RotateFinish; empty
	// outside a grace period (and during the first one, whose old generation is
	// the one Create issued).
	Previous Generation
	// Sequence counts completed RotateBegin calls and lets an External derive
	// a deterministic next generation, so a retry after a crash issues the
	// same one instead of leaking another.
	Sequence int64
}

// Rotator is implemented by an External whose backend can rotate credentials
// (ADR 0002, section 8). An External that is not a Rotator cannot rotate: a
// claim that sets rotationPolicy gets Synced=False, reason RotationUnsupported.
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

// HardCutRotator is implemented by a Rotator for a backend with no overlap:
// RotateBegin is the hard cut and RotateFinish a no-op. When HardCut returns
// true, Reconcile starts no grace period and emits the Event
// RotatedWithoutOverlap instead of RotationStarted.
type HardCutRotator interface {
	HardCut() bool
}

// LateInitializer is implemented by an External that has backend-assigned
// values to record under the LateInitialize management policy. The returned
// map replaces status.lateInitialized. An External without it accepts the
// policy and does nothing.
type LateInitializer[S, O any] interface {
	LateInitialize(ctx context.Context, spec S, cur O) (map[string]string, error)
}

// Errors an External wraps to tell Reconcile what kind of failure it was.
var (
	// ErrUnreachable marks a backend that could not be reached: the claim gets
	// ServerReachable=False and reason ServerUnreachable.
	ErrUnreachable = errors.New("server unreachable")
	// ErrNameCollision marks an external object, or a binding Secret, owned by
	// another claim: the claim gets reason NameCollision and is not retried with
	// backoff.
	ErrNameCollision = binding.ErrNameCollision
	// ErrNoBindingData is the failure of a credential kind whose Create or Update
	// returned no binding data: the credential would never reach the binding Secret.
	ErrNoBindingData = errors.New("create or update of a credential kind returned no binding data")
)

// Claim is implemented by every claim kind's API type. The accessors reach the
// embedded pkg/apis/common structs of the kind's spec and status.
type Claim interface {
	client.Object
	// GetServerRef returns spec.serverRef.
	GetServerRef() server.Ref
	// GetDeletionPolicy returns spec.deletionPolicy; empty when unset and always
	// empty for a credential kind.
	GetDeletionPolicy() common.DeletionPolicy
	// GetManagementPolicies returns spec.managementPolicies.
	GetManagementPolicies() []common.ManagementPolicy
	// GetConditions returns a pointer to status.conditions.
	GetConditions() *[]metav1.Condition
	// GetBindingSecretName returns spec.binding.secretName, or the claim's name
	// when it is unset; a data kind returns "".
	GetBindingSecretName() string
	// SetBindingName sets status.binding.name.
	SetBindingName(string)
	// GetExternalName and SetExternalName access status.externalName; Observe
	// goes by it, never by re-deriving (ADR 0002, section 3).
	GetExternalName() string
	SetExternalName(string)
	// GetExternalID and SetExternalID access status.externalId, a
	// backend-assigned id (an OIDC client_id, an S3 access key id); empty for
	// kinds that have none.
	GetExternalID() string
	SetExternalID(string)
	// SetObservedGeneration sets status.observedGeneration.
	SetObservedGeneration(int64)
	// SetLastReconcileTime sets status.lastReconcileTime.
	SetLastReconcileTime(metav1.Time)
	// GetEffectiveDeletionPolicy reads the pinned default and
	// SetEffectiveDeletionPolicy writes it with its source. A credential kind
	// has none: it returns "" and ignores the setter.
	GetEffectiveDeletionPolicy() common.DeletionPolicy
	SetEffectiveDeletionPolicy(common.DeletionPolicy, common.DeletionPolicySource)
	// SetLateInitialized sets status.lateInitialized.
	SetLateInitialized(map[string]string)
	// GetRotationPolicy returns nil for a kind that has no rotationPolicy.
	GetRotationPolicy() *common.RotationPolicy
	// GetRotationStatus and SetRotationStatus access status.rotation.
	GetRotationStatus() common.RotationStatus
	SetRotationStatus(common.RotationStatus)
}
