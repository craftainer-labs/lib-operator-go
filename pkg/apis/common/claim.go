package common

import (
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// DefaultGracePeriod is rotationPolicy.gracePeriod when the field is unset.
const DefaultGracePeriod = time.Hour

// ServerRef names the Cluster<Kind>Server or <Kind>Server a claim uses. A
// namespaced server is always looked up in the claim's own namespace. The
// operator's own claim spec restricts Kind to its two server kinds.
//
// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="serverRef is immutable"
type ServerRef struct {
	// Kind is the server kind.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=63
	Kind string `json:"kind"`
	// Name is the server's name.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	Name string `json:"name"`
}

// ClaimCommon is the spec every claim kind embeds (the drafts' ClaimCommon).
type ClaimCommon struct {
	// ServerRef selects the server. It is immutable.
	ServerRef ServerRef `json:"serverRef"`
	// ManagementPolicies limits what the controller may do to the external
	// object (ADR 0002, section 5). The default is ["*"].
	//
	// +kubebuilder:default={"*"}
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=5
	// +kubebuilder:validation:XValidation:rule="!('*' in self) || self.size() == 1",message="'*' must stand alone"
	// +kubebuilder:validation:XValidation:rule="self == ['*'] || 'Observe' in self",message="an explicit list must contain Observe"
	// +listType=set
	// +optional
	ManagementPolicies []ManagementPolicy `json:"managementPolicies,omitempty"`
}

// DataClaim is what a data kind (Database, Bucket) adds to ClaimCommon.
type DataClaim struct {
	// DeletionPolicy overrides the operator's DEFAULT_DATA_DELETION_POLICY for
	// this object. It has no schema default: the default is pinned into
	// status.effectiveDeletionPolicy at the first reconcile.
	//
	// +optional
	DeletionPolicy DeletionPolicy `json:"deletionPolicy,omitempty"`
}

// BindingSpec configures the binding Secret of a credential kind.
type BindingSpec struct {
	// SecretName is the Secret's name; the default is the claim's name. It is
	// immutable.
	//
	// +kubebuilder:validation:MaxLength=253
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9.]*[a-z0-9])?$`
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="binding.secretName is immutable"
	// +optional
	SecretName string `json:"secretName,omitempty"`
}

// Credentials is what a credential-bearing kind adds to ClaimCommon. The
// operator's spec also carries the CEL rule
// `!has(self.rotationPolicy) || self.managementPolicies == ['*'] ||
// 'Update' in self.managementPolicies` (rotation needs Update); it has to see
// both fields, so it cannot sit on this type.
type Credentials struct {
	// Binding configures the binding Secret.
	// +optional
	Binding BindingSpec `json:"binding,omitempty"`
	// RotationPolicy turns on credential rotation.
	// +optional
	RotationPolicy *RotationPolicy `json:"rotationPolicy,omitempty"`
}

// RotationPolicy is spec.rotationPolicy (ADR 0002, section 8).
type RotationPolicy struct {
	// Interval rotates on a schedule; unset means only on the
	// craftainer.dev/rotate annotation. Minimum 1h, maximum 8760h.
	//
	// +kubebuilder:validation:Type=string
	// +kubebuilder:validation:MaxLength=32
	// +kubebuilder:validation:XValidation:rule="duration(self) >= duration('1h') && duration(self) <= duration('8760h')",message="interval must be between 1h and 8760h"
	// +optional
	Interval *metav1.Duration `json:"interval,omitempty"`
	// GracePeriod is how long the previous generation stays valid. The default
	// is 1h; minimum 5m, maximum 168h.
	//
	// +kubebuilder:validation:Type=string
	// +kubebuilder:validation:MaxLength=32
	// +kubebuilder:default="1h"
	// +kubebuilder:validation:XValidation:rule="duration(self) >= duration('5m') && duration(self) <= duration('168h')",message="gracePeriod must be between 5m and 168h"
	// +optional
	GracePeriod *metav1.Duration `json:"gracePeriod,omitempty"`
}

// IntervalOrZero returns Interval, or zero when the policy is nil or has none.
func (p *RotationPolicy) IntervalOrZero() time.Duration {
	if p == nil || p.Interval == nil {
		return 0
	}
	return p.Interval.Duration
}

// GracePeriodOrDefault returns GracePeriod, or DefaultGracePeriod when unset.
func (p *RotationPolicy) GracePeriodOrDefault() time.Duration {
	if p == nil || p.GracePeriod == nil {
		return DefaultGracePeriod
	}
	return p.GracePeriod.Duration
}

// ClaimStatus is the status every claim kind embeds (ADR 0002, section 3).
type ClaimStatus struct {
	// ObservedGeneration is the metadata.generation last reconciled.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
	// Conditions holds Ready, Synced and ServerReachable.
	//
	// +listType=map
	// +listMapKey=type
	// +kubebuilder:validation:MaxItems=8
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
	// ExternalName is the name derived by naming.Tenant.
	// +kubebuilder:validation:MaxLength=255
	// +optional
	ExternalName string `json:"externalName,omitempty"`
	// ExternalID is the backend-assigned id, where there is one.
	// +kubebuilder:validation:MaxLength=255
	// +optional
	ExternalID string `json:"externalId,omitempty"`
	// LastReconcileTime is when the controller last reconciled the claim.
	// +optional
	LastReconcileTime *metav1.Time `json:"lastReconcileTime,omitempty"`
	// LateInitialized holds the values recorded under LateInitialize.
	//
	// +kubebuilder:validation:MaxProperties=20
	// +kubebuilder:validation:XValidation:rule="self.all(k, size(self[k]) <= 255)",message="values are at most 255 characters"
	// +optional
	LateInitialized map[string]string `json:"lateInitialized,omitempty"`
}

// DataClaimStatus is what a data kind adds to ClaimStatus.
type DataClaimStatus struct {
	// EffectiveDeletionPolicy is the default pinned at the first reconcile.
	// Deletion honours spec.deletionPolicy when set, otherwise this field.
	// +optional
	EffectiveDeletionPolicy DeletionPolicy `json:"effectiveDeletionPolicy,omitempty"`
	// DeletionPolicySource says where the pinned value came from.
	// +optional
	DeletionPolicySource DeletionPolicySource `json:"deletionPolicySource,omitempty"`
}

// BindingStatus names the binding Secret.
type BindingStatus struct {
	// Name is the Secret's name; its presence makes the claim a Provisioned
	// Service.
	// +kubebuilder:validation:MaxLength=253
	// +optional
	Name string `json:"name,omitempty"`
}

// CredentialsStatus is what a credential-bearing kind adds to ClaimStatus.
type CredentialsStatus struct {
	// Binding names the binding Secret.
	// +optional
	Binding *BindingStatus `json:"binding,omitempty"`
	// Rotation is the persisted rotation state.
	// +optional
	Rotation *RotationStatus `json:"rotation,omitempty"`
}

// RotationStatus is status.rotation, written by the framework.
type RotationStatus struct {
	// Active is the generation the binding Secret holds; empty before the first
	// rotation.
	// +kubebuilder:validation:MaxLength=128
	// +optional
	Active string `json:"active,omitempty"`
	// Previous is the generation awaiting retirement.
	// +kubebuilder:validation:MaxLength=128
	// +optional
	Previous string `json:"previous,omitempty"`
	// Sequence counts completed RotateBegin calls.
	// +optional
	Sequence int64 `json:"sequence,omitempty"`
	// RetireAfter is the end of the grace period. A grace period is in force
	// while it or Previous is set.
	// +optional
	RetireAfter *metav1.Time `json:"retireAfter,omitempty"`
	// LastRotated is when the last RotateBegin completed.
	// +optional
	LastRotated *metav1.Time `json:"lastRotated,omitempty"`
	// LastTrigger is the last handled value of the craftainer.dev/rotate
	// annotation.
	// +kubebuilder:validation:MaxLength=253
	// +optional
	LastTrigger string `json:"lastTrigger,omitempty"`
}
