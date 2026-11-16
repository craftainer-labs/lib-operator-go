// Package cloud is the shared cloud-provider layer of the operators (ADR 0002,
// section 16): the API types every cloud-capable server kind embeds, credential
// resolution per source and cloud, endpoint overrides gated on the egress mode,
// SDK HTTP clients that dial through pkg/egress, and token sources for IAM data
// plane logins.
//
// # API types
//
// The types carry the kubebuilder markers and CEL rules of the section, so an
// operator's controller-gen run produces the schema without copying anything.
// An operator adds its own target fields (an RDS instance, a bucket location)
// by embedding a block inline in a struct of its own:
//
//	type DBAWS struct {
//		cloud.AWSClusterBlock `json:",inline"`
//		DBInstanceIdentifier  string `json:"dbInstanceIdentifier,omitempty"`
//	}
//
// A cluster-scoped kind embeds AWSClusterBlock, GCPClusterBlock and
// AzureClusterBlock (they carry endpointOverride) and Credentials; a namespaced
// kind embeds AWSBlock, GCPBlock and AzureBlock (no endpointOverride, so the
// schema cannot carry one) and NamespacedCredentials (WorkloadIdentity is
// refused, and so is a secretRef namespace). ProviderType is the discriminator,
// immutable. The rules that span the server spec cannot live on a shared type;
// the operator adds them to its server spec, as written in docs/crd-drafts.md:
//
//	provider is the union: self.type == 'AWS' ? has(self.aws) : !has(self.aws)
//	  (likewise gcp and azure)
//	credentials only with provider: !has(self.credentials) || has(self.provider)
//	namespaced kinds need credentials: !has(self.provider) || has(self.credentials)
//
// # Resolving credentials
//
// Egress.AWS, Egress.GCP and Egress.Azure turn a provider block and its
// credentials into an SDK configuration. The caller reads the Secret
// credentials.secretRef names (the operator's own namespace for a cluster
// kind, which the caller enforces) and passes its data. WorkloadIdentity uses
// the SDK's own chain on the operator pod: IRSA and EKS Pod Identity, the
// Application Default Credentials of a GKE pod, the Azure workload identity
// webhook's variables. Secret uses the cloud's fixed keys (AWS accessKeyId,
// secretAccessKey, sessionToken; GCP credentials.json holding a
// service_account key; Azure clientId and clientSecret). Errors carry a
// category only and never an SDK message, so no secret material reaches a
// condition, an Event or a log.
//
// # Egress
//
// Every HTTP client the package builds dials through egress.Policy.HostDialer,
// so a tenant-chosen address (a service account's token_uri, an endpoint
// override) cannot reach loopback, link-local or, in strict mode, private
// addresses. That covers the token endpoints: AWS STS, the Google OAuth
// endpoint and the Entra authority. The clients take no proxy from the
// environment. The platform's own credential sources are reached by the SDKs'
// built-in clients and not through the policy, because they live at link-local
// addresses the policy blocks: the EKS Pod Identity agent
// (AWS_CONTAINER_CREDENTIALS_FULL_URI) and the GCE and GKE metadata server. The
// operator's environment chooses those, never a tenant. EC2 instance metadata
// and Azure managed identity do use the policy's client and so fail closed;
// Azure workload identity uses a federated token file and the Entra authority.
//
// endpointOverride (an emulator) is honoured only for a cluster-scoped server
// and only when the mode is egress.ModeAllowPrivate. Emulators are reached at
// private addresses: the policy refuses loopback in both modes.
//
// # Token sources
//
// TokenSource and CachedSource serve the IAM logins of the data plane: an RDS
// IAM token (valid 15 minutes) per connection, an Entra token for PostgreSQL
// or Redis, a Google access token for Memorystore. A CachedSource refreshes a
// token a margin before it expires and never hands out one that is closer to
// expiry than the margin.
//
// +kubebuilder:object:generate=true
package cloud

//go:generate go run sigs.k8s.io/controller-tools/cmd/controller-gen@v0.22.0 object paths=.
