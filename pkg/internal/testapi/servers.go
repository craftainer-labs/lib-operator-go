package testapi

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/craftainer-labs/lib-operator-go/pkg/apis/common"
)

// ClusterWidgetServerSpec is the spec of the cluster-scoped server.
type ClusterWidgetServerSpec struct {
	common.ServerCommon `json:",inline"`

	// Endpoint is the backend URL.
	// +kubebuilder:validation:MaxLength=512
	Endpoint string `json:"endpoint"`
	// AllowedNamespaces is required: a cluster server fails closed.
	AllowedNamespaces common.AllowedNamespaces `json:"allowedNamespaces"`
}

// ClusterWidgetServer is a cluster-scoped server, like ClusterDatabaseServer.
//
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster
type ClusterWidgetServer struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ClusterWidgetServerSpec `json:"spec"`
	Status common.ServerStatus     `json:"status,omitempty"`
}

// ClusterWidgetServerList is a list of ClusterWidgetServers.
//
// +kubebuilder:object:root=true
type ClusterWidgetServerList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ClusterWidgetServer `json:"items"`
}

// WidgetServerSpec is the spec of the namespaced server.
type WidgetServerSpec struct {
	common.ServerCommon `json:",inline"`

	// Endpoint is the backend URL.
	// +kubebuilder:validation:MaxLength=512
	Endpoint string `json:"endpoint"`
}

// WidgetServer is a namespaced server, like DatabaseServer.
//
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
type WidgetServer struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   WidgetServerSpec    `json:"spec"`
	Status common.ServerStatus `json:"status,omitempty"`
}

// WidgetServerList is a list of WidgetServers.
//
// +kubebuilder:object:root=true
type WidgetServerList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []WidgetServer `json:"items"`
}

// GetCommon returns the shared server settings.
func (s *ClusterWidgetServer) GetCommon() *common.ServerCommon { return &s.Spec.ServerCommon }

// GetEndpoint returns spec.endpoint.
func (s *ClusterWidgetServer) GetEndpoint() string { return s.Spec.Endpoint }

// GetAllowedNamespaces returns spec.allowedNamespaces.
func (s *ClusterWidgetServer) GetAllowedNamespaces() *common.AllowedNamespaces {
	return &s.Spec.AllowedNamespaces
}

// GetCommon returns the shared server settings.
func (s *WidgetServer) GetCommon() *common.ServerCommon { return &s.Spec.ServerCommon }

// GetEndpoint returns spec.endpoint.
func (s *WidgetServer) GetEndpoint() string { return s.Spec.Endpoint }

// GetAllowedNamespaces returns nil: a namespaced server is implicitly Same.
func (s *WidgetServer) GetAllowedNamespaces() *common.AllowedNamespaces { return nil }
