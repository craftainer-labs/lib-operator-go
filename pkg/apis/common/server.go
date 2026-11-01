package common

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
)

// NamespaceFrom says which namespaces may use a cluster server. It has the
// shape of the Gateway API's FromNamespaces without Same.
//
// +kubebuilder:validation:Enum=All;Selector
type NamespaceFrom string

// Values of NamespaceFrom.
const (
	// NamespacesAll allows every namespace.
	NamespacesAll NamespaceFrom = "All"
	// NamespacesSelector allows the namespaces whose labels match the selector.
	NamespacesSelector NamespaceFrom = "Selector"
)

// LabelSelectorOperator is the operator of a LabelSelectorRequirement.
//
// +kubebuilder:validation:Enum=In;NotIn;Exists;DoesNotExist
type LabelSelectorOperator string

// LabelSelectorRequirement mirrors metav1.LabelSelectorRequirement with the
// bounds CEL cost estimation needs.
type LabelSelectorRequirement struct {
	// Key is the label key.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	Key string `json:"key"`
	// Operator relates the key to the values.
	Operator LabelSelectorOperator `json:"operator"`
	// Values are the label values for In and NotIn.
	//
	// +kubebuilder:validation:MaxItems=20
	// +kubebuilder:validation:items:MaxLength=63
	// +optional
	Values []string `json:"values,omitempty"`
}

// LabelSelector mirrors metav1.LabelSelector, which cannot carry maxItems and
// maxProperties from a field marker. The JSON shape is identical.
type LabelSelector struct {
	// MatchLabels requires each key to have the value.
	//
	// +kubebuilder:validation:MaxProperties=20
	// +optional
	MatchLabels map[string]string `json:"matchLabels,omitempty"`
	// MatchExpressions are label requirements, ANDed.
	//
	// +kubebuilder:validation:MaxItems=20
	// +optional
	MatchExpressions []LabelSelectorRequirement `json:"matchExpressions,omitempty"`
}

// AllowedNamespaces is a cluster server's allowedNamespaces (ADR 0002,
// section 2). It fails closed: the field is required on a cluster server,
// there is no default for From, and an empty selector is invalid.
//
// +kubebuilder:validation:XValidation:rule="self.from != 'All' || !has(self.selector)",message="selector must not be set when from is All"
// +kubebuilder:validation:XValidation:rule="self.from != 'Selector' || has(self.selector)",message="selector is required when from is Selector"
// +kubebuilder:validation:XValidation:rule="self.from != 'Selector' || !has(self.selector) || (has(self.selector.matchLabels) ? size(self.selector.matchLabels) : 0) + (has(self.selector.matchExpressions) ? size(self.selector.matchExpressions) : 0) > 0",message="selector must have matchLabels or matchExpressions; use from: All to allow every namespace"
type AllowedNamespaces struct {
	// From selects All namespaces or those matching Selector.
	From NamespaceFrom `json:"from"`
	// Selector is required if and only if From is Selector.
	// +optional
	Selector *LabelSelector `json:"selector,omitempty"`
}

// Allows reports whether a claim in the namespace may use the server. Anything
// but a well-formed All or non-empty Selector allows nothing: a nil receiver,
// an unknown From, an empty or invalid selector.
func (a *AllowedNamespaces) Allows(ns *corev1.Namespace) bool {
	if a == nil {
		return false
	}
	switch a.From {
	case NamespacesAll:
		return true
	case NamespacesSelector:
		if a.Selector == nil || len(a.Selector.MatchLabels)+len(a.Selector.MatchExpressions) == 0 {
			return false
		}
		sel, err := metav1.LabelSelectorAsSelector(a.Selector.asMeta())
		if err != nil {
			return false
		}
		return sel.Matches(labels.Set(ns.Labels))
	default:
		return false
	}
}

func (s *LabelSelector) asMeta() *metav1.LabelSelector {
	out := &metav1.LabelSelector{MatchLabels: s.MatchLabels}
	for _, r := range s.MatchExpressions {
		out.MatchExpressions = append(out.MatchExpressions, metav1.LabelSelectorRequirement{
			Key:      r.Key,
			Operator: metav1.LabelSelectorOperator(r.Operator),
			Values:   r.Values,
		})
	}
	return out
}

// TLSVersion is tls.minVersion.
//
// +kubebuilder:validation:Enum="1.2";"1.3"
type TLSVersion string

// Values of TLSVersion.
const (
	// TLS12 is TLS 1.2, the floor and the default.
	TLS12 TLSVersion = "1.2"
	// TLS13 is TLS 1.3.
	TLS13 TLSVersion = "1.3"
)

// KeyReference names a key of a Secret or ConfigMap.
type KeyReference struct {
	// Name of the Secret or ConfigMap.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	Name string `json:"name"`
	// Key inside it.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	Key string `json:"key"`
}

// CABundle points at the trust anchors of a server. Exactly one reference.
//
// +kubebuilder:validation:XValidation:rule="has(self.secretRef) != has(self.configMapRef)",message="exactly one of secretRef and configMapRef"
type CABundle struct {
	// SecretRef is a Secret key holding a PEM bundle.
	// +optional
	SecretRef *KeyReference `json:"secretRef,omitempty"`
	// ConfigMapRef is a ConfigMap key holding a PEM bundle.
	// +optional
	ConfigMapRef *KeyReference `json:"configMapRef,omitempty"`
}

// TLS configures the connection to a backend. There is no insecureSkipVerify.
type TLS struct {
	// CABundle replaces the system roots.
	// +optional
	CABundle *CABundle `json:"caBundle,omitempty"`
	// ServerName overrides the host used for certificate verification.
	// +kubebuilder:validation:MaxLength=253
	// +optional
	ServerName string `json:"serverName,omitempty"`
	// MinVersion is the TLS floor.
	// +kubebuilder:default="1.2"
	// +optional
	MinVersion TLSVersion `json:"minVersion,omitempty"`
}

// AdminSecretRef names the Secret holding the server's admin credentials.
// A cluster server names a Secret in the operator namespace; a namespaced
// server leaves Namespace unset.
type AdminSecretRef struct {
	// Name of the Secret.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	Name string `json:"name"`
	// Key limits the credentials to one key of the Secret.
	// +kubebuilder:validation:MaxLength=253
	// +optional
	Key string `json:"key,omitempty"`
	// Namespace is required on a cluster server (the operator namespace) and
	// forbidden on a namespaced one; the operator's own CEL enforces that.
	// +kubebuilder:validation:MaxLength=63
	// +optional
	Namespace string `json:"namespace,omitempty"`
}

// ServerCommon is the spec every server kind embeds.
type ServerCommon struct {
	// TLS configures verification of the backend.
	// +optional
	TLS *TLS `json:"tls,omitempty"`
	// AdminCredentialsSecretRef names the admin credentials.
	AdminCredentialsSecretRef AdminSecretRef `json:"adminCredentialsSecretRef"`
	// ProxyURL is an http, https or socks5 proxy; unset uses the process
	// environment.
	//
	// +kubebuilder:validation:MaxLength=512
	// +optional
	ProxyURL string `json:"proxyURL,omitempty"`
	// HealthCheckInterval is how often the server is probed (default 1m,
	// minimum 10s, maximum 1h).
	//
	// +kubebuilder:validation:Type=string
	// +kubebuilder:validation:MaxLength=32
	// +kubebuilder:validation:XValidation:rule="duration(self) >= duration('10s') && duration(self) <= duration('1h')",message="healthCheckInterval must be between 10s and 1h"
	// +optional
	HealthCheckInterval *metav1.Duration `json:"healthCheckInterval,omitempty"`
}

// ServerStatus is the status every server kind embeds.
type ServerStatus struct {
	// ObservedGeneration is the metadata.generation last reconciled.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
	// Conditions holds Ready and ServerReachable.
	//
	// +listType=map
	// +listMapKey=type
	// +kubebuilder:validation:MaxItems=8
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
	// ServerVersion is the version the backend reported.
	// +kubebuilder:validation:MaxLength=64
	// +optional
	ServerVersion string `json:"serverVersion,omitempty"`
	// LastProbeTime is when the server was last probed.
	// +optional
	LastProbeTime *metav1.Time `json:"lastProbeTime,omitempty"`
}
