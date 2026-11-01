package testapi_test

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"

	"github.com/craftainer-labs/lib-operator-go/pkg/apis/common"
	"github.com/craftainer-labs/lib-operator-go/pkg/internal/testapi"
	"github.com/craftainer-labs/lib-operator-go/pkg/lifecycle"
	"github.com/craftainer-labs/lib-operator-go/pkg/server"
	"github.com/craftainer-labs/lib-operator-go/pkg/testenv"
)

// The test kinds implement the interfaces the library defines.
var (
	_ lifecycle.Claim = (*testapi.Widget)(nil)
	_ lifecycle.Claim = (*testapi.Gadget)(nil)
	_ server.Server   = (*testapi.ClusterWidgetServer)(nil)
	_ server.Server   = (*testapi.WidgetServer)(nil)
)

func TestSchemeAndCRDs(t *testing.T) {
	s := runtime.NewScheme()
	if err := testapi.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"Widget", "WidgetList", "Gadget", "GadgetList", "ClusterWidgetServer",
		"ClusterWidgetServerList", "WidgetServer", "WidgetServerList"} {
		if !s.Recognizes(testapi.GroupVersion.WithKind(kind)) {
			t.Errorf("scheme does not know %s", kind)
		}
	}
	crds := testapi.CRDs()
	if len(crds) != 4 {
		t.Fatalf("CRDs = %d, want 4", len(crds))
	}
	scopes := map[string]string{}
	for _, crd := range crds {
		scopes[crd.Spec.Names.Kind] = string(crd.Spec.Scope)
		if crd.Spec.Group != testapi.GroupVersion.Group || len(crd.Spec.Versions) != 1 || crd.Spec.Versions[0].Name != testapi.GroupVersion.Version {
			t.Errorf("%s: group %q versions %v", crd.Name, crd.Spec.Group, crd.Spec.Versions)
		}
	}
	if scopes["ClusterWidgetServer"] != "Cluster" || scopes["WidgetServer"] != "Namespaced" ||
		scopes["Widget"] != "Namespaced" || scopes["Gadget"] != "Namespaced" {
		t.Errorf("scopes = %v", scopes)
	}
}

func TestDeepCopy(t *testing.T) {
	testenv.VerifyDeepCopy(t,
		&testapi.Widget{}, &testapi.WidgetList{}, &testapi.WidgetSpec{}, &testapi.WidgetStatus{},
		&testapi.Gadget{}, &testapi.GadgetList{}, &testapi.GadgetSpec{}, &testapi.GadgetStatus{},
		&testapi.ClusterWidgetServer{}, &testapi.ClusterWidgetServerList{}, &testapi.ClusterWidgetServerSpec{},
		&testapi.WidgetServer{}, &testapi.WidgetServerList{}, &testapi.WidgetServerSpec{},
	)
}

func TestWidgetAccessors(t *testing.T) {
	w := &testapi.Widget{}
	w.Spec.ServerRef = common.ServerRef{Kind: "ClusterWidgetServer", Name: "s"}
	w.Spec.ManagementPolicies = []common.ManagementPolicy{"Observe"}
	w.Spec.DeletionPolicy = common.DeletionDelete
	w.SetExternalName("n")
	w.SetExternalID("i")
	w.SetObservedGeneration(3)
	w.SetLastReconcileTime(metav1.Now())
	w.SetEffectiveDeletionPolicy(common.DeletionRetain, common.SourceFallback)
	w.SetLateInitialized(map[string]string{"k": "v"})
	w.SetBindingName("ignored")
	w.SetRotationStatus(common.RotationStatus{Active: "a"})
	*w.GetConditions() = append(*w.GetConditions(), metav1.Condition{Type: "Ready"})

	if w.GetServerRef().Name != "s" || len(w.GetManagementPolicies()) != 1 || w.GetDeletionPolicy() != common.DeletionDelete ||
		w.GetExternalName() != "n" || w.GetExternalID() != "i" || w.Status.ObservedGeneration != 3 ||
		w.Status.LastReconcileTime == nil || w.GetEffectiveDeletionPolicy() != common.DeletionRetain ||
		w.Status.DeletionPolicySource != common.SourceFallback || w.Status.LateInitialized["k"] != "v" ||
		len(w.Status.Conditions) != 1 {
		t.Errorf("widget = %+v", w)
	}
	if w.GetBindingSecretName() != "" || w.GetRotationPolicy() != nil || w.GetRotationStatus() != (common.RotationStatus{}) {
		t.Error("a data kind has no binding or rotation")
	}
}

func TestGadgetAccessors(t *testing.T) {
	g := &testapi.Gadget{ObjectMeta: metav1.ObjectMeta{Name: "claim"}}
	g.Spec.ServerRef = common.ServerRef{Kind: "WidgetServer", Name: "s"}
	g.Spec.ManagementPolicies = []common.ManagementPolicy{"*"}
	if g.GetBindingSecretName() != "claim" {
		t.Errorf("default binding name = %q", g.GetBindingSecretName())
	}
	g.Spec.Binding.SecretName = "custom"
	g.Spec.RotationPolicy = &common.RotationPolicy{}
	if g.GetBindingSecretName() != "custom" || g.GetRotationPolicy() != g.Spec.RotationPolicy {
		t.Error("binding name or rotation policy")
	}
	if g.GetRotationStatus() != (common.RotationStatus{}) {
		t.Error("rotation status before any rotation")
	}
	g.SetRotationStatus(common.RotationStatus{Active: "b", Sequence: 2})
	g.SetBindingName("custom")
	g.SetExternalName("n")
	g.SetExternalID("i")
	g.SetObservedGeneration(5)
	g.SetLastReconcileTime(metav1.Now())
	g.SetLateInitialized(map[string]string{"k": "v"})
	g.SetEffectiveDeletionPolicy(common.DeletionDelete, common.SourceOperatorDefault)
	*g.GetConditions() = append(*g.GetConditions(), metav1.Condition{Type: "Ready"})

	if got := g.GetRotationStatus(); got.Active != "b" || got.Sequence != 2 {
		t.Errorf("rotation status = %+v", got)
	}
	if g.Status.Binding == nil || g.Status.Binding.Name != "custom" || g.GetExternalName() != "n" || g.GetExternalID() != "i" ||
		g.Status.ObservedGeneration != 5 || g.Status.LastReconcileTime == nil || g.Status.LateInitialized["k"] != "v" ||
		len(g.Status.Conditions) != 1 || g.GetServerRef().Name != "s" || len(g.GetManagementPolicies()) != 1 {
		t.Errorf("gadget = %+v", g)
	}
	if g.GetDeletionPolicy() != "" || g.GetEffectiveDeletionPolicy() != "" {
		t.Error("a credential kind has no deletion policy")
	}
}

func TestServerAccessors(t *testing.T) {
	cs := &testapi.ClusterWidgetServer{}
	cs.Spec.Endpoint = "https://c"
	cs.Spec.AllowedNamespaces = common.AllowedNamespaces{From: common.NamespacesAll}
	if cs.GetEndpoint() != "https://c" || cs.GetCommon() != &cs.Spec.ServerCommon || cs.GetAllowedNamespaces().From != common.NamespacesAll {
		t.Errorf("cluster server = %+v", cs)
	}
	ns := &testapi.WidgetServer{}
	ns.Spec.Endpoint = "https://n"
	if ns.GetEndpoint() != "https://n" || ns.GetCommon() != &ns.Spec.ServerCommon || ns.GetAllowedNamespaces() != nil {
		t.Errorf("namespaced server = %+v", ns)
	}
}
