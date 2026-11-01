// Package naming derives every external object name from the claim's
// namespace, the only tenancy guard a backend admin credential leaves (ADR
// 0002, "Tenancy naming"). Tenant always appends a hash; callers subtract any
// suffix they add afterwards from maxLen.
package naming
