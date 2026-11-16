package cloud

import "errors"

// Errors of the package. Each is a category: the text names what is wrong,
// never a secret value or an SDK message (which can echo a response body).
// Operators map them to the condition reasons of ADR 0002, section 16.1.
var (
	// ErrCredentialsUnavailable is reason CredentialsUnavailable: no workload
	// identity is injected, the Secret is missing a key or malformed, a GCP key
	// is not a service_account key, or the cloud refused the credential.
	ErrCredentialsUnavailable = errors.New("cloud credentials unavailable")
	// ErrEndpointOverrideForbidden is reason EndpointOverrideForbidden:
	// endpointOverride is set on a namespaced server or without
	// EGRESS_MODE=allow-private.
	ErrEndpointOverrideForbidden = errors.New("endpointOverride is allowed only on cluster-scoped servers with EGRESS_MODE=allow-private")
	// ErrWorkloadIdentityForbidden is the namespaced server's WorkloadIdentity,
	// which CEL already refuses; the resolvers check it again.
	ErrWorkloadIdentityForbidden = errors.New("a namespaced server cannot use WorkloadIdentity credentials")
	// ErrInvalidEndpoint is an endpointOverride that is not an http or https URL
	// with a host and no user information.
	ErrInvalidEndpoint = errors.New("invalid endpoint URL")
)
