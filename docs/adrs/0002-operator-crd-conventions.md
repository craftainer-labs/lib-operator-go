# 0002. Follow one set of CRD and lifecycle conventions in every operator

## Status

Accepted (2026-11-01)

The user approved the G-api decisions on 2026-11-01 (the outcomes are recorded
in "Decisions (G-api, 2026-11-01)" at the end). The ADR became Accepted when
NO1b's implementation merged, as `docs/adrs/README.md` requires.

## Context

craftainer-labs ships five Kubernetes operators, one per backend, each in its
own repository and its own CRD group under `craftainer.dev`:

| Repo | Group | Backend |
| --- | --- | --- |
| `operator-db` | `db.craftainer.dev` | PostgreSQL |
| `operator-s3` | `s3.craftainer.dev` | RustFS (admin API `/rustfs/admin/v3`) |
| `operator-mqtt` | `mqtt.craftainer.dev` | Mosquitto Dynamic Security |
| `operator-cache` | `cache.craftainer.dev` | Valkey / Redis ACL |
| `operator-oidc` | `oidc.craftainer.dev` | Keycloak Admin REST; any IdP through RFC 7591/7592 |

They share this library (`lib-operator-go`, interface I1 in the plan). Their
CRDs are public API: once teams write claims, a rename or a changed default
breaks them, and a convention that differs per operator makes the five feel
like five products. This ADR fixes the conventions before any API type or
`lib-operator-go` code is written. The draft kinds and fields are in
[`docs/crd-drafts.md`](../crd-drafts.md).

Forces and prior decisions that bear on it (plan decision log, 2026-10-30 and
NR4 of 2026-10-31):

- Namespace is the tenant. A cluster-scoped server object declares
  `allowedNamespaces`, and the object is invalid without it (EB-3).
- Backends cannot enforce tenant naming against an admin credential, so the
  naming function and the server resolver are the only tenancy guard (NR4 5).
- Credentials are written as Service Binding spec Secrets, with no binding
  runtime dependency, because Red Hat's Service Binding Operator is archived
  and the spec is still a working draft. COSI is not used (pre-alpha
  `v1alpha2`); the S3 kinds only borrow its names.
- The semantics of `managementPolicies` and the poll interval are borrowed
  from Crossplane managed resources, without adopting its providers.
  `deletionPolicy` is narrower than Crossplane's: it exists only on data kinds
  (section 4).
- NR4 found that overlapping credential rotation needs two phases on every
  backend that supports it, so I1's single `Rotate` cannot express it (NR4 3).
- Sources: [Kubernetes API conventions], [Service Binding spec], [COSI],
  [Crossplane managed resources], [Gateway API AllowedRoutes],
  [controller-runtime docs], [K8s RBAC good practices],
  [Pod Security Standards], [Go FIPS 140-3].

## Decision

We will follow the conventions below in every operator and in
`lib-operator-go`. "MUST", "SHOULD" and "MAY" are used as in RFC 2119.

### 1. API group, version and kind naming

- One group per operator repo, `<svc>.craftainer.dev` (user decision, plan
  2026-10-30). Kinds are PascalCase singular nouns with no `Craftainer`
  prefix, because the group already scopes them. Resources are the lower-case
  plural (`databases`, `buckets`).
- Server kinds: `Cluster<Kind>Server` (cluster scoped) and `<Kind>Server`
  (namespaced), where the group's own word replaces `<Kind>` (for example
  `ClusterDatabaseServer`, `ClusterObjectStore`, `ClusterMqttBroker`,
  `ClusterIdentityProvider`). Claim kinds are namespaced.
- Versions: every CRD starts at `v1alpha1`. We graduate to `v1` (EB-11)
  through a conversion webhook, never by editing `v1alpha1` in place:
  1. Add `v1` as the hub type; `v1alpha1` stays served and becomes a spoke
     with `Convertible` conversion functions (controller-runtime
     [`conversion` package][controller-runtime docs]). Both versions are
     served; the storage version moves to `v1` only after the conversion
     round-trips in tests (fuzzed, per the Kubernetes API-machinery
     convention).
  2. The webhook certificate comes from cert-manager (the template's
     convention), and the CRD's `conversion.strategy: Webhook` is set only in
     the release that ships `v1`. Until then `strategy: None` and a single
     version.
  3. `v1alpha1` is removed no sooner than two minor releases after `v1` ships,
     with the removal announced in the release notes. `v1alpha1` itself makes
     no compatibility promise, but we do not break it without a note.
  4. `v1` may be declared only when the group's envtest and the Chainsaw e2e
     pass on a real cluster, and the group's fields have survived one release
     of use.
- Field and enum names follow the [Kubernetes API conventions]:
  `lowerCamelCase` JSON names; enum values PascalCase (`Delete`, `Retain`,
  `ReadWrite`); acronyms keep their case in Go and are lower-cased at the
  start of a JSON name (Go `RedirectURIs` is JSON `redirectUris`, Go
  `CABundle` is JSON `caBundle`). Booleans are avoided when a state may
  grow a third value; an enum is used instead. References to objects are
  `<thing>Ref` with `name` (and `key` for a Secret key); a reference to a
  namespaced object never carries a namespace except the cluster-scoped
  server's `adminCredentialsSecretRef`, which names the operator namespace
  explicitly.
- Durations are `metav1.Duration` strings (`1h`, `30s`); sizes are
  `resource.Quantity`. Every string has `maxLength` and every list `maxItems`
  (CEL cost estimation requires bounds, and an unbounded list is a denial of
  service on the API server).
- `spec` and `status` are separate subresources. `status` is written only by
  the operator. Defaults are in the schema (`+kubebuilder:default`), not in a
  mutating webhook; the one exception is a data kind's `deletionPolicy`,
  whose default is operator configuration (section 4). Immutable fields use the CEL transition rule
  `self == oldSelf` ([CEL validation rules]). Every kind has printer columns
  `Ready`, `Synced`, `Server`, `Age`, and the category `craftainer`.
- The shared finalizer is `craftainer.dev/finalizer`. The shared annotations
  are `craftainer.dev/poll-interval`, `craftainer.dev/rotate` and
  `craftainer.dev/paused` (`"true"` stops reconciling without deleting;
  Crossplane's pause).
- The Go types shared by all operators (`ClaimCommon`, `DataClaim`,
  `Credentials` with `rotationPolicy`, `ClaimStatus`, `RotationStatus`,
  `AllowedNamespaces`, `ManagementPolicy`) live in `lib-operator-go`'s
  `pkg/apis/common`, with generated deep-copy; the operators embed them and
  do not copy them. This extends I1.

### 2. Server tiers and `allowedNamespaces`

- `Cluster<Kind>Server` (cluster scoped) holds the endpoint, the TLS settings
  (`tls.caBundle` from a Secret or ConfigMap key; TLS 1.2 minimum; there is no
  `insecureSkipVerify` field, EB-14), `adminCredentialsSecretRef` and
  `allowedNamespaces`. `<Kind>Server` (namespaced) has the same fields except
  `allowedNamespaces`, and its Secret reference names a Secret in its own
  namespace. Only the namespaced server is meant for a team that brings its
  own service. Both tiers ship in every group in `v1alpha1`.
- `adminCredentialsSecretRef` of a cluster server MUST name a Secret in the
  operator's own namespace; the controller refuses any other namespace with
  `Ready=False`, reason `AdminSecretNamespaceForbidden`. This stops a
  cluster server from being pointed at a tenant's Secret.
- `allowedNamespaces` has the shape of the Gateway API's
  `allowedRoutes.namespaces` ([Gateway API AllowedRoutes], type
  `RouteNamespaces`, with `FromNamespaces` values `All`, `Selector`, `Same`),
  minus `Same`:

  ```yaml
  allowedNamespaces:
    from: Selector            # All | Selector
    selector:                 # metav1.LabelSelector; required iff from: Selector
      matchExpressions:
        - {key: kubernetes.io/metadata.name, operator: In, values: [team-a, team-b]}
  ```

  - `from: All` allows every namespace. `from: Selector` allows the namespaces
    whose `Namespace` object labels match `selector`. Specific namespaces are
    selected with `matchExpressions` on the immutable
    `kubernetes.io/metadata.name` label, which the API server sets.
  - **It fails closed.** `allowedNamespaces` and its `from` are required on a
    cluster server: the CRD schema marks them `required`, so the API server
    rejects a `Cluster<Kind>Server` without them. There is no default for
    `from`, and no value that means "no namespace" or "same namespace" on a
    cluster server. CEL on `allowedNamespaces` rejects `selector` when
    `from: All`, a missing `selector` when `from: Selector`, and a
    `Selector` whose `matchLabels` and `matchExpressions` are both absent or
    empty (an empty `metav1.LabelSelector` matches everything; `from: All` is
    how that is said). `matchLabels` is a map and takes `maxProperties`,
    `matchExpressions` a list and takes `maxItems`. If a stored object
    nevertheless lacks the field or holds an empty selector (written before
    the schema, or past a bypassed webhook), the controller treats it as
    allowing no namespace. CEL cannot tell a non-empty selector that still
    matches every namespace (`DoesNotExist`, `NotIn` an unused value) from a
    narrow one, so the server author owns that. Prefer
    `kubernetes.io/metadata.name`: a selector on any other label is satisfied
    by whoever may `patch` namespaces, and the examples in the drafts that use
    a custom label assume that right is cluster-admin only. There is no
    migration: the earlier label-selector
    shape (where `{}` meant all) was only a draft and was never implemented or
    released.
  - A namespaced server has no `allowedNamespaces` field. It is implicitly
    `Same`: only claims in the server's own namespace may use it.
- A claim references its server with `serverRef: {kind, name}`. The kind
  enumerates the group's two server kinds. A namespaced server is always
  looked up in the claim's own namespace. `serverRef` is immutable.
- `server.Resolve(ctx, c, claimNS, ref)` is the only place that reads a
  server or its admin Secret. It returns `ErrNamespaceNotAllowed` for a
  namespace outside the selector, and the framework reports it as
  `Synced=False`, reason `NamespaceNotAllowed`, and requeues the claim at the
  drift-poll interval (no error backoff), because a `Namespace` label change
  triggers neither the claim nor the server. It reads the `Namespace` through
  the cache on every reconcile, so a label change takes effect on the claim's
  next reconcile, which is at the latest its next drift poll (10m by default,
  longer if `craftainer.dev/poll-interval` is raised); a change to the server
  re-queues its claims at once. `proxyURL` and the namespaced tier's egress
  have open questions at the end ("Open issues").

### 3. Claim spec and status shape

Every claim has these common spec fields (the drafts name them
`ClaimCommon`):

| Field | Type | Default | Notes |
| --- | --- | --- | --- |
| `serverRef` | `{kind, name}` | required | immutable |
| `managementPolicies` | list of `*`, `Observe`, `Create`, `Update`, `Delete`, `LateInitialize` | `["*"]` | section 5 |
| `deletionPolicy` | `Delete` or `Retain` | none in the schema; the operator's default (section 4) | data kinds only (`DataClaim`); credential-bearing kinds have no such field |
| `binding.secretName` | DNS-1123 subdomain | claim name | credential-bearing kinds only; immutable |
| `rotationPolicy` | object | absent | credential-bearing kinds only |

The data kinds are `Database` and `Bucket`. The credential-bearing kinds are
`DatabaseRole`, `BucketAccess`, `MqttUser`, `CacheUser` and `OidcClient`.
The `mqtt`, `cache` and `oidc` groups have no data kind, so no field or
setting of theirs is called `deletionPolicy`.

Every claim has this common status (`ClaimStatus`):

- `observedGeneration`: the `metadata.generation` last reconciled.
- `conditions[]`: `metav1.Condition`, `listType=map` on `type`. The
  operator MUST set `Ready`, `Synced` and `ServerReachable` on the first
  reconcile, each with `observedGeneration`, `reason` (CamelCase),
  `message` and `lastTransitionTime`, so `kubectl wait --for=condition=Ready`
  works and a missing condition always means "operator not running".
  Reasons used: `Available`, `Creating`, `Updating`, `Deleting`,
  `ReconcileSuccess`, `ReconcileError`, `ServerUnreachable`,
  `ServerNotFound`, `NamespaceNotAllowed`, `AdminSecretNamespaceForbidden`,
  `NameCollision`, `ObserveOnly`, `ManagementPolicyBlocked`,
  `RotationUnsupported`, `Rotating`.
- `externalName`: the derived external name (and `externalId` when the
  backend assigns one). Observe goes by these, never by re-deriving.
- `binding.name`: the binding Secret's name. Its presence makes the claim a
  Provisioned Service in the [Service Binding spec] sense.
- `rotation`: see section 8.
- `lastReconcileTime`.
- `lateInitialized`: see `LateInitialize` in section 5.
- Data kinds only: `effectiveDeletionPolicy` (`Delete` or `Retain`) and
  `deletionPolicySource` (`OperatorDefault` or `Fallback`), see section 4.

Conditions follow the [Kubernetes API conventions] (`Ready` summarises;
no condition is removed once set; the controller sets `Unknown` instead of
dropping a condition it cannot evaluate).

### 4. Deletion and `deletionPolicy`

- A finalizer holds the claim until the external object is handled. Then the
  binding Secret goes, which is owned by the claim.
- **Credential-bearing kinds are always deleted.** `DatabaseRole`,
  `BucketAccess`, `MqttUser`, `CacheUser` and `OidcClient` have no
  `deletionPolicy` field. When the claim is deleted, the operator deletes the
  external role, access key, user or client (all rotation generations,
  section 8), so no live credential outlives its claim. The one condition is
  that `managementPolicies` permits `Delete` (section 5).
- **Data kinds have `spec.deletionPolicy`** (`Retain` or `Delete`): `Database`
  and `Bucket`. `Delete` removes the external object; `Retain` leaves it and
  removes the finalizer. The field is optional and has no schema default.
- **The default comes from operator configuration.** Each operator reads the
  environment variable `DEFAULT_DATA_DELETION_POLICY` (the same name in all
  five operators; the library parses it into `Options.DefaultDeletionPolicy`
  for data kinds). Allowed values are `Retain` and `Delete`. Unset or empty
  means `Retain`. Any other value fails operator startup: the process logs the
  value and the allowed values and exits non-zero, so the operator never runs
  with a policy nobody chose. A value in `spec.deletionPolicy` overrides the
  default for that object. The operators in `mqtt`, `cache` and `oidc` have no
  data kind; they still accept the variable (so one Helm value serves all
  five) but do not use it.
- **The default is pinned at creation.** The first reconcile of a data
  claim records the resolved default in `status.effectiveDeletionPolicy` with
  `status.deletionPolicySource: OperatorDefault` (a status write), emits a
  Normal Event `DeletionPolicyResolved`, and only then adds the finalizer
  (a metadata patch) and starts any external action. A crash between the two
  leaves a pin without a finalizer, which the next reconcile completes; the
  reverse order would look like lost status. A claim
  deleted before its first reconcile has created nothing externally, so there
  is nothing for a policy to act on. Deletion honours the recorded value,
  never the current environment variable, so changing
  `DEFAULT_DATA_DELETION_POLICY` (and restarting the operator) affects only
  claims created afterwards. Reconciles never rewrite the recorded value. The policy in force is
  `spec.deletionPolicy` when set, otherwise `status.effectiveDeletionPolicy`;
  status alone is therefore not the whole answer, and `kubectl` users read
  both.
- **An explicit `spec.deletionPolicy` wins and is read at deletion.** It does
  not touch the pin: status keeps the pinned default, so removing the spec
  field later returns the claim to the value pinned at creation. Deletion
  reads the spec value itself, so an edit made just before deleting counts.
- **If status was lost** (an object restored from a backup or applied again
  without status; told apart from a first reconcile because the finalizer is
  already present while `status.effectiveDeletionPolicy` is empty),
  whether or not `spec.deletionPolicy` is set (the pin matters again if the
  field is removed): the original pin is unknown, so the controller pins `Retain`, the non-destructive value, whatever the current
  environment variable says. It records source `Fallback` and emits a Warning
  Event `DeletionPolicyRepinned` saying so. A deletion that arrives before
  that reconcile applies the same `Retain` when `spec.deletionPolicy` is
  unset (an implementation test covers the delete-before-repin path), and
  emits the same Warning. When `managementPolicies` lacks `Delete`, both
  Warnings are emitted (`DeletionPolicyRepinned`, then
  `ExternalObjectOrphaned`) and the object is left in place. This is the author's judgement
  call, not a user decision: the user's lean was to re-resolve from the
  environment variable, which could turn a `Retain` claim into a `Delete`
  one after a restore with a changed default.
- The operator logs the configured default once at startup.
- **Deletion outcome** (`P` is whether `managementPolicies` permits `Delete`):

  | Kind | `P` | Effective policy | External object | Event |
  | --- | --- | --- | --- | --- |
  | credential | yes | n/a | deleted | `Deleted` |
  | data | yes | `Delete` | deleted | `Deleted` |
  | data | yes | `Retain` | left in place | `Retained` |
  | data, status lost | yes | `Retain` (fallback) | left in place | Warning `DeletionPolicyRepinned`, then `Retained` |
  | any | no, and the list has `Create` or `Update` | any | left in place (orphaned) | Warning `ExternalObjectOrphaned` |
  | any | no, and the list is `["Observe"]` | any | left in place (never ours) | Normal `Retained` |

  Without `Delete` in `managementPolicies` the operator never deletes the
  external object, as in Crossplane, where an external resource without the
  `Delete` policy is orphaned ([Crossplane managed resources]). For a
  credential-bearing kind this takes precedence over the always-delete rule:
  the credential stays valid on the backend after the claim is gone. The
  Warning Event `ExternalObjectOrphaned` names the external object and says
  that its credential is still valid, and the operator logs it. The binding
  Secret is still deleted with the claim. Anyone who writes a policy list
  without `Delete` on a credential-bearing kind is choosing that outcome.
- `craftainer.dev/paused` stops spec changes, drift correction and rotation,
  not deletion: a deleted paused claim still runs the finalizer as above.
- For an `OidcClient` on a `DynamicRegistration` server that is orphaned, the
  operator keeps the `dcr-<claim uid>` Secret (section 9) instead of removing
  it, relabelled `craftainer.dev/orphaned=true`, so the client stays
  manageable through RFC 7592. It is the cluster administrator's to delete.
- Deleting a claim whose server is unreachable blocks on the finalizer with
  `Synced=False`, reason `ServerUnreachable`, and an Event; it never drops
  the finalizer silently. A user who must force it removes the finalizer by
  hand, which is a documented, deliberate act.
- A `Retain`ed or orphaned external object is not adopted by the next claim of
  the same name automatically; adoption needs `managementPolicies: [Observe]`.

### 5. `managementPolicies`

`spec.managementPolicies` takes Crossplane's full vocabulary, with its
meanings ([Crossplane managed resources], "managementPolicies"). The values:

| Value | Meaning |
| --- | --- |
| `*` | The default. Full control: the same as `Observe`, `Create`, `Update`, `Delete` and `LateInitialize` together. |
| `Observe` | The controller reads the external object and reports on it (`Synced`, `status`). It never changes the object. |
| `Create` | If the external object does not exist, the controller creates it from the spec. |
| `Update` | The controller changes the external object to match the spec: field changes, drift correction, and credential rotation (section 8). |
| `Delete` | The controller may delete the external object when the claim is deleted (section 4). |
| `LateInitialize` | The controller records backend-assigned values of optional fields that the spec leaves unset in `status.lateInitialized` (a map of field name to value), never in `spec` (Crossplane writes them into `spec`; we do not write `spec`). A kind with no such field accepts the value and does nothing. |

The default is `["*"]`. Valid combinations (CEL on the field; every other
list is rejected at admission):

- `*` stands alone: `["*"]`.
- An explicit list contains `Observe`. The controller always has to read the
  object to decide anything, so `Create`, `Update`, `Delete` and
  `LateInitialize` are each valid only together with `Observe`.
- Values are unique (`maxItems` 5) and the list is not empty. Crossplane
  treats an empty list as a pause; here the `craftainer.dev/paused`
  annotation pauses, so there is one way to do it.
- Any subset of `Create`, `Update`, `Delete`, `LateInitialize` plus `Observe`
  is valid. Common ones, as in Crossplane's table: `["Observe"]` (observe
  only, and the way to adopt an existing object), `["Observe", "Create",
  "Update"]` (never delete the external object; on a credential-bearing kind
  this orphans the live credential, section 4), `["Observe", "Create",
  "Delete"]` (never change it after creation).
- `rotationPolicy` needs `Update` (or `*`). The CEL rule is on the whole
  spec, so it also rejects an edit that drops `Update` while `rotationPolicy`
  is set. The `craftainer.dev/rotate` annotation is not validated at
  admission; on a claim without `Update` the controller ignores it and sets
  `Synced=False`, reason `ManagementPolicyBlocked`.

Behaviour:

- The controller looks the object up by `status.externalName` or, on first
  reconcile, by the derived name. If it is missing and `Create` is not
  permitted, the claim is `Synced=False`, reason `ObserveOnly` when the list is
  exactly `["Observe"]`, otherwise `ManagementPolicyBlocked`, with the message
  "not found"; it is not created. If the object has drifted and `Update` is
  not permitted, the drift is reported in `Synced` (same reasons) and not
  corrected.
- The binding Secret is written only when the backend can issue the credential
  without modifying the object (for the credential-less kinds it is not
  written). A claim that creates or rotates a credential needs `Create` or
  `Update` for that; an `["Observe"]` claim writes no Secret that needs a
  change on the backend.
- Observe reads the object's ownership marker (section 10): an object owned
  by another claim is refused with `NameCollision`.
- Without `Delete`, deletion orphans the external object, including the
  credential of a credential-bearing kind (section 4).

### 6. Drift polling

The controller re-queues every claim at `Options.DefaultPollInterval`
(default 10 minutes) to observe the external object and correct drift. The annotation
`craftainer.dev/poll-interval` (a Go duration, minimum `30s`, maximum `24h`;
invalid values fall back to the default and raise a Warning Event) overrides
it per resource. Watches use `GenerationChangedPredicate`-style filtering for
spec changes only plus the owned Secret, so status writes do not re-trigger
reconciles ([controller-runtime docs]). Drift on an `Observe`-only claim is
reported in `Synced`, never corrected. The poll jitters by up to 10% so many
claims do not hit the backend together.

### 7. Service Binding Secret

The Secret is written by `binding.WriteSecret(ctx, c, owner, name, typ,
provider, data)`:

- `type: servicebinding.io/<typ>` where `<typ>` is the binding type. The
  `data` always holds `type` (= `<typ>`) and `provider` (= `craftainer.dev`).
  The [Service Binding spec] (core 1.1.0) says a Provisioned Service Secret
  SHOULD hold `type` and RECOMMENDS `provider`; we always write both. The
  type's keys follow below.
- Secrets are owned by the claim (controller owner reference, so a `Delete`
  cascades) and carry `app.kubernetes.io/managed-by: <operator>` and the
  label `craftainer.dev/claim: <claim name>`. The operator's Secret cache is
  restricted to that managed-by label, so it never lists tenant Secrets that
  it does not own (EB-2).
- `WriteSecret` refuses to overwrite a Secret that exists without the claim as
  owner (`NameCollision`), so a claim cannot take over a user's Secret.
- Types: `postgresql`, `redis` (Valkey and Redis), `s3`, `mqtt`, `oidc`. Core
  1.1.0 defines no per-type key lists: it reserves only `host`, `port`, `uri`,
  `username`, `password`, `certificates` and `private-key` (with stated value
  requirements) and `type`/`provider`. The type names and every other key
  below (`database`, `sslmode`, `sslrootcert`, ...) are craftainer
  conventions; `postgresql` and `redis` reuse the names common to Service
  Binding implementations. Where a reserved name applies we use it with the
  spec's meaning (`host`, `port`, `uri`, `username`, `password`).

| Type | Keys |
| --- | --- |
| `postgresql` | `host`, `port`, `database`, `username`, `password`, plus `sslmode`, `sslrootcert` (CA PEM), `uri` |
| `redis` | `host`, `port`, `username`, `password`, plus `uri` (`rediss://`), `keyPrefix` (`<ns>:`), `sslrootcert` |
| `s3` | `endpoint`, `region`, `bucket`, `accessKeyId`, `secretAccessKey`, plus `prefix`, `pathStyle`, `sslrootcert` |
| `mqtt` | `host`, `port`, `protocol`, `username`, `password`, `clientId`, `topicRoot`, plus `sslrootcert` |
| `oidc` | `issuer`, `clientId`, `clientSecret` (confidential clients only), `tokenEndpoint`, `authorizationEndpoint`, `jwksUri`, plus `userinfoEndpoint` |

Keys are the Secret's `data` keys (base64 on the wire, bytes in Go). The
non-credential keys are repeated in the Secret so a workload needs one mount.

### 8. Two-phase credential rotation (replaces I1's `Rotate`)

I1 had a single `Rotate(ctx, cur) (binding.Data, error)`. NR4 (3) showed that
every backend that can overlap credentials does it in two steps: issue the
new credential while the old one still works, then retire the old one after a
grace period in which consumers pick up the new Secret. A single call cannot
express that without the operator sleeping. We replace it with two methods on
an optional interface. `External` loses `Rotate`; an `External` that is also a
`Rotator` supports rotation.

```go
package lifecycle

// Generation names one credential generation on the backend: "a" or "b" for a
// two-slot backend, an access key or client id for the others. The External
// chooses it; the framework stores it in status.rotation.
type Generation string

// RotationState is what the framework persisted in status.rotation.
type RotationState struct {
	Active   Generation // held by the binding Secret; "" before the first rotation
	Previous Generation // still valid, awaiting RotateFinish; "" outside a grace period
	Sequence int64      // completed RotateBegin calls: lets an External derive a
	                    // deterministic next generation, so a retry after a crash
	                    // issues the same one instead of leaking another
}

// Rotator is implemented by an External whose backend can rotate credentials.
type Rotator[S, O any] interface {
	External[S, O] // Observe, Create, Update, Delete (no Rotate)

	// RotateBegin issues a new generation and leaves the active one valid.
	// It returns the new generation's binding data and its Generation, and is
	// idempotent for the same RotationState.
	RotateBegin(ctx context.Context, spec S, cur O, st RotationState) (binding.Data, Generation, error)

	// RotateFinish retires st.Previous once the grace period has elapsed. It
	// succeeds when st.Previous is already gone.
	RotateFinish(ctx context.Context, spec S, cur O, st RotationState) error
}
```

`Claim` gains `GetRotationPolicy() *RotationPolicy`, `GetRotationStatus()
RotationStatus` and `SetRotationStatus(RotationStatus)` (see
`pkg/lifecycle/doc.go`). `ErrRotationUnsupported` is dropped: an `External`
that is not a `Rotator` simply cannot rotate, and a claim that sets
`rotationPolicy` for it gets `Synced=False`, reason `RotationUnsupported`.

The framework drives a small state machine from `status.rotation`
(`{active, previous, sequence, retireAfter, lastRotated, lastTrigger}`):

1. **Due.** A rotation is due when `rotationPolicy.interval` has elapsed since
   `lastRotated`, or when the annotation `craftainer.dev/rotate` holds a value
   different from `lastTrigger` (any new string triggers one rotation).
2. **Begin.** Call `RotateBegin` with the current `RotationState`. Write the
   returned data to the binding Secret first, then persist
   `status.rotation` (`previous = active`, `active = new`, `sequence++`,
   `retireAfter = now + gracePeriod`, `lastRotated`, `lastTrigger`). A crash
   between the two repeats `RotateBegin`, which is idempotent.
3. **Grace.** The claim stays `Ready=True`; `Synced` reason `Rotating`. The
   controller re-queues at `retireAfter`.
4. **Finish.** At `retireAfter`, call `RotateFinish`, then clear `previous`
   and `retireAfter`. A new rotation is not begun while `previous` is set.

`rotationPolicy` is `{interval: Duration (optional, min 1h, max 8760h), gracePeriod:
Duration (default 1h, min 5m, max 168h)}`. Events: `RotationStarted`,
`RotationFinished` (Normal), `RotationFailed` (Warning). A backend with no
overlap implements `RotateBegin` as the hard cut and `RotateFinish` as a
no-op; the Event reason is then `RotatedWithoutOverlap` so operators of the
workloads know the old secret died immediately.

Per backend (from NR4; the live checks that belong to each operator's NOxb
item are not repeated here):

| Backend | Pattern |
| --- | --- |
| PostgreSQL | A `NOLOGIN` owner role `<base>` owns the objects. Two `LOGIN` roles `<base>_a` and `<base>_b` are members of it with `ALTER ROLE ... SET role = <base>`. `RotateBegin` sets a new password on the inactive login role; `RotateFinish` sets the retired one to `NOLOGIN` and `PASSWORD NULL`, and optionally terminates its sessions (`terminateOnRetire`, default false). `Generation` is `a` or `b`. |
| RustFS | Several service accounts per user. `RotateBegin` creates a new service account (secret key at most 40 characters) with the user's policy; `RotateFinish` deletes the previous one. `Generation` is the access key id. |
| Valkey | Several passwords per ACL user. `RotateBegin` adds a `>password`; `RotateFinish` removes the previous by its hash (`!<sha256>`). `Generation` is the password's SHA-256. |
| Mosquitto | Two clients `<base>-a` and `<base>-b`, each with one password and the same role. `RotateFinish` deletes the retired client. |
| Keycloak | The PREVIEW `client-secret-rotation` feature plus a realm client policy (operator-oidc's own stack enables it): `RotateBegin` regenerates the secret and Keycloak keeps the rotated one valid; `RotateFinish` invalidates the rotated secret. |
| Dynamic client registration (RFC 7591/7592) | No overlap in general: `RotateBegin` re-registers (PUT to the registration endpoint) and `RotateFinish` is a no-op, unless the IdP documents otherwise. |

### 9. Dynamic client registration state (OIDC)

For a server of type `DynamicRegistration`, the operator stores the
server-assigned `client_id`, `registration_client_uri` and the registration
access token (RAT) of every client (NR4 4). The RAT is the credential that
manages the client, so it is a Secret, not status. The RAT is re-stored after
every PUT, because RFC 7592 lets the server return a new one.

- Where: one Secret per client, `dcr-<claim uid>`, in the **operator
  namespace**, labelled with the claim's namespace, name and UID, with keys
  `clientId`, `registrationClientUri` and `registrationAccessToken`. It is
  removed by the claim's finalizer (a cross-namespace owner reference is not
  allowed). The tenant never sees the RAT.
- The claim's `status.externalId` is the `client_id` and
  `status.registrationClientUri` the (non-secret) URI. Observe goes by these,
  never by name: a client's name is not unique at the IdP.
- Observe is `GET registration_client_uri` with the RAT; a 401 or 404 is
  `Synced=False`, reason `RegistrationLost` (the operator never re-registers
  silently, since that would change the `client_id` that the workload was
  given).

### 10. Tenancy naming

`naming.Tenant(ns, name string, maxLen int, sep string, hl naming.HashLen)
string` derives every external name from the claim's namespace and name. It is the only tenancy
guard besides `server.Resolve` (NR4 5): an admin credential can name anything,
so a wrong or colliding name is a cross-tenant incident. It has 100%
statement and branch coverage and a property test.

Inputs are Kubernetes-validated: `ns` is a DNS-1123 label, `name` a DNS-1123
subdomain; both are lower case and contain only `a-z`, `0-9`, `-` (and `.` in
`name`). They contain no `_`, `/`, `:` or `~`. The function does not
re-validate them.

Rules:

1. `hl` is `naming.HashShort` (10 characters, 50 bits) or `naming.HashLong`
   (16 characters, 80 bits). `h` is the first `hl` characters of the
   lower-case, unpadded base32 (RFC 4648, alphabet `a-z2-7`, 5 bits per
   character) of `SHA-256(len(ns) ":" ns ":" name)`. The length prefix makes
   the input unambiguous. SHA-256 is FIPS-approved (EB-14). The short hash is
   a prefix of the long one for the same input.
2. `readable = ns + sep + name`.
3. **The hash is always appended.** The result is
   `cut(readable, maxLen - len(sep) - hl) + sep + h`, where `cut` truncates
   and then strips trailing `-`, `.`, `_`, `/` and `:` so no empty segment or
   double separator results. The result is never longer than `maxLen`, and
   there is no branch that returns `readable` unchanged.
4. `Tenant` panics (a programmer error found by the first test run, never by
   user input) when `maxLen < hl + len(sep) + 5` (16 with `HashShort`, 22
   with `HashLong`), when `sep` is empty or longer than one byte, when `hl`
   is neither constant, or when `ns` or `name` is empty.
5. **Which length a backend uses.** A backend object that can hold the
   ownership marker below uses `HashShort`. A backend object that cannot
   hold one uses `HashLong`, because the name is then the only barrier. The
   markerless objects are: Valkey ACL users, RustFS users and service
   accounts (the S3 credential kinds), and DCR client names. Every other
   row of the table below has a marker and uses `HashShort`. A DCR client is
   found by the `client_id` the IdP assigned (`status.externalId`), never by
   name, so a name clash cannot make the operator adopt another tenant's
   client. The long hash there is not needed for safety; the rule is applied
   to every markerless object so it stays mechanical. `naming` exports the
   two constants only; each operator carries a test asserting the `hl` it
   passes for each of its objects, because `naming` does not know its
   callers.
6. `cut` keeps at least 5 characters of `readable` (`maxLen - len(sep) -
   hl >= 5`). `ns` starts with an alphanumeric character, so the visible
   prefix is never empty after stripping.

Example: `Tenant("team-a", "orders", 63, "_", HashShort)` is
`team-a_orders_gdeqxvirep`, `Tenant("team-a", "orders", 63, "-", HashShort)`
is `team-a-orders-gdeqxvirep` (the hash does not depend on `sep`), and
`Tenant("team-a", "orders", 128, ":", HashLong)` is
`team-a:orders:gdeqxvirepf7uebv`.

Why these rules are collision-safe:

- The hash input is unambiguous because of the length prefix, so two different
  `(ns, name)` pairs hash independently even when `ns + sep + name` is the same
  string (`("team-a", "db")` and `("team", "a-db")` both read `team-a-db` on a
  `-` backend). Every result ends in `sep` and a hash, so no result is a bare
  tenant-chosen string, and a tenant cannot pick a claim name that lands on
  another tenant's object without matching its hash.
- Two pairs collide only if their hashes match and their visible prefixes
  match: a second-preimage search against a chosen victim of about 2^50
  SHA-256 evaluations for `HashShort` and about 2^80 for `HashLong`. Where the
  backend can hold an ownership marker, the marker is the second barrier, so
  50 bits suffice. Every external object created by an operator carries the
  marker (`craftainer.dev/claim=<ns>/<name>/<uid>`) where the backend can
  hold one (PostgreSQL `COMMENT ON ROLE/DATABASE`, S3 bucket tags, Keycloak
  client attributes, Mosquitto client `textdescription`). Valkey ACL users,
  RustFS users and service accounts, and DCR client names cannot hold one, so
  for them the name is the only barrier and they use the 80-bit hash. Create
  and Observe refuse an object whose marker names a different UID
  (`NameCollision`).

Callers subtract any suffix they append from `maxLen` (the rotation suffixes
`_a`, `_b`, `-a`, `-b` are two bytes). Per backend:

| Backend object | `sep` | `hl` | `maxLen` passed | Limit and where it comes from |
| --- | --- | --- | --- | --- |
| PostgreSQL database, owner role | `_` | `HashShort` | 63 (login roles: 61, then `_a`/`_b`) | identifier limit of 63 bytes (`NAMEDATALEN` 64 minus the terminator, [PostgreSQL docs]); always quoted with `pgx.Identifier` since `-` is legal only quoted |
| S3 bucket (RustFS) | `-` | `HashShort` | 63 | bucket names are 3 to 63 lowercase characters, digits, hyphens; every result ends in the hash, which is alphanumeric |
| S3 user and service account (RustFS) | `-` | `HashLong` | 63 (not verified, see below) | service-account secret keys at most 40 characters (NR4); the access-key and user-name bounds are not in the NR4 findings |
| MQTT username and client id (Mosquitto) | `-` | `HashShort` | 128, then `-a`/`-b` (126) | the MQTT string limit is 65535 bytes; 128 is our cap |
| MQTT topic root | `/` | n/a | not derived by `Tenant` | the root is `<ns>/` (no hash, no truncation), so tenant ACL patterns are `<ns>/...` |
| Valkey ACL username | `:` | `HashLong` | 128 | no hard limit; our cap. Key pattern `~<ns>:*`, channel pattern `&<ns>:*` (these patterns use the namespace, not the hashed name) |
| Keycloak client id | `-` | `HashShort` | 255 | `CLIENT_ID` column is 255 characters (verify in NOxb) |
| DCR client name | `-` | `HashLong` | 255 | free text; our cap |
| Kubernetes Secret and object names | n/a | n/a | 253 | DNS-1123 subdomain; not derived by `Tenant` |

The two limits marked "not verified" are added to that operator's NOxb open
checks. If a backend turns out shorter, only the `maxLen` constant changes:
the rules do not. The long hash needs `maxLen` of at least 22; every
`HashLong` row passes 63 or more (Valkey rotates by adding passwords to the same user, and the rotation
generations of RustFS service accounts are separate access keys, so neither
appends a name suffix and no budget is subtracted), so that holds unless the RustFS user-name
limit turns out below 22, in which case the item escalates instead of
shortening the hash.

A claim can never choose an external name: there is no `spec.name` override,
so the derived name is the only path from a claim to a backend object.
`Bucket`, `DatabaseRole` and similar kinds add a CEL rule on `metadata.name`
that rejects characters outside the backend's set (for example, a `Bucket`
name containing `.`).

### 11. Events and audit (EB-4)

- Each state change emits a Kubernetes Event on the claim through the
  controller's `EventRecorder`: Normal `Created`, `Updated`, `Deleted`,
  `Observed`, `Retained`, `DeletionPolicyResolved`, `RotationStarted`,
  `RotationFinished`, `RotatedWithoutOverlap`; Warning `ServerUnreachable`,
  `NamespaceNotAllowed`, `NameCollision`, `ManagementPolicyBlocked`,
  `ExternalObjectOrphaned`, `DeletionPolicyRepinned`, `ReconcileError`, `RotationFailed`. The Event `reason` equals the
  condition reason where one exists.
- The same facts go to the structured JSON log (who: the controller and, for
  a user-driven change, the last `managedFields` manager; what: kind,
  namespace, name, UID, generation, `externalName`, action, result; when;
  source: the server ref and endpoint host; before and after: the names of the
  changed spec fields and a SHA-256 of the old and new spec, never their
  values). The Kubernetes API server's audit log holds the request's source IP.
- Credentials, tokens and Secret data are never in Events, logs, conditions or
  status. The tests assert that for the binding data.

### 12. RBAC least privilege (EB-2)

- The operator's ClusterRole lists exact resources and verbs (no `*`, no
  `escalate`, `bind` or `impersonate`). It has: the group's CRDs with `get`,
  `list`, `watch`; `status` and `finalizers` with `update` and `patch`; the
  `Namespace` `get`, `list`, `watch` (for `allowedNamespaces`); `Events`
  `create`, `patch`; `Secrets` `get`, `list`, `watch`, `create`, `update`,
  `patch`, `delete` (Kubernetes RBAC cannot restrict by label, so the
  controller's cache is restricted to its own label to keep the data it
  reads small). The admin Secrets of servers are read through a **Role** in
  the operator namespace, not the ClusterRole. Leader election uses a Role on
  `Leases` in the operator namespace.
- The tenant-facing roles are aggregated ClusterRoles
  (`<group>-edit`, `<group>-view`) labelled `rbac.authorization.k8s.io/
  aggregate-to-edit` and `-view` for claims only. Servers are cluster-admin
  only. A team's RBAC to its own namespace therefore decides who can create
  claims, and the IdP-group-to-role mapping is the cluster's (EB-2).
- The operator pod meets Pod Security `restricted` (EB-10): non-root, read-only
  root filesystem, all capabilities dropped, `seccompProfile: RuntimeDefault`.

### 13. FIPS (EB-14)

- Releases are built with `GOFIPS140=v1.0.0` (the frozen validated module;
  `latest` is not used for releases) and run with `GODEBUG=fips140=on`.
  The library uses only the standard library's crypto (`crypto/tls`,
  `crypto/sha256`, `crypto/rand`) and no cgo crypto, so the module in use is
  the Go Cryptographic Module ([Go FIPS 140-3]).
- Every connection to a backend uses a configurable CA bundle (the server's
  `tls`), TLS 1.2 or later, and never `InsecureSkipVerify`. The backend
  clients chosen in NR4 (`pgx`, `minio-go`, `paho.golang`, `valkey-go`,
  `gocloak`) use `crypto/tls`; none is cgo.
- A CI job runs the library's tests once with `GODEBUG=fips140=only`, which
  makes a non-approved algorithm fail the test instead of passing silently.
  PostgreSQL passwords are SCRAM-SHA-256 only (never MD5).

## Consequences

- Five operators and one library share one vocabulary, so a user who knows
  `Database` knows `Bucket`, and `lib-operator-go` can implement the
  lifecycle once. Changing a convention later is a breaking change to five
  public APIs, which is why this is decided before the code.
- The two-phase `Rotator` is a larger interface than the single `Rotate` and
  needs persisted rotation state in every credential-bearing kind. In return
  a rotation never cuts off a running workload on a backend that can overlap,
  and the hard-cut backends are visible as such.
- `Tenant` costs readability (every external name ends in a hash) in exchange
  for one rule with no branches. A name's hash is not secret and a database
  administrator can derive it. Backends without an ownership marker (Valkey
  ACL users, RustFS users and service accounts, DCR client names) use the
  80-bit hash, which takes names six characters longer than the 50-bit one.
- Fail-closed `allowedNamespaces` means a cluster server cannot be created
  without choosing `All` or a `Selector`, which is a deliberate extra step. A
  team that wants only its own namespace uses a namespaced server.
- Credential-bearing kinds cannot be retained: a deleted claim never leaves a
  live credential behind, unless its author omits `Delete` from
  `managementPolicies`, which orphans it with a Warning Event. A user who
  wants to keep a role or access key creates it outside the operator.
- The default deletion policy of data kinds is a deployment setting, not part
  of the CRD, so two clusters can differ. The effective value is in each
  claim's status.
- The operator namespace holds DCR registration Secrets, so a compromise of
  the operator namespace compromises every registered client; this is the
  same trust as the admin credential and is documented.
- `v1alpha1` gives no compatibility promise, but the conversion-webhook path
  to `v1` adds a webhook and cert-manager dependency at graduation.
- Open items: the values marked "not verified" and the 17 live checks from
  NR4 belong to the operators' NOxb items, not to this ADR.

## Decisions (G-api, 2026-11-01)

The user decided the sixteen G-api choices on 2026-11-01. The sections above
are written to these outcomes.

1. Two-phase rotation replaces `Rotate` (section 8): accepted as proposed.
2. `allowedNamespaces`: the Gateway API shape `from: All | Selector` with
   `selector`, required on cluster servers so it fails closed; namespaced
   servers are implicitly `Same` (section 2).
3. Both server tiers ship in `v1alpha1`.
4. `managementPolicies`: Crossplane's full set (`*`, `Observe`, `Create`,
   `Update`, `Delete`, `LateInitialize`), default `["*"]` (section 5).
   The author made these calls while writing it down, and the user did not
   decide them: `LateInitialize` records values in `status.lateInitialized`,
   not `spec`; an explicit list must contain `Observe`; an empty list is
   invalid; without `Delete` credentials are not deleted (section 4); a
   paused claim is still deleted; a `Selector` may not be empty; an orphaned
   DCR client keeps its registration Secret.
5. Deletion: credential-bearing kinds are always deleted and have no
   `deletionPolicy`; data kinds keep `spec.deletionPolicy`, whose default is
   the operator environment variable `DEFAULT_DATA_DELETION_POLICY`
   (`Retain` when unset; an invalid value fails startup) and is reported in
   `status` (section 4). Follow-up decision (GA1b, 2026-11-01): the default is
   pinned at creation. The first reconcile records it in
   `status.effectiveDeletionPolicy`, and deletion honours the recorded value,
   not the current variable; an explicit `spec.deletionPolicy` wins and is
   read at deletion. For a claim whose status was lost the controller emits a
   Warning Event; that is the author's judgement call,
   not the user's: a claim whose status was lost is pinned to `Retain`
   (source `Fallback`), not re-resolved from the variable, so a restore cannot
   turn `Retain` into `Delete`.
6. Naming: the hash is always appended; the readable-when-safe branch and its
   exclusion rule are gone; the ownership marker and per-backend length limits
   stay (section 10). Follow-up decision (GA1b, 2026-11-01): backends with no
   ownership marker (Valkey ACL users, RustFS users and service accounts, DCR
   client names) use an 80-bit hash (16 base32 characters); marker-bearing
   backends keep the 50-bit hash.
7. Separators per backend as proposed (section 10).
8. Binding type `redis` for Valkey (section 7).
9. DCR state lives in the operator namespace (section 9).
10. Durations are `metav1.Duration`.
11. Shared API types live in `lib-operator-go`'s `pkg/apis/common`. This
    extends I1 (section 1).
12. The `craftainer.dev/paused` annotation is included.
13. Graduation to `v1` by the criteria in section 1.
14. FIPS build `GOFIPS140=v1.0.0` for releases, plus the `fips140=only` CI job.
15. Audit is the structured JSON log plus Events, with no separate sink
    (section 11).
16. Kind names and the five groups' drafts in
    [`docs/crd-drafts.md`](../crd-drafts.md), including the COSI-aligned S3
    names.

## Open issues

New design questions found in review; not decided here. Decision 4 closed
the one on `Observe` with `rotationPolicy`: rotation needs `Update`
(section 5).

- Binding CA key: the spec reserves `certificates` (PEM chain) for TLS trust,
  while section 7 and the drafts use `sslrootcert`. Offer both, or only
  `certificates`?
- A cache or broker without TLS inside a namespace (a test instance) is not
  expressible, because EB-14 requires TLS to every backend. Is a documented
  exception wanted?
- `proxyURL` may carry credentials (`http://user:pass@proxy`) into a plain spec
  field; reject userinfo by CEL, or add a Secret reference for proxy auth?
- A namespaced server lets a tenant aim the operator at any host, port,
  endpoint, issuer or proxy; no egress or SSRF restriction is drafted
  (for example a deny list for link-local and cluster-internal ranges, or
  gating the namespaced tier).
- `External.Create` returns only `binding.Data`, so a backend-assigned
  `externalId` has no explicit channel to the framework; the per-kind
  `host`, `port` and `sslMode` fields also do not reach `server.Connection`.

[Kubernetes API conventions]: https://github.com/kubernetes/community/blob/master/contributors/devel/sig-architecture/api-conventions.md
[Service Binding spec]: https://github.com/servicebinding/spec
[COSI]: https://github.com/kubernetes-sigs/container-object-storage-interface
[Crossplane managed resources]: https://docs.crossplane.io/latest/managed-resources/managed-resources/
[controller-runtime docs]: https://pkg.go.dev/sigs.k8s.io/controller-runtime
[Gateway API AllowedRoutes]: https://gateway-api.sigs.k8s.io/reference/api-spec/1.6/spec/
[CEL validation rules]: https://kubernetes.io/docs/tasks/extend-kubernetes/custom-resources/custom-resource-definitions/#validation-rules
[K8s RBAC good practices]: https://kubernetes.io/docs/concepts/security/rbac-good-practices/
[Pod Security Standards]: https://kubernetes.io/docs/concepts/security/pod-security-standards/
[Go FIPS 140-3]: https://go.dev/doc/security/fips140
[PostgreSQL docs]: https://www.postgresql.org/docs/current/sql-syntax-lexical.html#SQL-SYNTAX-IDENTIFIERS
