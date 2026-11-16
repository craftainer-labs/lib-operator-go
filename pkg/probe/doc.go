// Package probe writes the status of a server kind (ADR 0002, "Server
// status"): serverVersion, lastProbeTime, observedGeneration and the Ready and
// ServerReachable conditions. A server kind's CRD promises them
// (common.ServerStatus) and nothing else fills them in.
//
// An operator implements Prober with the backend client it already caches and
// wires the Reconciler in a few lines:
//
//	err := (&probe.Reconciler{
//		Client: mgr.GetClient(),
//		New:    func() probe.Server { return &v1alpha1.DatabaseServer{} },
//		Prober: probe.ProberFunc(func(ctx context.Context, srv server.Server, conn server.Connection) (string, error) {
//			return backendFor(srv, conn).Version(ctx) // the operator's own cached client
//		}),
//		Options: probe.Options{OperatorNamespace: ns},
//	}).SetupWithManager(mgr)
//
// Reconciler is the only entry point. The helper owns the schedule (a health
// check every healthCheckInterval plus up to 10% jitter, whether or not the
// last probe failed), the conditions and the status write. It also watches the
// Secrets and ConfigMaps a server names, so fixing one probes at once; the
// operator needs get, list and watch on both, and patch on the servers'
// status. It writes the status only when something changed, so a healthy
// server costs no write per probe; see Options.RefreshInterval for
// lastProbeTime.
//
// A cloud server has no admin Secret to build a server.Connection from, and
// its probe also reads what the cloud knows (an endpoint, a resource id). Such
// a kind embeds common.CloudServerCommon and implements server.CloudServer;
// the operator implements CloudProber and sets Reconciler.NewCloud and
// Reconciler.CloudProber instead of New and Prober. The helper then writes
// ServerStatus.Discovered with the version. An existing Prober is untouched.
//
// Conditions carry fixed, coarse messages. The raw error of a failed probe
// goes to the controller's log, never into the status, where any user who can
// read the server would see it.
package probe
