# 0002. Follow one set of CRD and lifecycle conventions in every operator

## Status

Proposed (awaiting the G-api gate; the open choices are listed in "Decisions for the user (G-api)" at the end)

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
  `allowedNamespaces` (EB-3).
- Backends cannot enforce tenant naming against an admin credential, so the
  naming function and the server resolver are the only tenancy guard (NR4 5).
- Credentials are written as Service Binding spec Secrets, with no binding
  runtime dependency, because Red Hat's Service Binding Operator is archived
  and the spec is still a working draft. COSI is not used (pre-alpha
  `v1alpha2`); the S3 kinds only borrow its names.
- The semantics (`deletionPolicy`, `managementPolicies`, poll interval) are
  borrowed from Crossplane managed resources, without adopting its providers.
- NR4 found that overlapping credential rotation needs two phases on every
  backend that supports it, so I1's single `Rotate` cannot express it (NR4 3).
- Sources: [Kubernetes API conventions], [Service Binding spec], [COSI],
  [Crossplane managed resources], [controller-runtime docs], [K8s RBAC good
  practices], [Pod Security Standards], [Go FIPS 140-3].

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
  mutating webhook. Immutable fields use the CEL transition rule
  `self == oldSelf` ([CEL validation rules]). Every kind has printer columns
  `Ready`, `Synced`, `Server`, `Age`, and the category `craftainer`.
- The shared finalizer is `craftainer.dev/finalizer`. The shared annotations
  are `craftainer.dev/poll-interval`, `craftainer.dev/rotate` and
  (proposed) `craftainer.dev/paused`.

### 2. Server tiers and `allowedNamespaces`

- `Cluster<Kind>Server` (cluster scoped) holds the endpoint, the TLS settings
  (`tls.caBundle` from a Secret or ConfigMap key; TLS 1.2 minimum; there is no
  `insecureSkipVerify` field, EB-14), `adminCredentialsSecretRef` and
  `allowedNamespaces`. `<Kind>Server` (namespaced) has the same fields except
  `allowedNamespaces`, and its Secret reference names a Secret in its own
  namespace. Only the namespaced server is meant for a team that brings its
  own service.
- `adminCredentialsSecretRef` of a cluster server MUST name a Secret in the
  operator's own namespace; the controller refuses any other namespace with
  `Ready=False`, reason `AdminSecretNamespaceForbidden`. This stops a
  cluster server from being pointed at a tenant's Secret.
- `allowedNamespaces` is a `metav1.LabelSelector` matched against the labels
  of the claim's `Namespace` object. **It fails closed:** an absent
  `allowedNamespaces` allows no namespace; an empty selector (`{}`) allows
  every namespace and has to be written explicitly. Specific namespaces are
  selected with `matchExpressions` on the immutable
  `kubernetes.io/metadata.name` label, which the API server sets.
- A claim references its server with `serverRef: {kind, name}`. The kind
  enumerates the group's two server kinds. A namespaced server is always
  looked up in the claim's own namespace. `serverRef` is immutable.
- `server.Resolve(ctx, c, claimNS, ref)` is the only place that reads a
  server or its admin Secret. It returns `ErrNamespaceNotAllowed` for a
  namespace outside the selector, and the framework reports it as
  `Synced=False`, reason `NamespaceNotAllowed`, and requeues the claim at the
  drift-poll interval (no error backoff), because a `Namespace` label change
  triggers neither the claim nor the server. It reads the `Namespace` through
  the cache on every reconcile, so a label change takes effect on the next
  reconcile (a change to the server also re-queues its claims).

### 3. Claim spec and status shape

Every claim has these common spec fields (the drafts name them
`ClaimCommon`):

| Field | Type | Default | Notes |
| --- | --- | --- | --- |
| `serverRef` | `{kind, name}` | required | immutable |
| `deletionPolicy` | `Delete` or `Retain` | per group (below) | |
| `managementPolicies` | list of `*` or `Observe` | `["*"]` | see below |
| `binding.secretName` | DNS-1123 subdomain | claim name | credential-bearing kinds only; immutable |
| `rotationPolicy` | object | absent | credential-bearing kinds only |

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
  `NameCollision`, `ObserveOnly`, `RotationUnsupported`, `Rotating`.
- `externalName`: the derived external name (and `externalId` when the
  backend assigns one). Observe goes by these, never by re-deriving.
- `binding.name`: the binding Secret's name. Its presence makes the claim a
  Provisioned Service in the [Service Binding spec] sense.
- `rotation`: see section 8.
- `lastReconcileTime`.

Conditions follow the [Kubernetes API conventions] (`Ready` summarises;
no condition is removed once set; the controller sets `Unknown` instead of
dropping a condition it cannot evaluate).

### 4. `deletionPolicy`

- A finalizer holds the claim until the external object is handled.
  `Delete` removes the external object; `Retain` leaves it and removes the
  finalizer. Both then delete the binding Secret, which is owned by the claim.
- Defaults per group: `Retain` for `db` and `s3`; `Delete` for `mqtt`,
  `cache` and `oidc` (plan). The default is in the schema.
- Deleting a claim whose server is unreachable blocks on the finalizer with
  `Synced=False`, reason `ServerUnreachable`, and an Event; it never drops
  the finalizer silently. A user who must force it removes the finalizer by
  hand, which is a documented, deliberate act.
- A `Retain`ed external object is not adopted by the next claim of the same
  name automatically; adoption needs `managementPolicies: [Observe]`.

### 5. `managementPolicies`

A list with the values `*` (full control: create, update, delete) and
`Observe` (adopt an existing object and report on it; never create, update or
delete it). `["*"]` is the default; `["Observe"]` must stand alone (a CEL rule
rejects any other combination, and `*` must also stand alone).

- With `Observe`, the controller looks the object up by `status.externalName`
  or, on first reconcile, by the derived name. If it is missing the claim is
  `Synced=False`, reason `ObserveOnly` ("not found"); it is not created. The
  binding Secret is written only when the backend can issue the credential
  without modifying the object (for the credential-less kinds it is not
  written).
- Observe reads the object's ownership marker (section 10): an object owned
  by another claim is refused with `NameCollision`.
- Crossplane's finer policies (`Create`, `Update`, `Delete`, `LateInitialize`)
  are not adopted in `v1alpha1`; the list shape allows adding them later
  without a breaking change.

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

`naming.Tenant(ns, name string, maxLen int, sep string) string` derives every
external name from the claim's namespace and name. It is the only tenancy
guard besides `server.Resolve` (NR4 5): an admin credential can name anything,
so a wrong or colliding name is a cross-tenant incident. It has 100%
statement and branch coverage and a property test.

Inputs are Kubernetes-validated: `ns` is a DNS-1123 label, `name` a DNS-1123
subdomain; both are lower case and contain only `a-z`, `0-9`, `-` (and `.` in
`name`). They contain no `_`, `/`, `:` or `~`. The function does not
re-validate them.

Rules:

1. `h` is the first 10 characters of the lower-case, unpadded base32 (RFC
   4648, alphabet `a-z2-7`) of `SHA-256(len(ns) ":" ns ":" name)`. The length
   prefix makes the input unambiguous. SHA-256 is FIPS-approved (EB-14).
2. `readable = ns + sep + name`.
3. The result is `readable` when **all** hold: `sep` does not occur in `ns`;
   `len(readable) <= maxLen`; the part of `readable` after its last `sep` is
   not "hash-shaped" (exactly 10 characters, all in `a-z2-7`).
4. Otherwise the result is `cut(readable, maxLen - len(sep) - 10) + sep + h`,
   where `cut` truncates and then strips trailing `-`, `.`, `_`, `/` and `:`
   so no empty segment or double separator results. The result is never
   longer than `maxLen`.
5. `Tenant` panics (a programmer error found by the first test run, never by
   user input) when `maxLen < 16`, when `sep` is empty or longer than one
   byte, or when `ns` or `name` is empty.

Why these rules are collision-safe:

- When `sep` is not in `ns`, `readable` splits uniquely at its first `sep`, so
  two different `(ns, name)` pairs never give the same readable name. For
  `sep` in `{_, /, :}` that is every namespace, because those characters
  cannot occur in a namespace. For `sep = "-"` namespaces such as `team-a`
  take the hashed branch, so `("team-a", "db")` and `("team", "a-db")` cannot
  collide on `team-a-db`.
- The readable branch excludes names whose last segment looks like a hash, and
  every hashed result ends in `sep` and a hash-shaped segment, so the two
  branches produce disjoint sets. Without that exclusion a tenant could name
  a claim after another tenant's hash and land on its object.
- Within the hashed branch, two pairs collide only if their 50-bit hashes
  match and their visible prefixes match: a second-preimage search of about
  2^50 SHA-256 evaluations against a chosen victim. As defence in depth, every
  external object created by an operator carries an ownership marker
  (`craftainer.dev/claim=<ns>/<name>/<uid>`) where the backend can hold one
  (PostgreSQL `COMMENT ON ROLE/DATABASE`, S3 bucket tags, Keycloak client
  attributes, Mosquitto client `textdescription`; Valkey ACL users cannot, so
  they rely on the name alone). Create and Observe refuse an object whose
  marker names a different UID (`NameCollision`).

Callers subtract any suffix they append from `maxLen` (the rotation suffixes
`_a`, `_b`, `-a`, `-b` are two bytes). Per backend:

| Backend object | `sep` | `maxLen` passed | Limit and where it comes from |
| --- | --- | --- | --- |
| PostgreSQL database, owner role | `_` | 63 (login roles: 61, then `_a`/`_b`) | identifier limit of 63 bytes (`NAMEDATALEN` 64 minus the terminator, [PostgreSQL docs]); always quoted with `pgx.Identifier` since `-` is legal only quoted |
| S3 bucket (RustFS) | `-` | 63 | bucket names are 3 to 63 lowercase characters, digits, hyphens; the hashed branch always ends alphanumeric |
| S3 user and service account (RustFS) | `-` | 63 (not verified, see below) | service-account secret keys at most 40 characters (NR4); the access-key and user-name bounds are not in the NR4 findings |
| MQTT username and client id (Mosquitto) | `-` | 128, then `-a`/`-b` (126) | the MQTT string limit is 65535 bytes; 128 is our cap |
| MQTT topic root | `/` | not truncated | the root is `<ns>/`, so tenant ACL patterns are `<ns>/...` |
| Valkey ACL username | `:` | 128 | no hard limit; our cap. Key pattern `~<ns>:*`, channel pattern `&<ns>:*` |
| Keycloak client id | `-` | 255 | `CLIENT_ID` column is 255 characters (verify in NOxb) |
| DCR client name | `-` | 255 | free text; our cap |
| Kubernetes Secret and object names | n/a | 253 | DNS-1123 subdomain; not derived by `Tenant` |

The two limits marked "not verified" are added to that operator's NOxb open
checks. If a backend turns out shorter, only the `maxLen` constant changes:
the rules do not.

A claim can never choose an external name: there is no `spec.name` override,
so the derived name is the only path from a claim to a backend object.
`Bucket`, `DatabaseRole` and similar kinds add a CEL rule on `metadata.name`
that rejects characters outside the backend's set (for example, a `Bucket`
name containing `.`).

### 11. Events and audit (EB-4)

- Each state change emits a Kubernetes Event on the claim through the
  controller's `EventRecorder`: Normal `Created`, `Updated`, `Deleted`,
  `Observed`, `RotationStarted`, `RotationFinished`, `RotatedWithoutOverlap`;
  Warning `ServerUnreachable`, `NamespaceNotAllowed`, `NameCollision`,
  `ReconcileError`, `RotationFailed`. The Event `reason` equals the
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
  `latest` is the alternative, see G-api) and run with `GODEBUG=fips140=on`.
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
- `Tenant` costs readability in some names (a hash suffix for namespaces with
  a hyphen on `-` backends, and for long names) in exchange for collision
  safety. A name's hash is not secret and a database administrator can
  derive it.
- Fail-closed `allowedNamespaces` means a new server serves no one until its
  author lists namespaces, which is a deliberate extra step.
- The operator namespace holds DCR registration Secrets, so a compromise of
  the operator namespace compromises every registered client; this is the
  same trust as the admin credential and is documented.
- `v1alpha1` gives no compatibility promise, but the conversion-webhook path
  to `v1` adds a webhook and cert-manager dependency at graduation.
- Open items: the values marked "not verified" and the 17 live checks from
  NR4 belong to the operators' NOxb items, not to this ADR.

## Decisions for the user (G-api)

Each is a proposal with its trade-off and the author's lean.

1. **Two-phase rotation replaces `Rotate`** (section 8): `Rotator` with
   `RotateBegin`/`RotateFinish`, a persisted `RotationState`, and three
   new `Claim` methods. Trade-off: more interface and status for no outage at
   rotation, against the single call that cannot overlap. Lean: accept.
2. **`allowedNamespaces` shape**: a label selector that fails closed (absent
   allows none, `{}` allows all), as in the plan. Alternative: Gateway API's
   `from: Same | All | Selector`, more self-describing and safer by default,
   but a second shape to learn. Lean: label selector, fail closed.
3. **Namespaced `<Kind>Server` in `v1alpha1`**: ship both tiers in every group
   now (as drafted, matching the plan), or only the cluster tier first.
   Trade-off: the namespaced tier is a second kind per group to test and
   document, but adding a kind later is not a breaking change and removing one
   is. Lean: ship both as drafted, because the plan lists both and "bring your
   own service" is a stated use case; cutting the namespaced tier is the
   cheaper correction if it proves unused only before the first release.
4. **`managementPolicies` vocabulary**: only `*` and `Observe`, or Crossplane's
   `Create`, `Update`, `Delete`, `LateInitialize` as well. Lean: `*` and
   `Observe`; the list shape leaves room.
5. **`deletionPolicy` defaults**: the plan's group defaults (`Retain` for db
   and s3) also apply to credential-bearing kinds (`DatabaseRole`,
   `BucketAccess`), which leaves a live credential behind after the claim is
   gone. Alternative: `Retain` only for data kinds (`Database`, `Bucket`) and
   `Delete` for credential kinds in every group. Lean: the alternative.
6. **Naming rules** (section 10): readable names with a hash only when
   needed, against always appending the hash. Trade-off: readable names help
   operators and DBAs, always-hash is simpler to reason about and has no
   branch exclusion rule. Lean: the proposed readable-when-safe rules, with
   the ownership marker.
7. **Separator per backend**: `_` roles and databases, `-` buckets, clients,
   MQTT users, `/` topics, `:` Valkey. The plan fixed buckets and clients at
   `<ns>-<name>`. Lean: as proposed.
8. **Binding types**: `redis` for Valkey (the core spec defines no types, so
   `valkey` would be as valid) and craftainer-defined keys for all five
   types beyond the spec's reserved entries; a `uri` key is added. Alternative: `valkey` as the type.
   Lean: `redis` for client-library compatibility.
9. **DCR state location**: operator namespace (tenant never sees the RAT;
   removal by finalizer), against the claim's namespace (owner-reference
   garbage collection, but the tenant can read the RAT). Lean: operator
   namespace.
10. **Durations and sizes**: `metav1.Duration` strings, as most operators do,
    against integer fields with a unit suffix (`intervalSeconds`) as the API
    conventions prefer for new APIs. Lean: `metav1.Duration`.
11. **Shared API types**: duplicate the small `rotationPolicy` and status
    structs in each operator, or add a `pkg/apis/common` package to
    `lib-operator-go` with generated deep-copy so the five operators embed one
    definition. Adding the package extends I1. Lean: the shared package.
12. **`craftainer.dev/paused` annotation** (Crossplane's pause), to stop
    reconciling without deleting. Lean: include it.
13. **Graduation to `v1`**: the criteria in section 1 (one release of use,
    envtest and Chainsaw green, two-release deprecation of `v1alpha1`).
    Lean: accept.
14. **FIPS build**: `GOFIPS140=v1.0.0` (validated, older) against `latest`
    (newer, not yet validated); plus the `fips140=only` CI job. Lean:
    `v1.0.0` for releases.
15. **Audit log shape**: structured JSON log plus Events (section 11), with no
    separate append-only audit sink in the operators. Lean: accept; the
    cluster's API audit log and the SIEM export of the logs cover EB-4.
16. **Kind names and the five groups' drafts** in
    [`docs/crd-drafts.md`](../crd-drafts.md), including the COSI-aligned
    S3 names (`ClusterObjectStore`, `ObjectStore`, `Bucket`, `BucketAccess`).
    Lean: accept as drafted.

## Open issues

New design questions found in review; not decided here.

- Binding CA key: the spec reserves `certificates` (PEM chain) for TLS trust,
  while section 7 and the drafts use `sslrootcert`. Offer both, or only
  `certificates`?
- A cache or broker without TLS inside a namespace (a test instance) is not
  expressible, because EB-14 requires TLS to every backend. Is a documented
  exception wanted?
- `Observe` plus `rotationPolicy` on the same claim is not specified.
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
[CEL validation rules]: https://kubernetes.io/docs/tasks/extend-kubernetes/custom-resources/custom-resource-definitions/#validation-rules
[K8s RBAC good practices]: https://kubernetes.io/docs/concepts/security/rbac-good-practices/
[Pod Security Standards]: https://kubernetes.io/docs/concepts/security/pod-security-standards/
[Go FIPS 140-3]: https://go.dev/doc/security/fips140
[PostgreSQL docs]: https://www.postgresql.org/docs/current/sql-syntax-lexical.html#SQL-SYNTAX-IDENTIFIERS
