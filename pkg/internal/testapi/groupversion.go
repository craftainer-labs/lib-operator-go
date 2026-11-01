// Package testapi holds the throwaway CRD kinds the library's own tests run
// against: a data claim (Widget), a credential claim (Gadget), and a cluster
// and a namespaced server (ClusterWidgetServer, WidgetServer). They embed
// pkg/apis/common exactly as an operator's kinds do, so the generated CRDs in
// crds/ exercise the real schema and CEL rules against a real API server.
//
// Regenerate the deep-copy code and the CRDs with `go generate` in this
// directory after changing a type.
//
// +kubebuilder:object:generate=true
// +groupName=test.craftainer.dev
// +versionName=v1alpha1
package testapi

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

//go:generate go run sigs.k8s.io/controller-tools/cmd/controller-gen@v0.22.0 object crd paths=. output:crd:artifacts:config=crds

var (
	// GroupVersion is the group and version of the test kinds.
	GroupVersion = schema.GroupVersion{Group: "test.craftainer.dev", Version: "v1alpha1"}

	// SchemeBuilder registers the test kinds.
	SchemeBuilder = runtime.NewSchemeBuilder(addKnownTypes)

	// AddToScheme adds the test kinds to a scheme.
	AddToScheme = SchemeBuilder.AddToScheme
)

func addKnownTypes(s *runtime.Scheme) error {
	s.AddKnownTypes(GroupVersion,
		&Widget{}, &WidgetList{}, &Gadget{}, &GadgetList{},
		&ClusterWidgetServer{}, &ClusterWidgetServerList{}, &WidgetServer{}, &WidgetServerList{})
	metav1.AddToGroupVersion(s, GroupVersion)
	return nil
}
