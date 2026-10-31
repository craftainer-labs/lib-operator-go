// Package conditions names the status conditions every craftainer claim and
// server kind reports (ADR 0002, "Status").
package conditions

// Condition types set on every claim and server kind. All three are set on the
// first reconcile, each with observedGeneration.
const (
	// Ready is True when the external object exists, is up to date and its
	// binding Secret is written.
	Ready = "Ready"
	// Synced is True when the last attempt to make the external object match
	// the spec succeeded.
	Synced = "Synced"
	// ServerReachable is True when the referenced server resolved and answered.
	ServerReachable = "ServerReachable"
)
