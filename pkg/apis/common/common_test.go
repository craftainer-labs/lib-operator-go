package common_test

import (
	"errors"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/craftainer-labs/lib-operator-go/pkg/apis/common"
	"github.com/craftainer-labs/lib-operator-go/pkg/testenv"
)

func TestParseManagementPolicies(t *testing.T) {
	all := common.Policies{Observe: true, Create: true, Update: true, Delete: true, LateInitialize: true}
	failed := common.Policies{Observe: true, ObserveOnly: true}
	tests := []struct {
		name    string
		list    []common.ManagementPolicy
		want    common.Policies
		wantErr bool
	}{
		{"nil is the schema default", nil, all, false},
		{"empty is the schema default", []common.ManagementPolicy{}, all, false},
		{"star", []common.ManagementPolicy{"*"}, all, false},
		{"observe only", []common.ManagementPolicy{"Observe"}, common.Policies{Observe: true, ObserveOnly: true}, false},
		{"never delete", []common.ManagementPolicy{"Observe", "Create", "Update"},
			common.Policies{Observe: true, Create: true, Update: true}, false},
		{"never change after creation", []common.ManagementPolicy{"Observe", "Create", "Delete"},
			common.Policies{Observe: true, Create: true, Delete: true}, false},
		{"late initialize", []common.ManagementPolicy{"Observe", "LateInitialize"},
			common.Policies{Observe: true, LateInitialize: true}, false},
		{"every explicit value", []common.ManagementPolicy{"Observe", "Create", "Update", "Delete", "LateInitialize"}, all, false},
		{"order does not matter", []common.ManagementPolicy{"Update", "Observe"}, common.Policies{Observe: true, Update: true}, false},
		{"no Observe", []common.ManagementPolicy{"Create"}, failed, true},
		{"star with others", []common.ManagementPolicy{"*", "Observe"}, failed, true},
		{"duplicate", []common.ManagementPolicy{"Observe", "Observe"}, failed, true},
		{"unknown", []common.ManagementPolicy{"Observe", "Bogus"}, failed, true},
		{"too many", []common.ManagementPolicy{"Observe", "Create", "Update", "Delete", "LateInitialize", "Observe"}, failed, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := common.ParseManagementPolicies(tt.list)
			if (err != nil) != tt.wantErr || (err != nil && !errors.Is(err, common.ErrInvalidPolicies)) {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if got != tt.want {
				t.Errorf("policies = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestAllows(t *testing.T) {
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "team-a", Labels: map[string]string{
		"kubernetes.io/metadata.name": "team-a", "craftainer.dev/db": "allowed"}}}
	sel := func(l map[string]string, e ...common.LabelSelectorRequirement) *common.LabelSelector {
		return &common.LabelSelector{MatchLabels: l, MatchExpressions: e}
	}
	in := func(key string, vals ...string) common.LabelSelectorRequirement {
		return common.LabelSelectorRequirement{Key: key, Operator: "In", Values: vals}
	}
	tests := []struct {
		name string
		a    *common.AllowedNamespaces
		want bool
	}{
		{"nil fails closed", nil, false},
		{"zero value fails closed", &common.AllowedNamespaces{}, false},
		{"unknown from fails closed", &common.AllowedNamespaces{From: "Same"}, false},
		{"All", &common.AllowedNamespaces{From: common.NamespacesAll}, true},
		{"selector missing", &common.AllowedNamespaces{From: common.NamespacesSelector}, false},
		{"selector empty", &common.AllowedNamespaces{From: common.NamespacesSelector, Selector: sel(nil)}, false},
		{"matchLabels hit", &common.AllowedNamespaces{From: common.NamespacesSelector,
			Selector: sel(map[string]string{"craftainer.dev/db": "allowed"})}, true},
		{"matchLabels miss", &common.AllowedNamespaces{From: common.NamespacesSelector,
			Selector: sel(map[string]string{"craftainer.dev/db": "denied"})}, false},
		{"metadata.name In hit", &common.AllowedNamespaces{From: common.NamespacesSelector,
			Selector: sel(nil, in("kubernetes.io/metadata.name", "team-a", "team-b"))}, true},
		{"metadata.name In miss", &common.AllowedNamespaces{From: common.NamespacesSelector,
			Selector: sel(nil, in("kubernetes.io/metadata.name", "team-b"))}, false},
		{"invalid operator fails closed", &common.AllowedNamespaces{From: common.NamespacesSelector,
			Selector: sel(nil, common.LabelSelectorRequirement{Key: "k", Operator: "Bogus"})}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.a.Allows(ns); got != tt.want {
				t.Errorf("Allows = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestRotationPolicyDurations(t *testing.T) {
	var nilPolicy *common.RotationPolicy
	if got := nilPolicy.IntervalOrZero(); got != 0 {
		t.Errorf("nil interval = %v", got)
	}
	if got := nilPolicy.GracePeriodOrDefault(); got != common.DefaultGracePeriod {
		t.Errorf("nil grace = %v", got)
	}
	empty := &common.RotationPolicy{}
	if empty.IntervalOrZero() != 0 || empty.GracePeriodOrDefault() != time.Hour {
		t.Errorf("empty policy: %v %v", empty.IntervalOrZero(), empty.GracePeriodOrDefault())
	}
	set := &common.RotationPolicy{
		Interval:    &metav1.Duration{Duration: 720 * time.Hour},
		GracePeriod: &metav1.Duration{Duration: 2 * time.Hour},
	}
	if set.IntervalOrZero() != 720*time.Hour || set.GracePeriodOrDefault() != 2*time.Hour {
		t.Errorf("set policy: %v %v", set.IntervalOrZero(), set.GracePeriodOrDefault())
	}
}

// The random filler leaves a *metav1.Time nil; this covers the copy of one.
func TestServerStatusDeepCopyCopiesTheProbeTime(t *testing.T) {
	probe := metav1.NewTime(time.Unix(1000, 0))
	in := &common.ServerStatus{LastProbeTime: &probe, Conditions: []metav1.Condition{{Type: "Ready"}}}
	out := in.DeepCopy()
	if out.LastProbeTime == in.LastProbeTime || !out.LastProbeTime.Equal(in.LastProbeTime) || out.Conditions[0].Type != "Ready" {
		t.Errorf("copy = %+v", out)
	}
}

func TestDeepCopy(t *testing.T) {
	testenv.VerifyDeepCopy(t,
		&common.ServerRef{}, &common.ClaimCommon{}, &common.DataClaim{}, &common.BindingSpec{},
		&common.Credentials{}, &common.RotationPolicy{}, &common.ClaimStatus{}, &common.DataClaimStatus{},
		&common.BindingStatus{}, &common.CredentialsStatus{}, &common.RotationStatus{},
		&common.LabelSelectorRequirement{}, &common.LabelSelector{}, &common.AllowedNamespaces{},
		&common.KeyReference{}, &common.CABundle{}, &common.TLS{}, &common.AdminSecretRef{},
		&common.ServerCommon{}, &common.ServerStatus{}, &common.Policies{},
	)
}
