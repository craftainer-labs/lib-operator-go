package testapi

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"

	"github.com/craftainer-labs/lib-operator-go/pkg/apis/common"
)

// WidgetSpec is the spec of a data claim.
type WidgetSpec struct {
	common.ClaimCommon `json:",inline"`
	common.DataClaim   `json:",inline"`

	// Size is a kind-specific field, so a test can change the spec.
	// +kubebuilder:validation:MaxLength=32
	// +optional
	Size string `json:"size,omitempty"`
}

// WidgetStatus is the status of a data claim.
type WidgetStatus struct {
	common.ClaimStatus     `json:",inline"`
	common.DataClaimStatus `json:",inline"`
}

// Widget is a data kind, like Database or Bucket.
//
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
type Widget struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   WidgetSpec   `json:"spec"`
	Status WidgetStatus `json:"status,omitempty"`
}

// WidgetList is a list of Widgets.
//
// +kubebuilder:object:root=true
type WidgetList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Widget `json:"items"`
}

// GadgetSpec is the spec of a credential claim.
//
// +kubebuilder:validation:XValidation:rule="!has(self.rotationPolicy) || self.managementPolicies == ['*'] || 'Update' in self.managementPolicies",message="rotationPolicy needs the Update management policy"
type GadgetSpec struct {
	common.ClaimCommon `json:",inline"`
	common.Credentials `json:",inline"`

	// Size is a kind-specific field, so a test can change the spec.
	// +kubebuilder:validation:MaxLength=32
	// +optional
	Size string `json:"size,omitempty"`
}

// GadgetStatus is the status of a credential claim.
type GadgetStatus struct {
	common.ClaimStatus       `json:",inline"`
	common.CredentialsStatus `json:",inline"`
}

// Gadget is a credential-bearing kind, like DatabaseRole or BucketAccess.
//
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
type Gadget struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   GadgetSpec   `json:"spec"`
	Status GadgetStatus `json:"status,omitempty"`
}

// GadgetList is a list of Gadgets.
//
// +kubebuilder:object:root=true
type GadgetList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Gadget `json:"items"`
}

// The methods below implement lifecycle.Claim.

// GetServerRef returns spec.serverRef.
func (w *Widget) GetServerRef() common.ServerRef { return w.Spec.ServerRef }

// GetDeletionPolicy returns spec.deletionPolicy.
func (w *Widget) GetDeletionPolicy() common.DeletionPolicy { return w.Spec.DeletionPolicy }

// GetManagementPolicies returns spec.managementPolicies.
func (w *Widget) GetManagementPolicies() []common.ManagementPolicy { return w.Spec.ManagementPolicies }

// GetConditions returns status.conditions.
func (w *Widget) GetConditions() *[]metav1.Condition { return &w.Status.Conditions }

// GetBindingSecretName returns "": a data kind has no binding.
func (w *Widget) GetBindingSecretName() string { return "" }

// SetBindingName does nothing: a data kind has no binding.
func (w *Widget) SetBindingName(string) {}

// GetExternalName returns status.externalName.
func (w *Widget) GetExternalName() string { return w.Status.ExternalName }

// SetExternalName sets status.externalName.
func (w *Widget) SetExternalName(v string) { w.Status.ExternalName = v }

// GetExternalID returns status.externalId.
func (w *Widget) GetExternalID() string { return w.Status.ExternalID }

// SetExternalID sets status.externalId.
func (w *Widget) SetExternalID(v string) { w.Status.ExternalID = v }

// SetObservedGeneration sets status.observedGeneration.
func (w *Widget) SetObservedGeneration(v int64) { w.Status.ObservedGeneration = v }

// SetLastReconcileTime sets status.lastReconcileTime.
func (w *Widget) SetLastReconcileTime(v metav1.Time) { w.Status.LastReconcileTime = &v }

// GetEffectiveDeletionPolicy returns status.effectiveDeletionPolicy.
func (w *Widget) GetEffectiveDeletionPolicy() common.DeletionPolicy {
	return w.Status.EffectiveDeletionPolicy
}

// SetEffectiveDeletionPolicy sets the pinned deletion policy and its source.
func (w *Widget) SetEffectiveDeletionPolicy(p common.DeletionPolicy, s common.DeletionPolicySource) {
	w.Status.EffectiveDeletionPolicy, w.Status.DeletionPolicySource = p, s
}

// SetLateInitialized sets status.lateInitialized.
func (w *Widget) SetLateInitialized(v map[string]string) { w.Status.LateInitialized = v }

// GetRotationPolicy returns nil: a data kind does not rotate.
func (w *Widget) GetRotationPolicy() *common.RotationPolicy { return nil }

// GetRotationStatus returns the zero status: a data kind does not rotate.
func (w *Widget) GetRotationStatus() common.RotationStatus { return common.RotationStatus{} }

// SetRotationStatus does nothing: a data kind does not rotate.
func (w *Widget) SetRotationStatus(common.RotationStatus) {}

// GetServerRef returns spec.serverRef.
func (g *Gadget) GetServerRef() common.ServerRef { return g.Spec.ServerRef }

// GetDeletionPolicy returns "": a credential kind has no deletionPolicy.
func (g *Gadget) GetDeletionPolicy() common.DeletionPolicy { return "" }

// GetManagementPolicies returns spec.managementPolicies.
func (g *Gadget) GetManagementPolicies() []common.ManagementPolicy { return g.Spec.ManagementPolicies }

// GetConditions returns status.conditions.
func (g *Gadget) GetConditions() *[]metav1.Condition { return &g.Status.Conditions }

// GetBindingSecretName returns spec.binding.secretName, or the claim's name.
func (g *Gadget) GetBindingSecretName() string {
	if g.Spec.Binding.SecretName != "" {
		return g.Spec.Binding.SecretName
	}
	return g.Name
}

// SetBindingName sets status.binding.name.
func (g *Gadget) SetBindingName(v string) { g.Status.Binding = &common.BindingStatus{Name: v} }

// GetExternalName returns status.externalName.
func (g *Gadget) GetExternalName() string { return g.Status.ExternalName }

// SetExternalName sets status.externalName.
func (g *Gadget) SetExternalName(v string) { g.Status.ExternalName = v }

// GetExternalID returns status.externalId.
func (g *Gadget) GetExternalID() string { return g.Status.ExternalID }

// SetExternalID sets status.externalId.
func (g *Gadget) SetExternalID(v string) { g.Status.ExternalID = v }

// SetObservedGeneration sets status.observedGeneration.
func (g *Gadget) SetObservedGeneration(v int64) { g.Status.ObservedGeneration = v }

// SetLastReconcileTime sets status.lastReconcileTime.
func (g *Gadget) SetLastReconcileTime(v metav1.Time) { g.Status.LastReconcileTime = &v }

// GetEffectiveDeletionPolicy returns "": a credential kind has no pin.
func (g *Gadget) GetEffectiveDeletionPolicy() common.DeletionPolicy { return "" }

// SetEffectiveDeletionPolicy does nothing: a credential kind has no pin.
func (g *Gadget) SetEffectiveDeletionPolicy(common.DeletionPolicy, common.DeletionPolicySource) {}

// SetLateInitialized sets status.lateInitialized.
func (g *Gadget) SetLateInitialized(v map[string]string) { g.Status.LateInitialized = v }

// GetRotationPolicy returns spec.rotationPolicy.
func (g *Gadget) GetRotationPolicy() *common.RotationPolicy { return g.Spec.RotationPolicy }

// GetRotationStatus returns status.rotation, or the zero status.
func (g *Gadget) GetRotationStatus() common.RotationStatus {
	if g.Status.Rotation == nil {
		return common.RotationStatus{}
	}
	return *g.Status.Rotation
}

// SetRotationStatus sets status.rotation.
func (g *Gadget) SetRotationStatus(v common.RotationStatus) { g.Status.Rotation = &v }

var (
	_ runtime.Object = (*Widget)(nil)
	_ runtime.Object = (*Gadget)(nil)
)
