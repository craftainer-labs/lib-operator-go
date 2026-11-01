package common

import (
	"errors"
	"fmt"
)

// ManagementPolicy is one value of spec.managementPolicies, with Crossplane's
// meanings (ADR 0002, section 5).
//
// +kubebuilder:validation:Enum="*";Observe;Create;Update;Delete;LateInitialize
type ManagementPolicy string

// Values of ManagementPolicy.
const (
	// ManagementAll is "*": the same as every other value together. It stands alone.
	ManagementAll ManagementPolicy = "*"
	// ManagementObserve reads the external object and reports on it.
	ManagementObserve ManagementPolicy = "Observe"
	// ManagementCreate creates the external object when it does not exist.
	ManagementCreate ManagementPolicy = "Create"
	// ManagementUpdate changes the external object to match the spec, including
	// drift correction and credential rotation.
	ManagementUpdate ManagementPolicy = "Update"
	// ManagementDelete lets the controller delete the external object.
	ManagementDelete ManagementPolicy = "Delete"
	// ManagementLateInitialize records backend-assigned values in
	// status.lateInitialized.
	ManagementLateInitialize ManagementPolicy = "LateInitialize"
)

// maxPolicies is the maxItems of the field: the five explicit values.
const maxPolicies = 5

// Policies is a managementPolicies list resolved into what the controller may do.
type Policies struct {
	Observe, Create, Update, Delete, LateInitialize bool
	// ObserveOnly is true for exactly ["Observe"], the adoption mode.
	ObserveOnly bool
}

// ErrInvalidPolicies reports a managementPolicies list the CRD's CEL rules
// would have rejected (a stored object can still carry one).
var ErrInvalidPolicies = errors.New("invalid managementPolicies")

// ParseManagementPolicies resolves a managementPolicies list. A nil or empty
// list is the schema default, ["*"]. A list the CEL rules reject (unknown or
// duplicate values, "*" together with others, no Observe, more than five
// entries) returns ErrInvalidPolicies and the observe-only Policies, so a
// stored invalid list fails closed.
func ParseManagementPolicies(list []ManagementPolicy) (Policies, error) {
	failed := Policies{Observe: true, ObserveOnly: true}
	if len(list) == 0 {
		return allPolicies(), nil
	}
	if len(list) > maxPolicies {
		return failed, fmt.Errorf("%w: more than %d entries", ErrInvalidPolicies, maxPolicies)
	}
	var p Policies
	seen := map[ManagementPolicy]bool{}
	for _, v := range list {
		if seen[v] {
			return failed, fmt.Errorf("%w: duplicate %q", ErrInvalidPolicies, v)
		}
		seen[v] = true
		switch v {
		case ManagementAll:
			p = allPolicies()
		case ManagementObserve:
			p.Observe = true
		case ManagementCreate:
			p.Create = true
		case ManagementUpdate:
			p.Update = true
		case ManagementDelete:
			p.Delete = true
		case ManagementLateInitialize:
			p.LateInitialize = true
		default:
			return failed, fmt.Errorf("%w: unknown value %q", ErrInvalidPolicies, v)
		}
	}
	if seen[ManagementAll] && len(list) != 1 {
		return failed, fmt.Errorf("%w: \"*\" must stand alone", ErrInvalidPolicies)
	}
	if !seen[ManagementObserve] && !seen[ManagementAll] {
		return failed, fmt.Errorf("%w: an explicit list must contain Observe", ErrInvalidPolicies)
	}
	p.ObserveOnly = len(list) == 1 && seen[ManagementObserve]
	return p, nil
}

func allPolicies() Policies {
	return Policies{Observe: true, Create: true, Update: true, Delete: true, LateInitialize: true}
}

// DeletionPolicy decides whether a data kind's external object outlives its
// claim (ADR 0002, section 4).
//
// +kubebuilder:validation:Enum=Delete;Retain
type DeletionPolicy string

// Values of DeletionPolicy.
const (
	// DeletionDelete removes the external object with the claim.
	DeletionDelete DeletionPolicy = "Delete"
	// DeletionRetain leaves the external object and removes the finalizer.
	DeletionRetain DeletionPolicy = "Retain"
)

// DeletionPolicySource says where status.effectiveDeletionPolicy came from.
//
// +kubebuilder:validation:Enum=OperatorDefault;Fallback
type DeletionPolicySource string

// Values of DeletionPolicySource.
const (
	// SourceOperatorDefault is the operator's DEFAULT_DATA_DELETION_POLICY at the
	// claim's first reconcile.
	SourceOperatorDefault DeletionPolicySource = "OperatorDefault"
	// SourceFallback is Retain, pinned because the status was lost.
	SourceFallback DeletionPolicySource = "Fallback"
)
