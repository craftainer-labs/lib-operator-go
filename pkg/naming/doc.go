// Package naming derives every external object name from the claim's
// namespace, the only tenancy guard a backend admin credential leaves (ADR
// 0002, "Tenancy naming").
package naming

// TenantFunc is the signature of Tenant, which NO1b implements as a function
// and then removes this type. Callers pass maxLen already reduced by any
// suffix they append afterwards (for example 2 for a rotation suffix "_a").
// The rules, the alphabet and the per-backend limits are in ADR 0002.
type TenantFunc func(ns, name string, maxLen int, sep string) string
