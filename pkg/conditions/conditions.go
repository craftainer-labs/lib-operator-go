package conditions

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"k8s.io/apimachinery/pkg/api/meta"
)

// Condition types set on every claim and server kind. All three are set on the
// first reconcile, each with observedGeneration.
const (
	// Ready is True when the external object exists, is up to date and its
	// binding Secret is written.
	Ready = "Ready"
	// Synced is True when the last attempt to make the external object match
	// the spec succeeded.
	Synced = "Synced"
	// ServerReachable is True when the referenced server resolved and answered.
	ServerReachable = "ServerReachable"
)

// Condition reasons (ADR 0002, section 3).
const (
	Available                     = "Available"
	Creating                      = "Creating"
	Updating                      = "Updating"
	Deleting                      = "Deleting"
	ReconcileSuccess              = "ReconcileSuccess"
	ReconcileError                = "ReconcileError"
	ServerUnreachable             = "ServerUnreachable"
	ServerNotFound                = "ServerNotFound"
	NamespaceNotAllowed           = "NamespaceNotAllowed"
	AdminSecretNamespaceForbidden = "AdminSecretNamespaceForbidden"
	NameCollision                 = "NameCollision"
	ObserveOnly                   = "ObserveOnly"
	ManagementPolicyBlocked       = "ManagementPolicyBlocked"
	RotationUnsupported           = "RotationUnsupported"
	Rotating                      = "Rotating"
	// Reconciling is the reason of a condition that was set to Unknown on the
	// first reconcile and not evaluated yet.
	Reconciling = "Reconciling"
)

// Event reasons that are not condition reasons (ADR 0002, section 11). The
// condition reasons above are also used as Event reasons where both exist.
const (
	EventCreated                = "Created"
	EventUpdated                = "Updated"
	EventDeleted                = "Deleted"
	EventObserved               = "Observed"
	EventRetained               = "Retained"
	EventDeletionPolicyResolved = "DeletionPolicyResolved"
	EventDeletionPolicyRepinned = "DeletionPolicyRepinned"
	EventExternalObjectOrphaned = "ExternalObjectOrphaned"
	EventRotationStarted        = "RotationStarted"
	EventRotationFinished       = "RotationFinished"
	EventRotatedWithoutOverlap  = "RotatedWithoutOverlap"
	EventRotationFailed         = "RotationFailed"
	EventInvalidPollInterval    = "InvalidPollInterval"
)

// MaxMessageLength is the longest condition message Set keeps; the API allows
// 32768 bytes, and a status that large is unreadable.
const MaxMessageLength = 1024

// Set sets the condition on conds with the object's generation, keeping
// lastTransitionTime unless the status changed, and reports whether anything
// changed. A message longer than MaxMessageLength is cut.
func Set(conds *[]metav1.Condition, generation int64, typ string, status metav1.ConditionStatus, reason, message string) bool {
	if len(message) > MaxMessageLength {
		message = message[:MaxMessageLength]
	}
	return meta.SetStatusCondition(conds, metav1.Condition{
		Type:               typ,
		Status:             status,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: generation,
	})
}

// Ensure adds Ready, Synced and ServerReachable as Unknown (reason Reconciling)
// when they are missing, so a missing condition always means the operator is
// not running. Existing conditions are left as they are.
func Ensure(conds *[]metav1.Condition, generation int64) {
	for _, typ := range []string{Ready, Synced, ServerReachable} {
		if meta.FindStatusCondition(*conds, typ) == nil {
			Set(conds, generation, typ, metav1.ConditionUnknown, Reconciling, "not reconciled yet")
		}
	}
}
