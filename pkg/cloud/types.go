package cloud

// ProviderType selects the cloud of a server (ADR 0002, section 16.1). It is
// immutable: claims already point at objects in the old cloud. An operator
// that supports fewer clouds restricts the enum on its own field with
// +kubebuilder:validation:Enum.
//
// +kubebuilder:validation:Enum=AWS;GCP;Azure
// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="provider.type is immutable"
type ProviderType string

// Values of ProviderType.
const (
	ProviderAWS   ProviderType = "AWS"
	ProviderGCP   ProviderType = "GCP"
	ProviderAzure ProviderType = "Azure"
)

// CredentialsSource says where the operator gets the cloud credential.
//
// +kubebuilder:validation:Enum=WorkloadIdentity;Secret
type CredentialsSource string

// Values of CredentialsSource.
const (
	// SourceWorkloadIdentity uses the identity of the operator pod, found by the
	// SDK's default chain. Cluster kinds only.
	SourceWorkloadIdentity CredentialsSource = "WorkloadIdentity"
	// SourceSecret uses a static credential from a Secret with the keys fixed
	// per cloud.
	SourceSecret CredentialsSource = "Secret"
)

// Keys of the credentials Secret, fixed per cloud (ADR 0002, section 16.2).
const (
	KeyAWSAccessKeyID     = "accessKeyId"
	KeyAWSSecretAccessKey = "secretAccessKey"
	KeyAWSSessionToken    = "sessionToken"
	KeyGCPCredentials     = "credentials.json"
	KeyAzureClientID      = "clientId"
	KeyAzureClientSecret  = "clientSecret"
)

// SecretRef names the Secret that holds a static credential. It has no key:
// the key names are fixed per cloud.
type SecretRef struct {
	// Name is the Secret's name.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	Name string `json:"name"`
	// Namespace is the Secret's namespace: required on a cluster server (the
	// controller accepts only the operator's own namespace), forbidden on a
	// namespaced one.
	// +kubebuilder:validation:MaxLength=63
	// +optional
	Namespace string `json:"namespace,omitempty"`
}

// Credentials is a cluster server's spec.credentials. An absent credentials is
// read as WorkloadIdentity.
//
// +kubebuilder:validation:XValidation:rule="self.source == 'Secret' ? has(self.secretRef) : !has(self.secretRef)",message="secretRef is required when source is Secret and forbidden otherwise"
// +kubebuilder:validation:XValidation:rule="self.source != 'Secret' || !has(self.secretRef) || has(self.secretRef.namespace)",message="secretRef.namespace is required on a cluster server"
type Credentials struct {
	// Source is WorkloadIdentity (default) or Secret.
	// +kubebuilder:default=WorkloadIdentity
	Source CredentialsSource `json:"source"`
	// SecretRef is required if and only if Source is Secret.
	// +optional
	SecretRef *SecretRef `json:"secretRef,omitempty"`
}

// NamespacedCredentials is a namespaced server's spec.credentials. A
// namespaced server is tenant-created and may name any target, so the
// operator's own identity would make it a confused deputy: WorkloadIdentity is
// forbidden (ADR 0002, section 16.2, and decision 2 of 16.8).
//
// +kubebuilder:validation:XValidation:rule="self.source == 'Secret'",message="a namespaced server must use source Secret"
// +kubebuilder:validation:XValidation:rule="has(self.secretRef)",message="secretRef is required"
// +kubebuilder:validation:XValidation:rule="!has(self.secretRef) || !has(self.secretRef.namespace)",message="secretRef.namespace is forbidden on a namespaced server"
type NamespacedCredentials struct {
	// Source must be Secret.
	Source CredentialsSource `json:"source"`
	// SecretRef names the Secret in the server's own namespace.
	// +optional
	SecretRef *SecretRef `json:"secretRef,omitempty"`
}

// AsCredentials returns the same credentials as a Credentials; nil for nil.
func (c *NamespacedCredentials) AsCredentials() *Credentials {
	if c == nil {
		return nil
	}
	return &Credentials{Source: c.Source, SecretRef: c.SecretRef}
}

// AWSBlock is the common part of a provider.aws block. Embed it inline in the
// operator's block with its target fields.
type AWSBlock struct {
	// Region is the AWS region; the partition follows from it.
	// +kubebuilder:validation:MaxLength=32
	// +kubebuilder:validation:Pattern=`^[a-z]{2}(-[a-z]+)+-[0-9]+$`
	Region string `json:"region"`
	// RoleARN, when set, is assumed with the source credentials.
	// +kubebuilder:validation:MaxLength=2048
	// +kubebuilder:validation:XValidation:rule="self.startsWith('arn:')",message="roleARN must start with arn:"
	// +optional
	RoleARN string `json:"roleARN,omitempty"`
}

// AWSClusterBlock is AWSBlock for a cluster kind, which may set an endpoint
// override.
type AWSClusterBlock struct {
	AWSBlock `json:",inline"`
	// EndpointOverride points the SDK at an emulator. It is honoured only when
	// the operator runs with EGRESS_MODE=allow-private; otherwise the server is
	// Ready=False with reason EndpointOverrideForbidden. For tests, never for
	// production.
	// +kubebuilder:validation:MaxLength=2048
	// +kubebuilder:validation:Pattern=`^https?://[^\s]+$`
	// +optional
	EndpointOverride string `json:"endpointOverride,omitempty"`
}

// GCPBlock is the common part of a provider.gcp block.
type GCPBlock struct {
	// Project is the project ID.
	// +kubebuilder:validation:MaxLength=30
	// +kubebuilder:validation:Pattern=`^[a-z][a-z0-9-]{4,28}[a-z0-9]$`
	Project string `json:"project"`
	// Location is the region, required where the resource is regional.
	// +kubebuilder:validation:MaxLength=64
	// +optional
	Location string `json:"location,omitempty"`
}

// GCPClusterBlock is GCPBlock for a cluster kind.
type GCPClusterBlock struct {
	GCPBlock `json:",inline"`
	// EndpointOverride is as on AWSClusterBlock.
	// +kubebuilder:validation:MaxLength=2048
	// +kubebuilder:validation:Pattern=`^https?://[^\s]+$`
	// +optional
	EndpointOverride string `json:"endpointOverride,omitempty"`
}

// AzureBlock is the common part of a provider.azure block. SubscriptionID and
// ResourceGroup are required by the ARM-based operators (db, s3, mqtt) and
// absent for oidc, which talks to Graph; the operator's CEL says which.
type AzureBlock struct {
	// TenantID is the Entra tenant.
	// +kubebuilder:validation:Pattern=`^[0-9a-fA-F]{8}-([0-9a-fA-F]{4}-){3}[0-9a-fA-F]{12}$`
	TenantID string `json:"tenantID"`
	// SubscriptionID is the subscription of the resources.
	// +kubebuilder:validation:Pattern=`^[0-9a-fA-F]{8}-([0-9a-fA-F]{4}-){3}[0-9a-fA-F]{12}$`
	// +optional
	SubscriptionID string `json:"subscriptionID,omitempty"`
	// ResourceGroup is the resource group of the resources.
	// +kubebuilder:validation:Pattern=`^[-\w._()]{1,90}$`
	// +optional
	ResourceGroup string `json:"resourceGroup,omitempty"`
}

// AzureClusterBlock is AzureBlock for a cluster kind. The override replaces
// the Entra authority host, and is handed back as AzureClient.Endpoint for the
// operator's own service client.
type AzureClusterBlock struct {
	AzureBlock `json:",inline"`
	// EndpointOverride is as on AWSClusterBlock.
	// +kubebuilder:validation:MaxLength=2048
	// +kubebuilder:validation:Pattern=`^https?://[^\s]+$`
	// +optional
	EndpointOverride string `json:"endpointOverride,omitempty"`
}
