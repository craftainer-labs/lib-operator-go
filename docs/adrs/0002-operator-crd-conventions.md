# 0002. Follow one set of CRD and lifecycle conventions in every operator

## Status

Accepted (2026-11-01)

The user approved the G-api decisions on 2026-11-01 (the outcomes are recorded
in "Decisions (G-api, 2026-11-01)" at the end). The ADR became Accepted when
NO1b's implementation merged, as `docs/adrs/README.md` requires.

Section 16 (cloud providers) is a later addition. The user decided its eleven
questions on 2026-11-16 (16.8). It is **Accepted for the parts NK0 implements**
in `pkg/cloud`, `pkg/lifecycle`, `pkg/server` and `pkg/probe` (16.9 lists them) and stays **Proposed** for
the rest, which the operators' NK items implement, as `docs/adrs/README.md`
requires.

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
  (`tls.caBundle` from a Secret or ConfigMap key, `key` defaulting to `ca.crt`; TLS 1.2 minimum; there is no
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
  `ReconcileSuccess`, `ReconcileError`, `InvalidServer`, `ServerUnreachable`,
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
  The library retries a failing deletion with the controller's error backoff
  for one hour from the deletion timestamp. After that the finalizer still
  stays, but the reconcile stops returning errors: the claim gets `Synced=False`
  and `Ready=False` with reason `DeletionBlocked`, one Warning Event
  `DeletionBlocked` that names the finalizer to remove by hand, and a requeue at
  the poll interval. The deletion completes by itself once the server or backend
  recovers.
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

A Secret's `type` is immutable. When the claim's binding Secret exists with
another type (someone edited it), the library deletes it and creates it again
with the same keys and the right type, once, instead of reporting the drift on
every reconcile. The new Secret is built from `data` alone, so keys of the old
one that `data` lacks are not kept (the lifecycle repair passes the old keys in
`data`). If the old Secret is still Terminating (a finalizer holds it), the
create fails with AlreadyExists, which is treated as a retry on the next
reconcile, not as `ErrNameCollision`.

Known limit: the binding Secret is classified from the operator's cache, and
only a Missing or Drifted result is confirmed through the API reader, so a
cached "present" that has just been deleted or replaced shows up one poll late.

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
   and `retireAfter`. A new rotation is not begun while `previous` is set. A
   `craftainer.dev/rotate` value that arrives during a grace period is consumed
   and rejected (`lastTrigger` is updated, Warning Event `RotationDeferred`),
   not queued: the user sets a new value after the grace period ends. A
   `gracePeriod` of 0 (not admitted by the schema's minimum, but possible in a
   stored object) is a hard cut: `RotateFinish` runs in the same reconcile,
   right after the binding Secret is written.

`rotationPolicy` is `{interval: Duration (optional, min 1h, max 8760h), gracePeriod:
Duration (default 1h, min 5m, max 168h)}`. Events: `RotationStarted`,
`RotationFinished` (Normal), `RotationFailed` (Warning). A backend with no
overlap implements `RotateBegin` as the hard cut and `RotateFinish` as a
no-op; the Event reason is then `RotatedWithoutOverlap` so operators of the
workloads know the old secret died immediately.

Rotation is safe against a failed status write. After `RotateBegin` and the
binding Secret write, the library marks the Secret with the annotations
`craftainer.dev/rotation-sequence` and `craftainer.dev/rotation-generation`,
and only then records the rotation in `status.rotation`. If that status write
fails, the next reconcile finds a mark whose sequence is `status.rotation.sequence + 1`,
adopts the generation it names and records it, without calling `RotateBegin`
again. An Observe-only claim (`managementPolicies: [Observe]`) never calls
`RotateBegin` or `RotateFinish`. A binding Secret that goes missing while a
grace period runs makes the reconcile return when the grace period ends (the
shortest positive requeue wins); once it has ended, the previous generation is
retired and the Secret is recreated by a rotation in the same reconcile.

Two optional interfaces cover credentials with no secret to rotate (added with
NKlib-2; section 16.4's IAM mode is the first user). They change nothing for an
External that does not implement them, and a `Rotator`'s rotation is never
altered by them:

- `NonRotator` (`RotationNotApplicable() bool`) is for an External that is not
  a `Rotator` because rotation does not apply. When it returns true, the
  `craftainer.dev/rotate` annotation is acknowledged (its value is recorded in
  `status.rotation.lastTrigger`, so it fires once) with a Warning Event
  `RotationNotApplicable`, a `rotationPolicy` is ignored, and `Synced` stays
  True. When it returns false, or the interface is absent, the claim gets
  `Synced=False`, reason `RotationUnsupported`, as before. It is not consulted
  for a `Rotator`.
- `BindingRebuilder` (`RebuildBinding(ctx, spec, cur) (binding.Data, error)`)
  is for an External whose binding Secret holds no secret of its own. When the
  binding Secret is missing, the lifecycle writes the data it returns (Normal
  Event `BindingRepaired`) instead of `BindingSecretMissing`, whether or not
  the External is a `Rotator`, without the `Update` policy and without calling
  `Create`, `Update` or `RotateBegin`. It is only called for a missing Secret.
  An External whose Secret holds an issued credential must not implement it:
  the rotate path recreates that one. Empty data is `ErrNoBindingData`.

Known limits: if marking the Secret fails after its data was written, the next
reconcile calls `RotateBegin` again and a second credential is issued (closing
that window needs a `binding` write option that sets annotations, a G-api
change); and if the Secret is repaired or recreated between a failed status
write and the next reconcile, the adopted generation can disagree with the data.

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
   double separator results. `maxLen` counts bytes, as the backends and
   Kubernetes do, and `cut` never splits a multibyte UTF-8 character: it
   steps back to the character's first byte. With a tiny `maxLen` and wide characters the readable part can shrink to nothing; the hash still carries uniqueness. The result is never longer than `maxLen`, and
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

A claim can never choose an external name (one exception, IAM-mode database
users on Cloud SQL and Azure: section 16.4): there is no `spec.name` override,
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
- A blocked or failed result is written to the log when it differs from the
  last one written for the claim, not on every poll; a success starts over.
  The in-memory tracker behind this and the spec fingerprints drops a claim on
  deletion and any claim not reconciled for 48 hours (twice the longest poll
  interval), so a claim whose finalizer was removed by hand does not stay in
  memory. Known limits: the audit key of a repeated result leaves out the error
  text, so different errors under one action and result are one line; the idle
  sweep is O(n) once an hour under the tracker's lock; and the server-type
  lookup of `server.Resolve` is cached for the life of the process per scheme.
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

### 14. Server status

Added after the library was implemented (NOXprobe, 2026-11-13): the server
kinds' `ServerStatus` (`observedGeneration`, `conditions`, `serverVersion`,
`lastProbeTime`) was promised by every CRD and written by nothing. `pkg/probe`
writes it, so an operator only supplies the backend call.

- The operator implements `probe.Prober`:
  `Probe(ctx, server, connection) (version, error)`, using its own cached
  backend client; the library reads the connection settings
  (`server.ConnectionFor`, the same checks as `Resolve` minus the claim
  namespace, and one shared implementation) and owns the schedule, the
  conditions and the status write. `probe.Reconciler` is the single entry
  point, the controller-runtime wiring for a cluster-scoped or a namespaced
  kind alike; the kind adds `GetServerStatus()` to what `server.Server`
  already needs. The only knobs are `Options` and `Reconciler.Controller`
  (`controller.Options`, e.g. `MaxConcurrentReconciles`); the clock and the
  jitter source are unexported.
- Schedule: a health check, as in Kubernetes, runs every
  `spec.healthCheckInterval` (default 1m, or `Options.DefaultInterval`;
  bounded 10s to 1h) plus up to 10% jitter whether the last probe succeeded or
  not. A failed probe is recorded and logged, not returned as an error, so
  controller-runtime's backoff (5ms doubling to about 1000s) never stretches
  the interval of a failing server. Reconcile returns an error only for a
  status write that fails and for a read of the connection settings that fails
  for a reason other than the server's configuration (an API server error,
  which leaves the status as it was). If the context is cancelled while
  probing (the manager stops), nothing is recorded.
- Triggers: a server's own spec change (generation) probes at once; the
  controller's own status writes do not (generation predicate, applied to the
  server watch only). The controller also watches the Secrets and ConfigMaps
  the servers name (the admin Secret, the CA bundle) and probes the servers
  that use a changed one at once, so creating a missing Secret recovers
  without waiting for the interval. The mapping lists the cached servers on
  each event and matches by reference name (and namespace where it is known:
  a cluster server's Secrets are in the operator namespace, so it matches by
  name), which may probe a server needlessly and never misses one. The
  operator needs get, list and watch on Secrets and ConfigMaps and patch on
  `servers/status`.
- Conditions: `ServerReachable` and `Ready` (not `Synced`, which is a claim's).
  Success: both `True`, reason `Available`. Probe error: both `False`, reason
  `ServerUnreachable`, last known `serverVersion` kept. Admin Secret in a
  forbidden namespace: both `False`, reason `AdminSecretNamespaceForbidden`.
  Connection settings missing or invalid (a missing Secret or ConfigMap, an
  invalid proxy or CA reference, operator namespace unset):
  `ServerReachable=Unknown`, `Ready=False`, reason `InvalidServer`; the
  observed generation is stamped (it says the spec was evaluated) while
  `lastProbeTime` and `serverVersion` stay as they were. The last two cases are
  requeued at the interval without an error. Messages are fixed and coarse; the
  raw error is only logged, and never reaches the status, which every reader
  of the server can see. A failure is logged at Error when its condition
  changes and at V(1) while it persists.
- Status churn: the status is patched (merge patch, so a concurrent writer
  cannot make it conflict) only when a condition, `serverVersion` or
  `observedGeneration` changes. `lastProbeTime` is rewritten on those
  changes and otherwise every 10 minutes (`Options.RefreshInterval`), not on
  every probe, so it means "the last probe whose result was recorded" and
  stays at most that old while the operator runs; a healthy fleet writes no
  status per server per minute. Every condition carries the generation it was
  evaluated for, and `observedGeneration` is set with it.

### 15. Egress policy

Added after the library was implemented (NOX-egress, 2026-11-14): the four
operators each carried a copy of `internal/egress`, which limits where a
tenant-controlled server address may send the operator's backend connections
(user decisions 1 and 2, 2026-11-03: the proxy may not carry credentials, and
every connection is checked after name resolution). The copies were identical
but for comments, and for operator-db lacking `HostDialer` (it passes
addresses, not names). `pkg/egress` is the single copy; the operators switch
to it after the next library release.

- Public surface, the part the operators call today: `EnvMode`, `Mode` with
  `ModeStrict` and `ModeAllowPrivate`, `ParseMode`, `Policy`, `NewPolicy`,
  `Policy.Check`, `Policy.Dialer`, `Policy.HostDialer`, `DialFunc`, and the
  errors `ErrBlocked`, `ErrProxyUserinfo`, `ErrInvalidMode` and `ErrProxy`. The
  one addition is `Option` and `WithConnectTimeout`, variadic on `Dialer` and
  `HostDialer` so a call site that passes no option compiles as before.
  `ModeAllowPrivate` is the only exported name no operator references yet; it
  is the second value of the mode a startup flag selects.
- Modes (`EGRESS_MODE`, NO2b and NO3b; default `strict`): `allow-private`
  blocks every address in the IANA IPv4 and IPv6 Special-Purpose Address
  Registries (RFC 6890) that is never a backend, and `strict` adds the private
  rows (RFC 1918, RFC 6598 shared address space, RFC 4193 unique-local). The
  blocked set covers loopback, unspecified, link-local (which holds the cloud
  metadata address), multicast, the reserved top of IPv4, IETF protocol
  assignments (`192.0.0.0/24` and `2001::/23`, which hold Oracle's metadata
  address, AS112, AMT and the anycast addresses), documentation (RFC 5737,
  `2001:db8::/32`, `3fff::/20`), benchmarking, discard-only `100::/64`, the
  dummy prefix `100:0:0:1::/64`, ORCHID and ORCHIDv2, SRv6 SIDs, deprecated
  site-local, IPv4-compatible and IPv4-translated (SIIT) space, and the
  metadata addresses of Alibaba Cloud, AWS (IPv6), GCP (IPv6) and Azure. A row that is globally reachable
  (AS112, AMT) is blocked too: none is a backend. The tables and the registry
  rows they come from are in `pkg/egress/egress.go` and its test.
- Embedded IPv4: an IPv6 address that carries an IPv4 one gets the IPv4
  rules, so a private or loopback IPv4 address cannot be reached through IPv6.
  The carried address is decoded for IPv4-mapped (`::ffff:0:0/96`), NAT64
  (`64:ff9b::/96`, RFC 6052, and `64:ff9b:1::/48`, RFC 8215, whose address is
  split around octet 8), 6to4 (`2002::/16`) and Teredo (`2001::/32`, the
  client address, inverted). Teredo and 6to4 are not blocked outright: they
  are judged by the address they carry, as before. A NAT64 gateway with a
  network-specific prefix cannot be recognised; the operator relies on the
  well-known and local-use prefixes. A zone identifier is dropped before the
  check.
- Where it runs: after resolution, at connect time (`net.Dialer.Control`), so
  a DNS answer that changes between a check and the connection is not trusted.
  With a proxy `Dialer` accepts only an IP address and `HostDialer` resolves a
  name once, checks every address and asks the proxy for an address, never the
  name. The proxy's own address is checked too. A proxied destination must also
  have a canonical port (1 to 65535, checked before any name is resolved) and
  no zone identifier. A decimal, octal or hexadecimal
  IPv4 notation is not a canonical IP address, so it fails closed as a name
  with a proxy and is judged by the resolver's answer without one.
- Defects fixed in the move (both deferred from NO5b-1): the HTTP CONNECT
  exchange had no deadline when the context had none, so a proxy that never
  answered held the reconcile forever. The tunnel set-up (TLS handshake,
  CONNECT, SOCKS5 negotiation) is now bounded by `WithConnectTimeout`, 30
  seconds by default and never unbounded, and ends at the earlier context
  deadline or on cancellation. And some transition ranges were missing: the
  registry rows above, which also now cover the zero `netip.Addr` (blocked) and
  IPv4 documentation ranges. A server's first bytes that arrive with the proxy's
  response are no longer dropped.
- Consequence for the operators: addresses in the documentation ranges
  (`192.0.2.0/24`, `198.51.100.0/24`, `203.0.113.0/24`, `2001:db8::/32`) are
  blocked, so a test that stands in a documentation address for a public
  host needs another one (or the zero `Policy`).

### 16. Cloud providers

Status: decided at G-api (user, 2026-11-16: all eleven leans of 16.8
accepted). **Accepted** for what NK0 implemented in this library (16.9); the
rest is **Proposed** until the operators' NK items merge. The Accepted status
of sections 1 to 15 is unaffected. The plan's NK* items follow from it.

Context (plan decision log, 2026-11-16): every operator gains native AWS, GCP
and Azure backends next to its self-hosted one, behind the same claim kinds,
lifecycle (sections 3 to 8), naming (section 10) and egress policy (section
15). The user decided: native API calls (no ACK, Config Connector or ASO); all
five operators; emulator-only CI; `credentials.source` `WorkloadIdentity`
(default) or `Secret`; db and cache tenant login `Password` and `IAM`, both
from the start; the AWS mqtt certificate and key go in the binding Secret;
mqtt × GCP, oidc × GCP dropped; cache × Azure deferred (NKR1). This section
drafts the field names and shapes for those decisions. The field tables are in
[`docs/crd-drafts.md`](../crd-drafts.md), "Cloud providers (proposed)".

API conventions applied ([Kubernetes API conventions], "Constants" and
"Unions"): enum values are CamelCase with an initial capital (`AWS`, `GCP`,
`Azure`, `WorkloadIdentity`, `Secret`, `Password`, `IAM`; acronyms stay upper
case, as in `ClientIP`); the members of a union are all optional, one
discriminator selects the member and no default is set on the union fields
themselves; Go fields are PascalCase and JSON fields camelCase. The
discriminator-plus-matching-block shape is the one `IdentityProvider` already
uses (`type` with `keycloak` or `dynamicRegistration`, in the
[drafts](../crd-drafts.md)), so this adds no new pattern.

#### 16.1 Shape in one view

On the five server kinds (cluster and namespaced alike) the cloud variant is
two optional blocks next to the self-hosted fields:

```yaml
spec:
  provider:                    # absent: self-hosted, exactly as today
    type: AWS                  # AWS | GCP | Azure (an operator lists only the clouds it supports); immutable
    aws: {region: eu-west-1, roleARN: arn:aws:iam::111122223333:role/pg-admin,
          dbInstanceIdentifier: orders-prod}
  credentials:                 # absent with a provider: WorkloadIdentity
    source: WorkloadIdentity   # WorkloadIdentity | Secret
    # secretRef: {name: aws-creds, namespace: craftainer-system}   # Secret only
```

Rules (CEL on the server spec unless noted):

- `provider` is the discriminated union: `type` selects exactly one of `aws`,
  `gcp`, `azure` (each rule is `self.type == 'AWS' ? has(self.aws) :
  !has(self.aws)`, and likewise for the others). `type` is immutable
  (`self == oldSelf`): a server cannot move between clouds, because claims
  already point at objects in the old one. The target fields under the block
  (instance, user pool, ...) stay mutable so a restored instance can be
  re-pointed; claims keep working by `status.externalName`.
- With `provider` set, the self-hosted connection fields that name a host are
  no longer required. `host` (db, cache, mqtt) and `endpoint` (s3) are required
  only when `provider` is absent. With a provider they are optional overrides
  of the discovered data-plane address (needed when an emulator's API and its
  data plane are different processes), and every connection still goes through
  `pkg/egress` (section 15). `issuer` (oidc) is derived from the pool or
  tenant. The self-hosted-only fields `s3.region`, `s3.pathStyle` and
  `s3.backend` lose their schema defaults and get them in the controller, so
  CEL can reject them when `provider` is set (a defaulted field is always
  present to CEL).
- `credentials` is allowed only with `provider`.
  `!has(self.credentials) || (self.credentials.source == 'Secret' ?
  has(self.credentials.secretRef) : !has(self.credentials.secretRef))`: `Secret`
  requires `credentials.secretRef`, `WorkloadIdentity` forbids it, and an
  absent `credentials` passes (it means `WorkloadIdentity`). `secretRef` is `{name, namespace?}` with the
  same namespace rules as `adminCredentialsSecretRef`: a namespaced server
  forbids `namespace` (CEL), and a cluster server requires it (CEL) and the
  controller refuses any namespace but the operator's (CEL cannot know it),
  with the new reason `CredentialsNamespaceForbidden`, as
  `AdminSecretNamespaceForbidden` does for the admin Secret. It has no `key`: the key
  names are fixed per cloud (16.2).
- `adminCredentialsSecretRef` keeps its meaning, the login to the data plane.
  It is therefore still required for `ClusterDatabaseServer` and
  `DatabaseServer` (the operator runs SQL as an administrator) unless
  `adminAuthMode: IAM` (decided, 16.8 question 4: `Password` by default;
  `IAM` only on Azure in `v1alpha1`, where the operator logs in as the Entra administrator
  named by `provider.azure.entraAdmin`, with a token for scope
  `https://ossrdbms-aad.database.windows.net/.default` from the same identity
  as `credentials` (so `entraAdmin` is that identity's Entra principal name);
  cluster kinds only, since a namespaced server may not use workload
  identity; the Secret is then forbidden), and forbidden
  with a provider on the s3, mqtt, cache and oidc server kinds, whose cloud
  API credential is `credentials`. Self-hosted servers are unchanged.
- Server `status` gains `discovered` (map of string, maxProperties 10, values
  maxLength 512), the same shape as `lateInitialized`, for what the operator
  read from the cloud: `endpoint`, `port`, `resourceId` (RDS `DbiResourceId`
  or `DbClusterResourceId`), `accountId`, `region`. The probe (section 14)
  fills it, and claims read it from the cached server.
- New condition reasons, on server kinds unless stated:
  `CredentialsNamespaceForbidden`, `CredentialsUnavailable` (no workload identity
  injected, Secret missing or malformed), `TargetNotFound` (the named instance,
  user pool or namespace does not exist) and `EndpointOverrideForbidden` are
  server reasons; `AuthModeUnsupported` and `IAMPrincipalRequired` are claim
  reasons (`Synced=False`). Section 11 uses the same strings as Event reasons. Messages stay coarse
  (section 14).

#### 16.2 `credentials.source`

`WorkloadIdentity` (default) lets the cloud SDK's default chain find the
operator pod's identity. The controller code calls no cloud API for it:

| Cloud | Chain the SDK walks | What the user sets up |
| --- | --- | --- |
| AWS | `aws-sdk-go-v2` default config: `AWS_WEB_IDENTITY_TOKEN_FILE` + `AWS_ROLE_ARN` (IRSA), or `AWS_CONTAINER_CREDENTIALS_FULL_URI` + token file (EKS Pod Identity) | IRSA: annotate the operator's ServiceAccount `eks.amazonaws.com/role-arn: arn:aws:iam::<account>:role/<name>`. Pod Identity: no annotation; a Pod Identity association for the ServiceAccount (`aws eks create-pod-identity-association`). Either way the role's trust policy is the user's |
| GCP | Application Default Credentials: the GKE metadata server (Workload Identity Federation for GKE) or an `external_account` file (the platform's own ADC; a tenant's Secret may not use it, see the `Secret` table) | Preferred: grant IAM roles directly to the federated principal `principal://iam.googleapis.com/projects/<number>/locations/global/workloadIdentityPools/<project>.svc.id.goog/subject/ns/<operator-ns>/sa/<sa>`, no annotation. Alternative: annotate the ServiceAccount `iam.gke.io/gcp-service-account: <gsa>@<project>.iam.gserviceaccount.com` and bind `roles/iam.workloadIdentityUser` |
| Azure | `azidentity` `DefaultAzureCredential` / `WorkloadIdentityCredential`: `AZURE_CLIENT_ID`, `AZURE_TENANT_ID`, `AZURE_FEDERATED_TOKEN_FILE` | annotate the ServiceAccount `azure.workload.identity/client-id: <client id>` (optional `azure.workload.identity/tenant-id`), put the pod label `azure.workload.identity/use: "true"` on the operator's pod template (the webhook only mutates labelled pods), and create the federated identity credential for the issuer and subject `system:serviceaccount:<ns>:<sa>` |

The operator never creates, edits or deletes its own identity: not the IAM
role, trust policy, federated credential, role assignment or ServiceAccount
annotation. The chart exposes `serviceAccount.annotations` and `podLabels`
values; the user supplies the annotations and the grants. What the operator
does create is tenant-scoped cloud objects named by claims (IAM users and
access keys, service accounts, IoT certificates, ACL users, app clients),
within the permissions the user granted. Each operator's ADR 0005 lists the
exact actions; the resource types are:

| Operator | AWS | GCP | Azure |
| --- | --- | --- | --- |
| db | read RDS instances and clusters (`rds:Describe*`) | Cloud SQL users and instances (`cloudsql.users.*`, `cloudsql.instances.get`) | read the PG flexible server; Entra admin on it for IAM mode |
| s3 | buckets, IAM users, access keys and user policies | buckets, service accounts, HMAC keys | containers, role assignments (or SAS) on one storage account |
| cache | ElastiCache or MemoryDB users, one user group or ACL, read replication groups or clusters | Memorystore ACL policies and read instances | deferred |
| mqtt | IoT certificates, policies, policy attachments, `DescribeEndpoint` | dropped | Event Grid namespace clients (NKmqtt-2 verifies) |
| oidc | Cognito user pool app clients | dropped | Graph `Application.ReadWrite.OwnedBy` (app registrations the operator owns) |

`Secret` holds a static credential. The key names are fixed per cloud, so the
reference has no `key`:

| Cloud | Keys | Notes |
| --- | --- | --- |
| AWS | `accessKeyId`, `secretAccessKey`, optional `sessionToken` | used with `roleARN` as the AssumeRole source when that is set |
| GCP | `credentials.json` | the filename Google's tooling uses for ADC. Only `"type": "service_account"` is accepted; an `external_account` or impersonation file is rejected (`CredentialsUnavailable`), because such a file can make the client fetch a URL or run an executable that a tenant chose (Google's guidance on externally sourced credential configs) |
| Azure | `clientId`, `clientSecret` | the tenant comes from `provider.azure.tenantID`; certificates are not offered in `v1alpha1` |

Defaults and tiers: `source` defaults to `WorkloadIdentity` when `credentials`
is present, and an absent `credentials` is read as `WorkloadIdentity` by the
controller (a schema default on the object would also apply without a
provider). **A namespaced server must use `Secret`** (CEL: `!has(self.provider)
|| (has(self.credentials) && self.credentials.source == 'Secret')`). A
namespaced server is tenant-created and may name any target, so with the
operator's own identity it would be a confused deputy: a tenant could aim the
operator's cloud permissions at any instance, bucket or pool those
permissions reach. Cluster servers are created by the platform admin and may
use `WorkloadIdentity`. The Secret of a cluster server lives in the operator
namespace, so no tenant reads it; a namespaced server's Secret is the
tenant's own.

#### 16.3 Provider configuration per cloud

The common fields are the same in every operator; the target fields below
them are the operator's own.

| Cloud | Common fields | Notes |
| --- | --- | --- |
| `aws` | `region` (required; pattern `^[a-z]{2}(-[a-z]+)+-[0-9]+$`, maxLength 32), `roleARN` (optional; AssumeRole from the source credentials; `arn:` prefix by CEL, maxLength 2048), `endpointOverride` | the AWS partition follows from the region and from `roleARN` |
| `gcp` | `project` (required; project ID pattern, maxLength 30), `location` (region; required where the resource is regional), `endpointOverride` | |
| `azure` | `tenantID` (required; GUID), `subscriptionID` and `resourceGroup` (required for the ARM-based operators db, s3, mqtt; absent for oidc, which talks to Graph), `endpointOverride`; db only: `entraAdmin` (see 16.1, `adminAuthMode`) | |

What identifies the target, per operator (an `allowedNamespaces` selector and
the naming rules apply as before):

| Operator | AWS | GCP | Azure |
| --- | --- | --- | --- |
| db | `dbInstanceIdentifier` or `dbClusterIdentifier` (exactly one; an Aurora cluster uses its writer endpoint) | `instance` (with `project` and `location`; the instance connection name `project:location:instance` is derived and written to the binding) | `serverName` (a PG flexible server in `resourceGroup`) |
| s3 | the account and `region` are the target: buckets are created in `region`; optional `iamPath` (default `/craftainer/`) and `permissionsBoundaryARN` for the IAM users the operator creates | `project`, `location` (bucket location) | `storageAccount` (3 to 24 lowercase letters or digits) |
| cache | `elasticache: {userGroupID, replicationGroupID}` or `memorydb: {aclName, clusterName}` (exactly one block). The user group or ACL must already exist and be attached to the cluster by the admin; the operator only adds and removes users in it | `memorystore: {instance}` (with `project` and `location`) | deferred |
| mqtt | none beyond `region`: the operator reads the account's IoT data endpoint (`DescribeEndpoint`, `iot:Data-ATS`) | dropped | `eventGridNamespace` (name, in `resourceGroup`) |
| oidc | `cognito: {userPoolID, domain}`; `domain` (the hosted-UI domain prefix or custom domain) is needed to build the token and authorization endpoints | dropped | `entra: {}` only: the target is the tenant (`tenantID`) |

For oidc the rules of 16.1 and 16.2 apply with `type` in place of
`provider`: `credentials` only with `type` `Cognito` or `Entra`, a namespaced
server must use `Secret` for those types, `adminCredentialsSecretRef` is
forbidden for them, and `issuer` is required only for `Keycloak` and
`DynamicRegistration`. For oidc the discriminator is the existing `type` (extended with `Cognito`
and `Entra`, with blocks `cognito` and `entra`), not a second `provider`
(decided, 16.8 question 1). `issuer` is derived (`https://cognito-idp.<region>.amazonaws.com/<userPoolID>`,
and the tenant's v2.0 issuer).

**Endpoint override (test-only).** `endpointOverride` points the SDK at an
emulator (moto, MiniStack, fake-gcs-server, Azurite). Proposal: it exists on
cluster server kinds only (CEL on the namespaced kinds forbids it); a value is
an `https` URL, or `http` when the operator runs with `EGRESS_MODE=allow-private`;
and the operator honours it only in `allow-private` mode. In `strict` mode a
server that sets it is `Ready=False`, reason `EndpointOverrideForbidden`. The
mode is a startup setting that CEL cannot see, so the controller enforces it.
Trade-off (decided: gate on the egress mode): this adds no new knob and
means "private addresses are acceptable here" has one switch, but production
use of private cloud endpoints (AWS PrivateLink or VPC endpoints resolve to
RFC 1918 addresses) also needs `allow-private` and is not a test, so the mode
cannot tell a test from a PrivateLink deployment. A separate switch (an
operator env var such as `CLOUD_ENDPOINT_OVERRIDE=allow`) was the rejected
alternative. Emulators: section 15 blocks loopback in both modes, so CI
reaches an emulator at a private address (a service container or pod IP),
never loopback or `127.0.0.1`.

#### 16.4 `authMode` on `DatabaseRole` and `CacheUser`

```yaml
spec:
  authMode: IAM          # Password (default) | IAM; immutable
  iam:
    principal: app-sa@my-project.iam    # GCP and Azure only
```

- `authMode` is `Password` (default: today's path, a generated credential in
  the binding Secret, two-phase rotation of section 8) or `IAM` (the tenant's
  cloud identity logs in with a short-lived token and the operator creates
  no password). It is immutable (CEL `self == oldSelf`): switching modes
  changes the external object, the binding keys and the rotation state, so it
  is a new claim. `IAM` needs a cloud server; the claim cannot see the
  server's provider (CEL cannot cross objects), so the controller refuses it
  on a self-hosted server with `AuthModeUnsupported`.
- `iam.principal` (string, maxLength 320) is the consumer's cloud identity
  name, required for GCP and Azure and refused for AWS (`IAMPrincipalRequired`;
  the controller checks it against the server's cloud). CEL on the claim:
  `!has(self.iam) || self.authMode == 'IAM'`, and `rotationPolicy` is
  forbidden in IAM mode: `self.authMode == 'Password' ||
  !has(self.rotationPolicy)`. In IAM mode there is nothing to rotate; the
  `craftainer.dev/rotate` annotation produces a Warning Event with the new
  reason `RotationNotApplicable` and `status.rotation` stays empty.
  (`RotationUnsupported` keeps its section 8 meaning: a backend that cannot
  overlap credentials.)
- Per backend in IAM mode:

| Backend | What the operator creates | Consumer's login |
| --- | --- | --- |
| RDS / Aurora Postgres | one login role named by `naming.Tenant` (no `_a`/`_b`), `GRANT rds_iam`, and the same privileges as `Password` mode | `rds-db:connect` on `arn:aws:rds-db:<region>:<account>:dbuser:<resourceId>/<role>`, which the user adds to the workload's role; a token from `BuildAuthToken` (15 minutes), as the password |
| Cloud SQL Postgres | an IAM database user via `sqladmin users.insert` (type `CLOUD_IAM_USER` or `CLOUD_IAM_SERVICE_ACCOUNT`) named by `iam.principal`, plus the SQL grants | the Cloud SQL connector with IAM authentication (`cloudsqlconn` `WithIAMAuthN`), which refreshes the token itself |
| Azure PG flexible | an Entra principal via `pgaadauth_create_principal` named by `iam.principal`, plus the grants | an Entra token for scope `https://ossrdbms-aad.database.windows.net/.default` as the password |
| ElastiCache / MemoryDB | a user with authentication mode `iam`, whose user id equals its user name, in the configured user group or ACL | a SigV4-presigned token as the password (`elasticache:Connect` or `memorydb:connect` on the cluster and the user) |
| Memorystore for Valkey | an ACL policy bound to `iam.principal` | an OAuth access token (1 to 12 hours) sent with `AUTH` (login shape verified in NKcache-2) |

- Deviation from section 10 ("a claim can never choose an external name"): on
  Cloud SQL and Azure the database user's name must be the principal's name, so
  in those two modes `iam.principal` is chosen by the claim. This is
  the one exception to section 10's sentence: "except the database user of an
  IAM-mode claim on Cloud SQL and Azure". Section 10's Decision text is not
  edited (it is Accepted); it carries a one-line pointer to this paragraph. The check runs in PostgreSQL on
  the role, not on the cloud API object: before `users.insert` or
  `pgaadauth_create_principal` the operator requires that no role of that name
  exists (otherwise `NameCollision`), and right after it sets the section 10
  marker (`COMMENT ON ROLE` with the claim's UID). A crash between the two
  leaves an unmarked role, which then reads as `NameCollision` until an
  administrator removes it: it fails closed. An existing unmarked role or one
  marked with another UID is never adopted, so a tenant cannot take over or
  drop another tenant's login. A tenant can
  still name any principal in the project and give it access to its own
  database; it gains nothing outside that database. An optional
  allow-list of principals on cluster servers was considered and not adopted
  (16.8 question 6). Adding it later as an optional field, empty by default, is
  additive; making it mandatory would be breaking.
- IAM-mode paths have no emulator that validates tokens (plan, NKR1). They get
  unit tests against recorded API shapes, and their NFR verification says
  "untested in CI" until a real-cloud lane exists.

#### 16.5 Binding Secret shapes

All cloud bindings keep the section 7 type (`postgresql`, `redis`, `s3`,
`mqtt`, `oidc`), `type` and `provider`, and add:

| Key | Values | When |
| --- | --- | --- |
| `cloud` | `aws`, `gcp`, `azure` | every cloud binding |
| `authMode` | `Password`, `IAM` | db and cache bindings |
| `iamResourceArn` | the ARN the consumer's role must be allowed to `connect` to | AWS, `IAM` mode |
| `instanceConnectionName` | `project:location:instance` | Cloud SQL, both modes |
| `region` | the AWS region | AWS db and cache |

`IAM` mode, db: `host`, `port`, `database`, `username`, `sslmode`,
`sslrootcert`, `uri` without a password, plus the keys above. Cache: `host`,
`port`, `username`, `uri` (`rediss://`, no password), `keyPrefix` and
`sslrootcert` (the server's `tls.caBundle`, as in section 7), plus the keys
above. There is **no `password`** and no `uri` that embeds one. For
GCP and Azure `username` is the principal's name (the consumer's own identity,
which is the "principal name"); for AWS `username` is the derived role or user
id and `iamResourceArn` is the "ARN" the consumer must be authorized for.
Nothing in the Secret is secret in this mode, but it is still a Secret so the
shape is the same.

mqtt × AWS IoT Core (user decision: certificate and key in the Secret):

| Key | Value |
| --- | --- |
| `host`, `port`, `protocol` | the account's data endpoint (`<id>-ats.iot.<region>.amazonaws.com`), `8883`, `mqtts` |
| `endpoint` | `mqtts://<host>:8883`, the one-key form of the three above |
| `clientId` | derived with `naming.Tenant` (`-`, `HashShort`, at most 128 bytes, the IoT limit); the attached IoT policy allows `iot:Connect` on this client id only, and the tenant topic root (`<ns>/`) for publish, subscribe and receive |
| `certificate.pem` | the device certificate from `CreateKeysAndCertificate` |
| `private-key.pem` | its private key, which AWS returns once; never logged, and never written to status or an Event |
| `topicRoot` | `<ns>/` as in the other mqtt bindings |

No `username` or `password` (the certificate is the credential) and no
`sslrootcert` (the data endpoint's certificate chains to the Amazon Trust
Services roots that system trust stores carry). The IoT policy is one per
claim, named by `naming.Tenant`, and carries the ownership tag. Rotation is
certificate replacement: `RotateBegin` creates a new certificate (active),
attaches the policy and rewrites the Secret (`rotation.active` is the new
certificate ID); `RotateFinish` after the grace period sets the old one
`INACTIVE`, detaches the policy and deletes it. A crash between
`CreateKeysAndCertificate` and the policy attachment leaves an unattached
certificate, which grants nothing; the operator's sweep removes certificates
not in `rotation.active` or `rotation.previous` (specified in NKmqtt-1). The
generation cannot be derived from `rotation.sequence` as section 8 prefers,
because AWS assigns the certificate ID.

#### 16.6 Status

`externalName` is the derived name, `externalId` the cloud's own identifier,
as in section 3:

| Kind × cloud | `externalName` | `externalId` |
| --- | --- | --- |
| DatabaseRole / Database (all clouds) | role or database name (as self-hosted; the principal for Cloud SQL and Azure IAM) | none |
| BucketAccess × AWS | IAM user name | IAM user ARN (`rotation.active` is the access key ID) |
| Bucket × AWS, GCP | bucket name | `arn:aws:s3:::<bucket>`, `projects/_/buckets/<bucket>` |
| CacheUser × ElastiCache, MemoryDB | user id (at most 40 characters, letters, digits and hyphens: `naming.Tenant` with `-` and `HashShort`, `maxLen` 40) | user ARN |
| CacheUser × Memorystore | ACL policy id | ACL policy resource name |
| MqttUser × AWS | IoT policy name | active certificate ARN (`rotation.active` is the certificate ID) |
| OidcClient × Cognito | app client name | app client ID (also `clientId` in the binding) |
| OidcClient × Entra | app registration name | application object ID (`clientId` in the binding is the app ID) |

Naming and the marker (section 10) for the new objects. An object that can
carry a tag or label holds the marker `craftainer.dev/claim` and uses
`HashShort`; one that cannot uses `HashLong`:

| Backend object | `sep` | `hl` | `maxLen` | Marker |
| --- | --- | --- | --- | --- |
| AWS IAM user (s3) | `-` | `HashShort` | 64 (IAM limit) | tag |
| IoT policy | `-` | `HashShort` | 128 | tag |
| IoT client id | `-` | `HashShort` | 128 | none needed (not an object) |
| ElastiCache and MemoryDB user id | `-` | `HashShort` | 40, letters, digits, hyphens; must start with a letter and has no consecutive hyphens | tag (both services tag users); a leading digit gets a `u` prefix inside `maxLen` and doubled hyphens are collapsed: the exact rule is verified in NKcache-1 |
| Memorystore ACL policy | `-` | `HashLong` until a label on it is confirmed (NKcache-2) | 63 | unconfirmed |
| Cognito app client name | `-` | `HashShort` | 128 | none; found by the client ID in `externalId` |
| Entra app registration display name | `-` | `HashLong` | 120 | none; found by object ID in `externalId` |

The cloud-native object names are checked against each service's limits in
that operator's ADR 0005; only the rows above change the section 10 rules.

`ElastiCache` and `MemoryDB` users are API-only and asynchronous: a user is
`creating`, `active`, `modifying` or `deleting`, and the SDKs have no waiter
for it (NKR1). The operator polls `DescribeUsers` (and the user group or ACL
state, which is also asynchronous) after every write:

| Cloud status | `Ready` | `Synced` | Next |
| --- | --- | --- | --- |
| `creating` | `False`, `Creating` | `True` | requeue in 10 seconds (plus jitter) |
| `modifying` (user, group, ACL) | `False`, `Updating` | `True` | same |
| `active` | `True`, `Available` | `True` | the drift poll |
| `deleting` | `False`, `Deleting` | `True` | requeue until NotFound, then drop the finalizer |
| missing without a prior create | `False`, `ReconcileError` | `False` | normal drift handling |

After 15 minutes in a transitional state the reason stays but the message
says so and the requeue backs off to the drift poll. No transitional state is
an error, so none triggers controller-runtime backoff. Memorystore's
long-running operations are polled the same way. The 10-second requeue is
shorter than the 30 second minimum of `craftainer.dev/poll-interval`
(section 6), which needs a library change: `External` must be able to report
"pending, check again in X" and `pkg/lifecycle` must requeue sooner without an
error. That is NK0 scope, a new exported name and so a minor release of this
library (decided, 16.8 question 8).

#### 16.7 Out of scope or dropped

- **mqtt × GCP: dropped** (user decision). Google Cloud IoT Core was retired
  on 2023-08-16 and GCP has no managed MQTT broker since; there is nothing to
  provision.
- **oidc × GCP: dropped** (user decision). GCP has no general API to create
  OAuth clients (the Identity-Aware Proxy and Identity Platform clients are
  different products with their own models).
- **cache × Azure: deferred** (NKR1). Azure Cache for Redis is retiring, and
  per-user access strings on Azure Managed Redis exist only in preview API
  `2026-05-01-preview` and SDK `armredisenterprise/v4` v4.1.0-beta.1. A
  released operator should not depend on a preview API (no SLA, may change).
  Revisit at GA; the `provider.type` enum for cache lists `AWS` and `GCP`
  until then.
- Not drafted here: `externalName`, `externalId` and binding keys for
  `BucketAccess` on GCP (service account) and Azure (SAS or Entra RBAC) and
  for `Bucket` on Azure. NKs3-2 and NKs3-3 specify them in the operator's ADR
  0005 before implementing, and send any change to this section back to
  G-api.
- Not offered in `v1alpha1`: certificate credentials for Azure, sovereign
  clouds beyond what the AWS region and ARN partition imply, and the operator
  creating its own cloud identity.

#### 16.8 Decisions (user, 2026-11-16)

These decisions bind from now: NK0 and the NK* items are written to them.
The section stays Proposed in the ADR until NK0 merges. Until then a change
is an edit of this section with a new decision-log entry; after that, a new
ADR that supersedes it, as `docs/adrs/README.md` says.

The user accepted the lean of all eleven questions. The tables above are the
decided shapes. Each line records the question and what was decided; the
rejected options are named so a later change can find them.

1. **Discriminator.** Decided: `spec.provider` on db, s3, cache and mqtt; the
   existing `type` extended with `Cognito` and `Entra` on oidc. Rejected:
   `provider` on all five (two discriminators in oidc), and extending each
   operator's own enum.
2. **Namespaced servers and `WorkloadIdentity`.** Decided: forbidden by CEL
   (a tenant-created server could aim the operator's own permissions at any
   target, 16.2). Rejected: allowing it behind a setting, or outright.
3. **`endpointOverride`.** Decided: cluster kinds only, honoured only with
   `EGRESS_MODE=allow-private`. Rejected: a separate operator switch, and any
   server ungated.
4. **Data-plane admin login through IAM.** Decided: `adminAuthMode`
   (`Password` default, `IAM`) on cloud db servers, `IAM` only for Azure in
   `v1alpha1`, with `provider.azure.entraAdmin` naming the Entra administrator
   the operator logs in as (the field name is settled in NKdb-3). Rejected:
   password admin everywhere with no Azure IAM mode, and `IAM` admin on every
   cloud.
5. **Binding key names.** Decided: `cloud`, `authMode`, `iamResourceArn`,
   `instanceConnectionName`; for mqtt × AWS `certificate.pem`, `private-key.pem`
   and `endpoint` (next to `host`, `port`, `protocol`), not the Service Binding
   reserved `certificates` and `private-key`.
6. **IAM principals as role names.** Decided: the marker plus `NameCollision`
   protection of 16.4, no `allowedPrincipals` allow-list for now; it can be
   added later as an optional, empty-by-default field when a user asks.
7. **Immutability.** Decided: `authMode` and `provider.type` are both
   immutable; a change means a new claim or server.
8. **Async polling in the library.** Decided: add the "pending, requeue in X"
   signal to `pkg/lifecycle` in NK0 (a minor release). Rejected: polling
   inside `Create` and `Update`, which holds a reconcile worker.
9. **Sovereign clouds.** Decided: out of `v1alpha1` (no `provider.azure.cloud`,
   no GCP universe domain); AWS GovCloud and China follow from the region.
10. **Credential Secret keys.** Decided: the cloud's own names (`accessKeyId`,
    `secretAccessKey`, `sessionToken`; `credentials.json`; `clientId`,
    `clientSecret`), different from RustFS's `accessKey` and `secretKey`; GCP
    accepts only `service_account` JSON, not `external_account`.
11. **Schema defaults.** Decided: s3 `region`, `pathStyle` and `backend` move
    from the schema to the controller now, while the API is `v1alpha1`, so CEL
    can reject them on a cloud server. Rejected: keeping the defaults and
    ignoring the fields, and separate cloud kinds.

Sources for 16.1 to 16.8 (the Kubernetes conventions are in the link list
below): [AWS IRSA], [EKS Pod Identity], [aws-sdk-go-v2 credentials], [GKE
Workload Identity Federation], [Azure Workload Identity annotations], [RDS IAM
authentication], [Cloud SQL IAM authentication], [Azure PostgreSQL Entra
authentication], [ElastiCache RBAC], [Memorystore ACL policies], [AWS IoT
`CreateKeysAndCertificate`], and the NKR1 findings in the plan's decision log
(2026-11-16).

#### 16.9 Implementation (NK0, lib-operator-go)

Accepted, implemented in the library (NK0):

- `pkg/cloud` types, with the markers and CEL of 16.1 to 16.3: `ProviderType`
  (immutable), `CredentialsSource`, `SecretRef`, `Credentials` and
  `NamespacedCredentials`, and the provider blocks `AWSBlock`, `GCPBlock` and
  `AzureBlock` plus their `...ClusterBlock` forms. Decision 2 and decision 3 are
  enforced by the type split and not by CEL alone: a namespaced kind embeds the
  plain block, which has no `endpointOverride` field, and
  `NamespacedCredentials`, whose CEL requires `source: Secret`. The resolvers
  check both again at run time (`ErrWorkloadIdentityForbidden`,
  `ErrEndpointOverrideForbidden`).
- Credential resolution (16.2), `Egress.AWS`, `Egress.GCP` and `Egress.Azure`:
  the SDK's default chain for `WorkloadIdentity`, the fixed Secret keys for
  `Secret`, and a GCP key accepted only as `service_account` (decision 10).
  Errors are categories (`ErrCredentialsUnavailable` and three others) and
  carry no SDK text or secret value.
- Egress (section 15): every HTTP client dials through
  `Policy.HostDialer`, the STS, Google OAuth and Entra token endpoints
  included; `endpointOverride` is honoured only for a cluster-scoped server in
  `allow-private` mode (decision 3), and loopback stays refused.
- Token sources for the IAM logins of 16.4: `RDSTokenSource` (15 minutes),
  `AzureTokenSource` with `AzurePostgresScope` and `AzureRedisScope`,
  `GCPTokenSource` (Memorystore AUTH), and `CachedSource`, which refreshes a
  token a margin before it expires.
- The "pending, requeue in X" signal of decision 8:
  `lifecycle.PendingError`, returned by any `External` method. It requeues
  after its own interval, shorter than the 30 second poll minimum, with no
  error and so no backoff, and sets `Synced=True` and `Ready=False` with the
  reason the `External` names (16.6).

Deviations from the text above, found while implementing:

- `WorkloadIdentity` on Azure is `WorkloadIdentityCredential` only, not
  `DefaultAzureCredential`. The chain's managed identity endpoint is a
  link-local address the egress policy blocks, and the chain also runs the
  Azure CLI. The same holds for EC2 instance metadata on AWS, which fails
  closed; IRSA and EKS Pod Identity work.
- The platform's own credential sources are not dialled through the policy:
  the EKS Pod Identity agent (`AWS_CONTAINER_CREDENTIALS_FULL_URI`) and the
  GCE and GKE metadata server are link-local addresses the policy blocks, the
  operator's environment chooses them and a tenant cannot, and the SDKs use their
  own clients for them. Everything that leaves the pod for a host a claim or a
  Secret can influence goes through the policy.
- `endpointOverride` on Azure replaces the Entra authority host (which MSAL
  requires to be https) and is handed back for the operator's own service
  client; on GCP and AWS it is the API endpoint.

Implemented afterwards (NKlib-1, additive: no exported type or signature of
the earlier releases changed):

- `common.CloudServerCommon`: `ServerCommon` with an optional
  `adminCredentialsSecretRef` (a pointer). It holds only the shared connection
  settings. `provider` and `credentials` stay in the operator's own spec,
  because the provider blocks carry the operator's target fields and a cluster
  kind and a namespaced kind use different credentials types
  (`cloud.Credentials`, `cloud.NamespacedCredentials`). The self-hosted
  `ServerCommon` is unchanged.
- `common.ServerStatus.Discovered` (`status.discovered`, `maxProperties` 10,
  values at most 512 characters by CEL), optional.
- `server.CloudServer`, `server.ResolveCloud`, `server.ConnectionForCloud` and
  `server.CloudConnection`: `Resolve` and `ConnectionFor` for a server that may
  have no admin Secret. The admin Secret is read only when the reference is
  set (`Admin` is empty otherwise), `allowedNamespaces` is enforced by the same
  code as `Resolve`, and the data of the credentials Secret is returned in
  `CloudConnection.Credentials`, ready for `cloud.Egress`. The credentials
  Secret must be in the operator namespace (cluster server) or the server's
  own (namespaced), else `ErrCredentialsSecretNamespaceForbidden`; a missing
  one is `ErrCredentialsUnavailable`.
- `probe.CloudProber`, `probe.CloudResult`, `probe.CloudServer` and the
  `Reconciler` fields `NewCloud` and `CloudProber`: the probe of a server with
  no admin Secret. It writes `status.discovered` with the version in the same
  merge patch, bounded to the schema (ten entries, 512 bytes a value), and
  keeps the last known values when a probe fails. Existing `Prober`s work
  unchanged and never touch `discovered`.
- The condition reasons `conditions.CredentialsNamespaceForbidden` and
  `conditions.CredentialsUnavailable`.
- `lifecycle.NonRotator`, `lifecycle.BindingRebuilder` and the Event
  `conditions.EventRotationNotApplicable` (NKlib-2): the `RotationNotApplicable`
  Event of 16.4's IAM mode, and the rebuild of an IAM binding Secret that holds
  no secret. Section 8 describes them; existing Externals work unchanged.

Not implemented here, left to the operators' NK items:

- The operators' own CRD types, the cross-field CEL of the server spec
  (the `provider` union, `credentials` only with `provider`), the remaining new
  condition and Event reasons (`TargetNotFound`, `EndpointOverrideForbidden`
  and the claim reasons), and the binding keys of 16.5. The operators'
  `CloudProber` reads `provider` and fills `discovered`.
- IAM tokens for ElastiCache and MemoryDB (a SigV4-presigned request), and the
  Cloud SQL connector, which refreshes its own token; operator-db and
  operator-cache add them.
- Emulator and recorded-shape tests of the data planes. The tests of this
  library use fake STS, OAuth and Entra endpoints; no emulator enforces IAM.

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
[AWS IRSA]: https://docs.aws.amazon.com/eks/latest/userguide/iam-roles-for-service-accounts.html
[EKS Pod Identity]: https://docs.aws.amazon.com/eks/latest/userguide/pod-identities.html
[aws-sdk-go-v2 credentials]: https://aws.github.io/aws-sdk-go-v2/docs/configuring-sdk/#specifying-credentials
[GKE Workload Identity Federation]: https://cloud.google.com/kubernetes-engine/docs/concepts/workload-identity
[Azure Workload Identity annotations]: https://azure.github.io/azure-workload-identity/docs/topics/service-account-labels-and-annotations.html
[RDS IAM authentication]: https://docs.aws.amazon.com/AmazonRDS/latest/UserGuide/UsingWithRDS.IAMDBAuth.html
[Cloud SQL IAM authentication]: https://cloud.google.com/sql/docs/postgres/iam-authentication
[Azure PostgreSQL Entra authentication]: https://learn.microsoft.com/en-us/azure/postgresql/security/security-entra-configure
[ElastiCache RBAC]: https://docs.aws.amazon.com/AmazonElastiCache/latest/dg/Clusters.RBAC.html
[Memorystore ACL policies]: https://docs.cloud.google.com/memorystore/docs/valkey/use-acl-policies
[AWS IoT `CreateKeysAndCertificate`]: https://docs.aws.amazon.com/iot/latest/apireference/API_CreateKeysAndCertificate.html
