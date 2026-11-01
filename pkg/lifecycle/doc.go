// Package lifecycle is the reconcile framework every operator controller calls:
// finalizer, deletionPolicy, managementPolicies, drift polling, two-phase
// credential rotation and the Ready, Synced and ServerReachable conditions
// (ADR 0002). An operator implements External (and Rotator for a backend that
// can rotate) for its backend and Claim for its API types, and calls Reconcile
// from its controller.
//
// A typical controller:
//
//	conn, err := server.Resolve(ctx, c, claim.Namespace, claim.Spec.ServerRef,
//		server.WithOperatorNamespace(ns))
//	var ext lifecycle.External[Spec, Obj]
//	if err == nil {
//		ext = newBackend(conn, claim) // sets the claim's external name and id
//	}
//	return lifecycle.Reconcile(ctx, c, claim, claim.Spec, ext, lifecycle.Options{
//		Class: lifecycle.ClassData, ResolveErr: err, Recorder: recorder, ...
//	})
//
// Reconcile never reads the server itself: ResolveErr carries the result of
// server.Resolve, so a missing server, a namespace outside allowedNamespaces
// and an unreachable backend become conditions and Events instead of errors.
// The claim's external name and id are set by the External on the claim it was
// built with (SetExternalName, SetExternalID); Reconcile persists them with the
// rest of the status.
package lifecycle
