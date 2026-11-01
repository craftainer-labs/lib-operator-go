package testenv_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/craftainer-labs/lib-operator-go/pkg/apis/common"
	"github.com/craftainer-labs/lib-operator-go/pkg/internal/testapi"
)

type obj = map[string]any

func manifest(kind, name, ns string, spec obj) *unstructured.Unstructured {
	o := &unstructured.Unstructured{Object: obj{
		"apiVersion": testapi.GroupVersion.String(),
		"kind":       kind,
		"metadata":   obj{"name": name},
		"spec":       spec,
	}}
	if ns != "" {
		o.SetNamespace(ns)
	}
	return o
}

// created creates the manifest and checks the API server's verdict: an empty
// wantErr means accepted; otherwise the request must be Invalid and the message
// must contain wantErr.
func created(t *testing.T, o *unstructured.Unstructured, wantErr string) {
	t.Helper()
	err := env.Client.Create(context.Background(), o)
	switch {
	case wantErr == "" && err != nil:
		t.Errorf("rejected: %v", err)
	case wantErr != "" && err == nil:
		t.Errorf("accepted, want an error containing %q", wantErr)
	case wantErr != "" && !apierrors.IsInvalid(err):
		t.Errorf("err = %v, want Invalid", err)
	case wantErr != "" && !strings.Contains(err.Error(), wantErr):
		t.Errorf("err = %v, want it to contain %q", err, wantErr)
	}
}

func clusterServerSpec(allowed any) obj {
	spec := obj{"endpoint": "https://backend.example", "adminCredentialsSecretRef": obj{"name": "admin", "namespace": "op"}}
	if allowed != nil {
		spec["allowedNamespaces"] = allowed
	}
	return spec
}

func sel(m obj) obj { return obj{"from": "Selector", "selector": m} }

func manyLabels(n int) obj {
	m := obj{}
	for i := range n {
		m[fmt.Sprintf("k%d", i)] = "v"
	}
	return m
}

func manyExpressions(n int) []any {
	var out []any
	for range n {
		out = append(out, obj{"key": "k", "operator": "Exists"})
	}
	return out
}

// docs/crd-drafts.md: allowedNamespaces fails closed.
func TestAllowedNamespacesSchemaAndCEL(t *testing.T) {
	byName := obj{"key": "kubernetes.io/metadata.name", "operator": "In", "values": []any{"team-a", "team-b"}}
	tests := []struct {
		name    string
		allowed any
		wantErr string
	}{
		{"All", obj{"from": "All"}, ""},
		{"Selector with matchLabels", sel(obj{"matchLabels": obj{"a": "b"}}), ""},
		{"Selector with matchExpressions", sel(obj{"matchExpressions": []any{byName}}), ""},
		{"Selector with both", sel(obj{"matchLabels": obj{"a": "b"}, "matchExpressions": []any{byName}}), ""},
		{"field omitted: fails closed", nil, "allowedNamespaces"},
		{"from missing", obj{}, "from"},
		{"from missing with a selector", obj{"selector": obj{"matchLabels": obj{"a": "b"}}}, "from"},
		{"Same is not offered", obj{"from": "Same"}, "Unsupported value"},
		{"All with a selector", obj{"from": "All", "selector": obj{"matchLabels": obj{"a": "b"}}}, "selector must not be set when from is All"},
		{"Selector without a selector", obj{"from": "Selector"}, "selector is required when from is Selector"},
		{"Selector with an empty selector", sel(obj{}), "selector must have matchLabels or matchExpressions"},
		{"Selector with empty matchLabels", sel(obj{"matchLabels": obj{}}), "selector must have matchLabels or matchExpressions"},
		{"Selector with empty matchExpressions", sel(obj{"matchExpressions": []any{}}), "selector must have matchLabels or matchExpressions"},
		{"20 labels", sel(obj{"matchLabels": manyLabels(20)}), ""},
		{"21 labels", sel(obj{"matchLabels": manyLabels(21)}), "20"},
		{"20 expressions", sel(obj{"matchExpressions": manyExpressions(20)}), ""},
		{"21 expressions", sel(obj{"matchExpressions": manyExpressions(21)}), "20"},
		{"unknown operator", sel(obj{"matchExpressions": []any{obj{"key": "k", "operator": "Bogus"}}}), "Unsupported value"},
	}
	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			created(t, manifest("ClusterWidgetServer", fmt.Sprintf("allowed-%d", i), "", clusterServerSpec(tt.allowed)), tt.wantErr)
		})
	}
}

func TestServerCommonSchemaAndCEL(t *testing.T) {
	ns := env.Namespace(t, nil)
	spec := func(extra obj) obj {
		s := obj{"endpoint": "https://backend.example", "adminCredentialsSecretRef": obj{"name": "admin"}}
		for k, v := range extra {
			s[k] = v
		}
		return s
	}
	ref := obj{"name": "ca", "key": "ca.crt"}
	tests := []struct {
		name    string
		extra   obj
		wantErr string
	}{
		{"a namespaced server has no allowedNamespaces", nil, ""},
		{"CA from a Secret", obj{"tls": obj{"caBundle": obj{"secretRef": ref}}}, ""},
		{"CA from a ConfigMap", obj{"tls": obj{"caBundle": obj{"configMapRef": ref}}}, ""},
		{"CA from both", obj{"tls": obj{"caBundle": obj{"secretRef": ref, "configMapRef": ref}}}, "exactly one of secretRef and configMapRef"},
		{"CA from neither", obj{"tls": obj{"caBundle": obj{}}}, "exactly one of secretRef and configMapRef"},
		{"TLS 1.3", obj{"tls": obj{"minVersion": "1.3"}}, ""},
		{"TLS 1.1 is not offered", obj{"tls": obj{"minVersion": "1.1"}}, "Unsupported value"},
		{"health check 30s", obj{"healthCheckInterval": "30s"}, ""},
		{"health check 10s", obj{"healthCheckInterval": "10s"}, ""},
		{"health check 1h", obj{"healthCheckInterval": "1h"}, ""},
		{"health check too short", obj{"healthCheckInterval": "5s"}, "between 10s and 1h"},
		{"health check too long", obj{"healthCheckInterval": "2h"}, "between 10s and 1h"},
	}
	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			created(t, manifest("WidgetServer", fmt.Sprintf("srv-%d", i), ns, spec(tt.extra)), tt.wantErr)
		})
	}
}

func claimSpec(extra obj) obj {
	s := obj{"serverRef": obj{"kind": "ClusterWidgetServer", "name": "shared"}}
	for k, v := range extra {
		s[k] = v
	}
	return s
}

// docs/crd-drafts.md: ClaimCommon, managementPolicies.
func TestManagementPoliciesSchemaAndCEL(t *testing.T) {
	ns := env.Namespace(t, nil)
	list := func(v ...string) obj {
		items := []any{}
		for _, s := range v {
			items = append(items, s)
		}
		return obj{"managementPolicies": items}
	}
	tests := []struct {
		name    string
		extra   obj
		wantErr string
	}{
		{"default", nil, ""},
		{"star", list("*"), ""},
		{"Observe", list("Observe"), ""},
		{"never delete", list("Observe", "Create", "Update"), ""},
		{"never change after creation", list("Observe", "Create", "Delete"), ""},
		{"every explicit value", list("Observe", "Create", "Update", "Delete", "LateInitialize"), ""},
		{"empty", list(), "at least 1"},
		{"no Observe", list("Create", "Update"), "an explicit list must contain Observe"},
		{"star with others", list("*", "Observe"), "'*' must stand alone"},
		{"duplicate", list("Observe", "Observe"), "Duplicate value"},
		{"six values", list("*", "Observe", "Create", "Update", "Delete", "LateInitialize"), "at most 5"},
		{"unknown value", list("Observe", "Bogus"), "Unsupported value"},
	}
	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			created(t, manifest("Widget", fmt.Sprintf("mp-%d", i), ns, claimSpec(tt.extra)), tt.wantErr)
		})
	}

	t.Run("the schema default is [*]", func(t *testing.T) {
		var w testapi.Widget
		if err := env.Client.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: "mp-0"}, &w); err != nil {
			t.Fatal(err)
		}
		if got := w.Spec.ManagementPolicies; len(got) != 1 || got[0] != common.ManagementAll {
			t.Errorf("managementPolicies = %v", got)
		}
	})
}

func TestDataClaimSchema(t *testing.T) {
	ns := env.Namespace(t, nil)
	for i, tt := range []struct {
		name, policy, wantErr string
	}{
		{"Delete", "Delete", ""},
		{"Retain", "Retain", ""},
		{"unknown", "Maybe", "Unsupported value"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			created(t, manifest("Widget", fmt.Sprintf("dp-%d", i), ns, claimSpec(obj{"deletionPolicy": tt.policy})), tt.wantErr)
		})
	}
	t.Run("no schema default for deletionPolicy", func(t *testing.T) {
		created(t, manifest("Widget", "dp-none", ns, claimSpec(nil)), "")
		var w testapi.Widget
		if err := env.Client.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: "dp-none"}, &w); err != nil {
			t.Fatal(err)
		}
		if w.Spec.DeletionPolicy != "" {
			t.Errorf("deletionPolicy = %q: the default is the operator's, not the schema's", w.Spec.DeletionPolicy)
		}
	})
	t.Run("serverRef needs kind and name", func(t *testing.T) {
		created(t, manifest("Widget", "no-kind", ns, obj{"serverRef": obj{"name": "x"}}), "kind")
		created(t, manifest("Widget", "no-ref", ns, obj{}), "serverRef")
	})
}

func TestServerRefIsImmutable(t *testing.T) {
	ns := env.Namespace(t, nil)
	created(t, manifest("Widget", "immutable", ns, claimSpec(nil)), "")
	var w testapi.Widget
	if err := env.Client.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: "immutable"}, &w); err != nil {
		t.Fatal(err)
	}
	w.Spec.ServerRef.Name = "other"
	err := env.Client.Update(context.Background(), &w)
	if err == nil || !strings.Contains(err.Error(), "serverRef is immutable") {
		t.Errorf("err = %v", err)
	}
}

// docs/crd-drafts.md: Credentials.
func TestRotationPolicySchemaAndCEL(t *testing.T) {
	ns := env.Namespace(t, nil)
	policy := func(interval, grace string) obj {
		p := obj{}
		if interval != "" {
			p["interval"] = interval
		}
		if grace != "" {
			p["gracePeriod"] = grace
		}
		return obj{"rotationPolicy": p}
	}
	with := func(policies []any, rp obj) obj {
		s := obj{}
		for k, v := range rp {
			s[k] = v
		}
		if policies != nil {
			s["managementPolicies"] = policies
		}
		return s
	}
	tests := []struct {
		name    string
		extra   obj
		wantErr string
	}{
		{"interval 1h", policy("1h", ""), ""},
		{"interval 720h and grace 2h", policy("720h", "2h"), ""},
		{"interval 8760h", policy("8760h", ""), ""},
		{"empty policy: trigger only", policy("", ""), ""},
		{"interval too short", policy("30m", ""), "between 1h and 8760h"},
		{"interval too long", policy("8761h", ""), "between 1h and 8760h"},
		{"grace 5m", policy("", "5m"), ""},
		{"grace 168h", policy("", "168h"), ""},
		{"grace too short", policy("", "1m"), "between 5m and 168h"},
		{"grace too long", policy("", "169h"), "between 5m and 168h"},
		{"rotation with the default policies", policy("1h", ""), ""},
		{"rotation with Update", with([]any{"Observe", "Update"}, policy("1h", "")), ""},
		{"rotation with star", with([]any{"*"}, policy("1h", "")), ""},
		{"rotation without Update", with([]any{"Observe", "Create"}, policy("1h", "")), "rotationPolicy needs the Update management policy"},
		{"observe only with rotation", with([]any{"Observe"}, policy("", "")), "rotationPolicy needs the Update management policy"},
		{"no rotation without Update is fine", with([]any{"Observe", "Create"}, nil), ""},
	}
	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			created(t, manifest("Gadget", fmt.Sprintf("rp-%d", i), ns, claimSpec(tt.extra)), tt.wantErr)
		})
	}

	t.Run("gracePeriod defaults to 1h", func(t *testing.T) {
		var g testapi.Gadget
		if err := env.Client.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: "rp-3"}, &g); err != nil {
			t.Fatal(err)
		}
		if g.Spec.RotationPolicy == nil || g.Spec.RotationPolicy.GracePeriodOrDefault() != common.DefaultGracePeriod ||
			g.Spec.RotationPolicy.GracePeriod == nil {
			t.Errorf("rotationPolicy = %+v", g.Spec.RotationPolicy)
		}
	})

	t.Run("an edit cannot drop Update while rotationPolicy is set", func(t *testing.T) {
		created(t, manifest("Gadget", "drop-update", ns, claimSpec(with([]any{"Observe", "Update"}, policy("1h", "")))), "")
		var g testapi.Gadget
		if err := env.Client.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: "drop-update"}, &g); err != nil {
			t.Fatal(err)
		}
		g.Spec.ManagementPolicies = []common.ManagementPolicy{"Observe", "Create"}
		err := env.Client.Update(context.Background(), &g)
		if err == nil || !strings.Contains(err.Error(), "rotationPolicy needs the Update management policy") {
			t.Errorf("err = %v", err)
		}
	})
}

func TestBindingSecretNameSchema(t *testing.T) {
	ns := env.Namespace(t, nil)
	created(t, manifest("Gadget", "named", ns, claimSpec(obj{"binding": obj{"secretName": "orders-db"}})), "")
	created(t, manifest("Gadget", "badname", ns, claimSpec(obj{"binding": obj{"secretName": "Bad_Name"}})), "should match")

	var g testapi.Gadget
	if err := env.Client.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: "named"}, &g); err != nil {
		t.Fatal(err)
	}
	g.Spec.Binding.SecretName = "renamed"
	if err := env.Client.Update(context.Background(), &g); err == nil || !strings.Contains(err.Error(), "binding.secretName is immutable") {
		t.Errorf("err = %v", err)
	}
}

func TestStatusSchema(t *testing.T) {
	ns := env.Namespace(t, nil)
	created(t, manifest("Widget", "st-w", ns, claimSpec(nil)), "")
	created(t, manifest("Gadget", "st-g", ns, claimSpec(nil)), "")

	long := func(n int) string { return strings.Repeat("x", n) }
	values := func(n int, size int) map[string]string {
		m := map[string]string{}
		for i := range n {
			m[fmt.Sprintf("k%d", i)] = long(size)
		}
		return m
	}
	t.Run("lateInitialized", func(t *testing.T) {
		for _, tt := range []struct {
			name    string
			m       map[string]string
			wantErr string
		}{
			{"20 values of 255", values(20, 255), ""},
			{"a value of 256", values(1, 256), "at most 255 characters"},
			{"21 values", values(21, 1), "20"},
		} {
			var w testapi.Widget
			if err := env.Client.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: "st-w"}, &w); err != nil {
				t.Fatal(err)
			}
			w.Status.LateInitialized = tt.m
			err := env.Client.Status().Update(context.Background(), &w)
			if (tt.wantErr == "") != (err == nil) || (err != nil && !strings.Contains(err.Error(), tt.wantErr)) {
				t.Errorf("%s: err = %v, want %q", tt.name, err, tt.wantErr)
			}
		}
	})
	t.Run("rotation", func(t *testing.T) {
		for _, tt := range []struct {
			name    string
			active  string
			wantErr string
		}{{"128 characters", long(128), ""}, {"129 characters", long(129), "128"}} {
			var g testapi.Gadget
			if err := env.Client.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: "st-g"}, &g); err != nil {
				t.Fatal(err)
			}
			g.Status.Rotation = &common.RotationStatus{Active: tt.active}
			err := env.Client.Status().Update(context.Background(), &g)
			if (tt.wantErr == "") != (err == nil) || (err != nil && !strings.Contains(err.Error(), tt.wantErr)) {
				t.Errorf("%s: err = %v, want %q", tt.name, err, tt.wantErr)
			}
		}
	})
}
