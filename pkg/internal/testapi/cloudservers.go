package testapi

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"

	"github.com/craftainer-labs/lib-operator-go/pkg/apis/common"
)

// The cloud test kinds are only used with a fake client, so they have no CRD
// and no generated deep copy: both kinds below are excluded from the generator
// and copy themselves by hand.

// ClusterCloudWidgetServerSpec is the spec of the cluster-scoped cloud server.
//
// +kubebuilder:object:generate=false
type ClusterCloudWidgetServerSpec struct {
	common.CloudServerCommon `json:",inline"`

	// Endpoint is the data-plane endpoint override.
	Endpoint string `json:"endpoint,omitempty"`
	// CredentialsSecret is spec.credentials.secretRef.
	CredentialsSecret *common.AdminSecretRef `json:"credentialsSecretRef,omitempty"`
	// AllowedNamespaces is required: a cluster server fails closed.
	AllowedNamespaces common.AllowedNamespaces `json:"allowedNamespaces"`
}

// ClusterCloudWidgetServer is a cluster-scoped cloud server.
//
// +kubebuilder:object:generate=false
type ClusterCloudWidgetServer struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ClusterCloudWidgetServerSpec `json:"spec"`
	Status common.ServerStatus          `json:"status,omitempty"`
}

// ClusterCloudWidgetServerList is a list of ClusterCloudWidgetServers.
//
// +kubebuilder:object:generate=false
type ClusterCloudWidgetServerList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ClusterCloudWidgetServer `json:"items"`
}

// CloudWidgetServerSpec is the spec of the namespaced cloud server.
//
// +kubebuilder:object:generate=false
type CloudWidgetServerSpec struct {
	common.CloudServerCommon `json:",inline"`

	// Endpoint is the data-plane endpoint override.
	Endpoint string `json:"endpoint,omitempty"`
	// CredentialsSecret is spec.credentials.secretRef.
	CredentialsSecret *common.AdminSecretRef `json:"credentialsSecretRef,omitempty"`
}

// CloudWidgetServer is a namespaced cloud server.
//
// +kubebuilder:object:generate=false
type CloudWidgetServer struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   CloudWidgetServerSpec `json:"spec"`
	Status common.ServerStatus   `json:"status,omitempty"`
}

// CloudWidgetServerList is a list of CloudWidgetServers.
//
// +kubebuilder:object:generate=false
type CloudWidgetServerList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []CloudWidgetServer `json:"items"`
}

func copyRef(in *common.AdminSecretRef) *common.AdminSecretRef {
	if in == nil {
		return nil
	}
	out := *in
	return &out
}

// DeepCopyObject returns a copy.
func (in *ClusterCloudWidgetServer) DeepCopyObject() runtime.Object {
	out := *in
	in.ObjectMeta.DeepCopyInto(&out.ObjectMeta)
	out.Spec.CloudServerCommon = *in.Spec.CloudServerCommon.DeepCopy()
	out.Spec.CredentialsSecret = copyRef(in.Spec.CredentialsSecret)
	out.Spec.AllowedNamespaces = *in.Spec.AllowedNamespaces.DeepCopy()
	in.Status.DeepCopyInto(&out.Status)
	return &out
}

// DeepCopyObject returns a copy.
func (in *ClusterCloudWidgetServerList) DeepCopyObject() runtime.Object {
	out := *in
	in.ListMeta.DeepCopyInto(&out.ListMeta)
	out.Items = make([]ClusterCloudWidgetServer, len(in.Items))
	for i := range in.Items {
		out.Items[i] = *in.Items[i].DeepCopyObject().(*ClusterCloudWidgetServer)
	}
	return &out
}

// DeepCopyObject returns a copy.
func (in *CloudWidgetServer) DeepCopyObject() runtime.Object {
	out := *in
	in.ObjectMeta.DeepCopyInto(&out.ObjectMeta)
	out.Spec.CloudServerCommon = *in.Spec.CloudServerCommon.DeepCopy()
	out.Spec.CredentialsSecret = copyRef(in.Spec.CredentialsSecret)
	in.Status.DeepCopyInto(&out.Status)
	return &out
}

// DeepCopyObject returns a copy.
func (in *CloudWidgetServerList) DeepCopyObject() runtime.Object {
	out := *in
	in.ListMeta.DeepCopyInto(&out.ListMeta)
	out.Items = make([]CloudWidgetServer, len(in.Items))
	for i := range in.Items {
		out.Items[i] = *in.Items[i].DeepCopyObject().(*CloudWidgetServer)
	}
	return &out
}

// GetCloudCommon returns the shared cloud server settings.
func (in *ClusterCloudWidgetServer) GetCloudCommon() *common.CloudServerCommon {
	return &in.Spec.CloudServerCommon
}

// GetEndpoint returns spec.endpoint.
func (in *ClusterCloudWidgetServer) GetEndpoint() string { return in.Spec.Endpoint }

// GetAllowedNamespaces returns spec.allowedNamespaces.
func (in *ClusterCloudWidgetServer) GetAllowedNamespaces() *common.AllowedNamespaces {
	return &in.Spec.AllowedNamespaces
}

// GetCredentialsSecret returns spec.credentialsSecretRef.
func (in *ClusterCloudWidgetServer) GetCredentialsSecret() (name, namespace string) {
	return refParts(in.Spec.CredentialsSecret)
}

// GetServerStatus returns status.
func (in *ClusterCloudWidgetServer) GetServerStatus() *common.ServerStatus { return &in.Status }

// GetCloudCommon returns the shared cloud server settings.
func (in *CloudWidgetServer) GetCloudCommon() *common.CloudServerCommon {
	return &in.Spec.CloudServerCommon
}

// GetEndpoint returns spec.endpoint.
func (in *CloudWidgetServer) GetEndpoint() string { return in.Spec.Endpoint }

// GetAllowedNamespaces returns nil: a namespaced server is implicitly Same.
func (in *CloudWidgetServer) GetAllowedNamespaces() *common.AllowedNamespaces { return nil }

// GetCredentialsSecret returns spec.credentialsSecretRef.
func (in *CloudWidgetServer) GetCredentialsSecret() (name, namespace string) {
	return refParts(in.Spec.CredentialsSecret)
}

// GetServerStatus returns status.
func (in *CloudWidgetServer) GetServerStatus() *common.ServerStatus { return &in.Status }

func refParts(r *common.AdminSecretRef) (name, namespace string) {
	if r == nil {
		return "", ""
	}
	return r.Name, r.Namespace
}
