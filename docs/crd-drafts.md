# CRD drafts for the five craftainer.dev groups

API for the five groups, written to the conventions of
[ADR 0002](adrs/0002-operator-crd-conventions.md) (Accepted; section 16 is decided and partly implemented, see "Cloud providers (proposed)" at the end; the user approved
the G-api decisions on 2026-11-01 and they are folded in). All kinds are `v1alpha1`. Nothing here is
implemented; the field tables are what the API types and CRD schemas will be
generated from. The shared blocks are Go types in `lib-operator-go`'s
`pkg/apis/common`, which the operators embed.

Conventions used in the tables:

- "Req" is `yes` for a required field and `no` for an optional one.
- Validation is OpenAPI (`min`, `max`, `pattern`, `enum`) or CEL
  (`x-kubernetes-validations`). Every string has a `maxLength` and every list a
  `maxItems`, as CEL cost estimation requires. A CEL rule `self == oldSelf`
  makes a field immutable.
- Every kind has `metadata.name` as a DNS-1123 subdomain; where a stricter
  rule applies it is listed under the kind.

## Shared blocks

### `ServerCommon` (every server kind)

Cluster servers (`Cluster<Kind>Server`) and namespaced servers
(`<Kind>Server`) both include these spec fields.

| Field | Type | Req | Default | Validation |
| --- | --- | --- | --- | --- |
| `tls.caBundle.secretRef` | `{name, key?}` | no | system roots | name and key maxLength 253; `key` defaults to `ca.crt`; exactly one of `secretRef` and `configMapRef` |
| `tls.caBundle.configMapRef` | `{name, key?}` | no | none | as above |
| `tls.serverName` | string | no | host of the endpoint | maxLength 253 |
| `tls.minVersion` | enum | no | `"1.2"` | `"1.2"`, `"1.3"` |
| `adminCredentialsSecretRef` | `{name, key?, namespace?}` | yes | | `name` maxLength 253. Cluster server: `namespace` required, and the controller refuses any namespace but the operator's. Namespaced server: `namespace` forbidden (CEL), the Secret is in the server's namespace |
| `proxyURL` | string | no | process `HTTP(S)_PROXY` | scheme `http`, `https` or `socks5`; maxLength 512 |
| `healthCheckInterval` | duration | no | `1m` | min `10s`, max `1h` |

Cluster servers add:

| Field | Type | Req | Default | Validation |
| --- | --- | --- | --- | --- |
| `allowedNamespaces.from` | enum | yes | none | `All`, `Selector` (the Gateway API `FromNamespaces` values without `Same`) |
| `allowedNamespaces.selector` | `metav1.LabelSelector` | iff `from: Selector` | none | `matchLabels` maxProperties 20, `matchExpressions` maxItems 20; CEL: absent when `from: All`; when `from: Selector`, present and `size(matchLabels) + size(matchExpressions) > 0` (absent counts as 0), so an empty selector cannot match everything |

`allowedNamespaces` is itself required (CRD `required`), so a cluster server
without it is rejected at admission: it fails closed. Namespaced servers have
no `allowedNamespaces` and serve only claims in their own namespace. The shape
is that of the Gateway API's `allowedRoutes.namespaces`
([RouteNamespaces](https://gateway-api.sigs.k8s.io/reference/api-spec/1.6/spec/)).

There is no `insecureSkipVerify` field.

Status (`ServerStatus`):

| Field | Type | Notes |
| --- | --- | --- |
| `observedGeneration` | int64 | |
| `conditions[]` | `metav1.Condition` | `Ready`, `ServerReachable`; map by `type`, maxItems 8 |
| `serverVersion` | string | version the backend reported; maxLength 64 |
| `lastProbeTime` | time | |

### `ClaimCommon` (every claim kind)

| Field | Type | Req | Default | Validation |
| --- | --- | --- | --- | --- |
| `serverRef.kind` | enum | yes | | the group's two server kinds; immutable |
| `serverRef.name` | string | yes | | DNS-1123 subdomain, maxLength 253; immutable |
| `managementPolicies` | list of enum | no | `["*"]` | items `*`, `Observe`, `Create`, `Update`, `Delete`, `LateInitialize`; minItems 1, maxItems 5; unique; CEL: `*` only alone (`self.size() == 1` if it is present), and any list other than `["*"]` contains `Observe`. Semantics in ADR 0002 section 5 |

### `DataClaim` (data kinds: `Database`, `Bucket`)

Adds to `ClaimCommon`:

| Field | Type | Req | Default | Validation |
| --- | --- | --- | --- | --- |
| `deletionPolicy` | enum | no | none in the schema: the operator's `DEFAULT_DATA_DELETION_POLICY` (`Retain` when unset) | `Delete`, `Retain` |

Credential-bearing kinds have no `deletionPolicy`: they are always deleted
with their claim (when `managementPolicies` permits `Delete`; ADR 0002
section 4). The mqtt, cache and oidc groups have no data kind.

### `Credentials` (credential-bearing claim kinds)

| Field | Type | Req | Default | Validation |
| --- | --- | --- | --- | --- |
| `binding.secretName` | string | no | claim name | DNS-1123 subdomain, maxLength 253; immutable |
| `rotationPolicy.interval` | duration | no | none (rotate on trigger only) | min `1h`, max `8760h` |
| `rotationPolicy.gracePeriod` | duration | no | `1h` | min `5m`, max `168h` |

CEL on the claim spec of each credential kind (it has to see both fields, so
it cannot sit on `rotationPolicy` itself): `!has(self.rotationPolicy) ||
self.managementPolicies == ['*'] || 'Update' in self.managementPolicies`.

The annotation `craftainer.dev/rotate: "<any new value>"` triggers one
rotation (it also needs `Update`); `craftainer.dev/poll-interval:
"<duration>"` (30s to 24h) overrides the drift poll;
`craftainer.dev/paused: "true"` stops reconciling.

### `ClaimStatus` (every claim kind)

| Field | Type | Notes |
| --- | --- | --- |
| `observedGeneration` | int64 | |
| `conditions[]` | `metav1.Condition` | `Ready`, `Synced`, `ServerReachable`, all set on the first reconcile with `observedGeneration`; map by `type`, maxItems 8 |
| `externalName` | string | derived name from `naming.Tenant`; maxLength 255 |
| `externalId` | string | backend-assigned id, where there is one; maxLength 255 |
| `lastReconcileTime` | time | |
| `lateInitialized` | map of string | values recorded under `LateInitialize`; maxProperties 20, values maxLength 255 |

Data kinds add:

| Field | Type | Notes |
| --- | --- | --- |
| `effectiveDeletionPolicy` | enum | `Delete` or `Retain`: the default pinned at the claim's first reconcile. Deletion honours it, not the current env var, unless `spec.deletionPolicy` is set; the policy in force is the spec value when set, otherwise this field, and the spec value never changes this field |
| `deletionPolicySource` | enum | `OperatorDefault` (pinned from the env var) or `Fallback` (status was lost; pinned to `Retain`) |

The first reconcile pins the default and emits the Normal Event
`DeletionPolicyResolved`; later reconciles do not rewrite an `OperatorDefault`
value. Lost status (finalizer present, field empty) is pinned to `Retain` with
the Warning Event `DeletionPolicyRepinned` (ADR 0002 section 4).

Credential-bearing kinds add:

| Field | Type | Notes |
| --- | --- | --- |
| `binding.name` | string | the Secret's name; makes the claim a Provisioned Service |
| `rotation.active` | string | current generation, maxLength 128 |
| `rotation.previous` | string | generation awaiting retirement, maxLength 128 |
| `rotation.sequence` | int64 | completed rotations |
| `rotation.retireAfter` | time | end of the grace period |
| `rotation.lastRotated` | time | |
| `rotation.lastTrigger` | string | last handled `craftainer.dev/rotate` value, maxLength 253 |

### Printer columns (every kind)

`Ready` (`.status.conditions[?(@.type=="Ready")].status`), `Synced`, `Server`
(`.spec.serverRef.name`; none on server kinds), `Age`. Category `craftainer`.

---

## Operator configuration

Every operator reads the environment variable `DEFAULT_DATA_DELETION_POLICY`
(`Retain` or `Delete`; unset or empty means `Retain`; anything else fails
startup). It is the `deletionPolicy` that a new data claim whose spec leaves
the field unset gets pinned to at its first reconcile; changing it later
affects only claims created afterwards. Only `operator-db` and `operator-s3` have data kinds.

## Group `db.craftainer.dev` (operator-db, PostgreSQL)

Binding type `postgresql`.

### `ClusterDatabaseServer` (cluster scoped) and `DatabaseServer` (namespaced)

Includes `ServerCommon`. Admin Secret keys: `username`, `password`.

| Field | Type | Req | Default | Validation |
| --- | --- | --- | --- | --- |
| `host` | string | yes | | hostname or IP, maxLength 253 |
| `port` | int32 | no | `5432` | 1 to 65535 |
| `adminDatabase` | string | no | `postgres` | maxLength 63 |
| `sslMode` | enum | no | `verify-full` | `verify-full`, `verify-ca`, `require` (`disable`, `allow`, `prefer` are not offered) |
| `allowedExtensions` | list of string | no | `[]` | maxItems 50, each maxLength 63; `Database.spec.extensions` must be a subset |

`ClusterDatabaseServer` also takes `allowedNamespaces`. Status: `ServerStatus`.

```yaml
apiVersion: db.craftainer.dev/v1alpha1
kind: ClusterDatabaseServer
metadata:
  name: shared-pg
spec:
  host: pg.data.svc.cluster.local
  sslMode: verify-full
  tls:
    caBundle:
      secretRef: {name: pg-ca, key: ca.crt}
  adminCredentialsSecretRef: {name: pg-admin, namespace: craftainer-system}
  allowedNamespaces:
    from: Selector
    selector:
      matchLabels: {craftainer.dev/db: allowed}
  allowedExtensions: [pgcrypto, uuid-ossp]
---
apiVersion: db.craftainer.dev/v1alpha1
kind: DatabaseServer
metadata: {name: team-pg, namespace: team-a}
spec:
  host: pg.team-a.svc.cluster.local
  adminCredentialsSecretRef: {name: team-pg-admin}
```

### `Database` (namespaced claim, data kind)

Includes `ClaimCommon` and `DataClaim` (`deletionPolicy` defaults to the
operator's `DEFAULT_DATA_DELETION_POLICY`, `Retain` when unset). No binding
Secret: access comes from a `DatabaseRole`.

| Field | Type | Req | Default | Validation |
| --- | --- | --- | --- | --- |
| `encoding` | string | no | `UTF8` | enum `UTF8`; immutable |
| `locale` | string | no | server default | pattern `^[A-Za-z0-9_.@-]{1,64}$`; immutable |
| `extensions` | list of string | no | `[]` | maxItems 20, each maxLength 63; unique; subset of the server's `allowedExtensions` (controller) |
| `connectionLimit` | int32 | no | `-1` (unlimited) | min -1 |

`metadata.name` additionally matches `^[a-z0-9]([a-z0-9-]*[a-z0-9])?$` (CEL on
the root, no dots). Status: `ClaimStatus` (`externalName` is the database).

```yaml
apiVersion: db.craftainer.dev/v1alpha1
kind: Database
metadata: {name: orders, namespace: team-a}
spec:
  serverRef: {kind: ClusterDatabaseServer, name: shared-pg}
  extensions: [pgcrypto]
```

### `DatabaseRole` (namespaced claim, credential kind)

Includes `ClaimCommon`, `Credentials`. No `deletionPolicy`: the role is always
deleted with the claim (ADR 0002 section 4).

| Field | Type | Req | Default | Validation |
| --- | --- | --- | --- | --- |
| `databaseRef.name` | string | yes | | name of a `Database` in the same namespace, maxLength 253; immutable |
| `privileges` | enum | no | `ReadWrite` | `ReadOnly`, `ReadWrite`, `Owner` |
| `connectionLimit` | int32 | no | `-1` | min -1 |

Binding keys: `type`, `provider`, `host`, `port`, `database`, `username`,
`password`, `sslmode`, `sslrootcert`, `uri`. Status: `ClaimStatus` with
credentials (`externalName` is the base role; logins are `_a` and `_b`).

```yaml
apiVersion: db.craftainer.dev/v1alpha1
kind: DatabaseRole
metadata: {name: orders-app, namespace: team-a}
spec:
  serverRef: {kind: ClusterDatabaseServer, name: shared-pg}
  databaseRef: {name: orders}
  privileges: ReadWrite
  binding: {secretName: orders-db}
  rotationPolicy: {interval: 720h, gracePeriod: 2h}
```

---

## Group `s3.craftainer.dev` (operator-s3, RustFS)

Binding type `s3`. COSI-aligned names, no COSI dependency.

### `ClusterObjectStore` (cluster scoped) and `ObjectStore` (namespaced)

Includes `ServerCommon`. Admin Secret keys: `accessKey`, `secretKey`.

| Field | Type | Req | Default | Validation |
| --- | --- | --- | --- | --- |
| `endpoint` | string | yes | | `https` URL, maxLength 512 (CEL: `self.startsWith('https://')`) |
| `region` | string | no | controller default `us-east-1` (no schema default; ADR 0002 section 16.8) | maxLength 64; self-hosted only (CEL: forbidden with `provider`) |
| `pathStyle` | bool | no | controller default `true` | self-hosted only (forbidden with `provider`) |
| `backend` | enum | no | controller default `RustFS` | `RustFS` (MinIO is unsupported; the clouds are `provider`); forbidden with `provider` |

The admin API is `<endpoint>/rustfs/admin/v3` (SigV4). Cluster kind also takes
`allowedNamespaces`. Status: `ServerStatus`.

```yaml
apiVersion: s3.craftainer.dev/v1alpha1
kind: ClusterObjectStore
metadata: {name: rustfs}
spec:
  endpoint: https://rustfs.storage.svc.cluster.local:9000
  adminCredentialsSecretRef: {name: rustfs-admin, namespace: craftainer-system}
  allowedNamespaces:
    from: Selector
    selector:
      matchExpressions:
        - {key: kubernetes.io/metadata.name, operator: In, values: [team-a, team-b]}
---
apiVersion: s3.craftainer.dev/v1alpha1
kind: ObjectStore
metadata: {name: team-store, namespace: team-a}
spec:
  endpoint: https://rustfs.team-a.svc:9000
  adminCredentialsSecretRef: {name: team-store-admin}
```

### `Bucket` (namespaced claim, data kind)

Includes `ClaimCommon` and `DataClaim` (`deletionPolicy` defaults to the
operator's `DEFAULT_DATA_DELETION_POLICY`, `Retain` when unset). No binding
Secret.

| Field | Type | Req | Default | Validation |
| --- | --- | --- | --- | --- |
| `versioning` | bool | no | `false` | |
| `objectLock` | bool | no | `false` | immutable |
| `quota.hardBytes` | quantity | no | none | min 1 |
| `lifecycleRules[]` | list of `{id, prefix, expirationDays}` | no | `[]` | maxItems 10; `id` maxLength 255 unique; `prefix` maxLength 1024, no leading `/`; `expirationDays` min 1 |

`metadata.name` additionally matches `^[a-z0-9]([a-z0-9-]*[a-z0-9])?$`
(no dots). Status: `ClaimStatus` (`externalName` is the bucket).

```yaml
apiVersion: s3.craftainer.dev/v1alpha1
kind: Bucket
metadata: {name: uploads, namespace: team-a}
spec:
  serverRef: {kind: ClusterObjectStore, name: rustfs}
  versioning: true
  quota: {hardBytes: 50Gi}
```

### `BucketAccess` (namespaced claim, credential kind)

Includes `ClaimCommon`, `Credentials`. No `deletionPolicy`: the access key and
its service accounts are always deleted with the claim (ADR 0002 section 4).

| Field | Type | Req | Default | Validation |
| --- | --- | --- | --- | --- |
| `bucketRef.name` | string | yes | | `Bucket` in the same namespace; immutable |
| `access` | enum | no | `ReadWrite` | `ReadOnly`, `ReadWrite` |
| `prefix` | string | no | whole bucket | maxLength 1024, no leading `/`; restricts the policy to keys under it |

Binding keys: `type`, `provider`, `endpoint`, `region`, `bucket`,
`accessKeyId`, `secretAccessKey`, `prefix`, `pathStyle`, `sslrootcert`.
Status: `ClaimStatus` with credentials (`rotation.active` is the access key).

```yaml
apiVersion: s3.craftainer.dev/v1alpha1
kind: BucketAccess
metadata: {name: uploads-app, namespace: team-a}
spec:
  serverRef: {kind: ClusterObjectStore, name: rustfs}
  bucketRef: {name: uploads}
  access: ReadWrite
  prefix: app/
  rotationPolicy: {interval: 2160h}
```

---

## Group `mqtt.craftainer.dev` (operator-mqtt, Mosquitto Dynamic Security)

Binding type `mqtt`.

### `ClusterMqttBroker` (cluster scoped) and `MqttBroker` (namespaced)

Includes `ServerCommon`. Admin Secret keys: `username`, `password` (a
Dynamic Security administrator).

| Field | Type | Req | Default | Validation |
| --- | --- | --- | --- | --- |
| `host` | string | yes | | maxLength 253 |
| `port` | int32 | no | `8883` | 1 to 65535 |
| `protocol` | enum | no | `mqtts` | `mqtts`, `wss` (plain `mqtt` and `ws` are not offered: TLS is required) |
| `controlTopic` | string | no | `$CONTROL/dynamic-security/v1` | maxLength 256, starts with `$CONTROL/` |
| `topicRoot` | string | no | `{namespace}/` | template; only `{namespace}` allowed; ends with `/` |

Cluster kind also takes `allowedNamespaces`. Status: `ServerStatus`.

```yaml
apiVersion: mqtt.craftainer.dev/v1alpha1
kind: ClusterMqttBroker
metadata: {name: mosquitto}
spec:
  host: mosquitto.messaging.svc.cluster.local
  adminCredentialsSecretRef: {name: mosquitto-admin, namespace: craftainer-system}
  allowedNamespaces: {from: All}
---
apiVersion: mqtt.craftainer.dev/v1alpha1
kind: MqttBroker
metadata: {name: team-broker, namespace: team-a}
spec:
  host: mqtt.team-a.svc
  adminCredentialsSecretRef: {name: team-broker-admin}
```

### `MqttUser` (namespaced claim, credential kind)

Includes `ClaimCommon`, `Credentials`. No `deletionPolicy`: the user and both
clients are always deleted with the claim.

| Field | Type | Req | Default | Validation |
| --- | --- | --- | --- | --- |
| `acls[]` | list of `{topic, access}` | no | `[{topic: "#", access: PublishSubscribe}]` under the tenant root | maxItems 50 |
| `acls[].topic` | string | yes | | relative to the tenant root `<ns>/`; maxLength 256; CEL: no leading `/`, no `$` prefix, no `..`; wildcards `+` and `#` allowed, `#` only as the last level |
| `acls[].access` | enum | yes | | `Publish`, `Subscribe`, `PublishSubscribe` |
| `maxConnections` | int32 | no | `-1` | min -1 |

The operator prepends `<ns>/` to every pattern; a claim cannot reach another
namespace's topics. Binding keys: `type`, `provider`, `host`, `port`,
`protocol`, `username`, `password`, `clientId`, `topicRoot`, `sslrootcert`.
Status: `ClaimStatus` with credentials (clients are `<base>-a` and `-b`).

```yaml
apiVersion: mqtt.craftainer.dev/v1alpha1
kind: MqttUser
metadata: {name: sensor-gateway, namespace: team-a}
spec:
  serverRef: {kind: ClusterMqttBroker, name: mosquitto}
  acls:
    - {topic: "sensors/+/telemetry", access: Publish}
    - {topic: "commands/#", access: Subscribe}
```

---

## Group `cache.craftainer.dev` (operator-cache, Valkey / Redis ACL)

Binding type `redis`.

### `ClusterCacheServer` (cluster scoped) and `CacheServer` (namespaced)

Includes `ServerCommon`. Admin Secret keys: `username`, `password`.

| Field | Type | Req | Default | Validation |
| --- | --- | --- | --- | --- |
| `host` | string | yes | | maxLength 253 |
| `port` | int32 | no | `6379` | 1 to 65535 |
| `mode` | enum | no | `Standalone` | `Standalone` (ACLs are per node, so Cluster and Sentinel are out of `v1alpha1`) |

Cluster kind also takes `allowedNamespaces`. Status: `ServerStatus`.

```yaml
apiVersion: cache.craftainer.dev/v1alpha1
kind: ClusterCacheServer
metadata: {name: valkey}
spec:
  host: valkey.cache.svc.cluster.local
  adminCredentialsSecretRef: {name: valkey-admin, namespace: craftainer-system}
  allowedNamespaces:
    from: Selector
    selector:
      matchLabels: {craftainer.dev/cache: allowed}
---
apiVersion: cache.craftainer.dev/v1alpha1
kind: CacheServer
metadata: {name: team-cache, namespace: team-a}
spec:
  host: valkey.team-a.svc
  adminCredentialsSecretRef: {name: team-cache-admin}
```

### `CacheUser` (namespaced claim, credential kind)

Includes `ClaimCommon`, `Credentials`. No `deletionPolicy`: the ACL user is
always deleted with the claim.

| Field | Type | Req | Default | Validation |
| --- | --- | --- | --- | --- |
| `profile` | enum | no | `ReadWrite` | `ReadOnly`, `ReadWrite` (`@dangerous` and `@admin` commands are never granted) |
| `channels` | bool | no | `false` | grants pub/sub on `&<ns>:*` |

Key pattern is always `~<ns>:*`, derived, never set by the claim. Binding
keys: `type`, `provider`, `host`, `port`, `username`, `password`, `uri`,
`keyPrefix`, `sslrootcert`. Status: `ClaimStatus` with credentials
(`rotation.active` is the password's SHA-256).

```yaml
apiVersion: cache.craftainer.dev/v1alpha1
kind: CacheUser
metadata: {name: session-store, namespace: team-a}
spec:
  serverRef: {kind: ClusterCacheServer, name: valkey}
  profile: ReadWrite
  rotationPolicy: {interval: 720h}
```

---

## Group `oidc.craftainer.dev` (operator-oidc, Keycloak and RFC 7591/7592)

Binding type `oidc`.

### `ClusterIdentityProvider` (cluster scoped) and `IdentityProvider` (namespaced)

Includes `ServerCommon` (its `adminCredentialsSecretRef` Secret holds
`clientId` and `clientSecret` of a service account for Keycloak, or is
optional for a DCR provider that uses an initial access token).

| Field | Type | Req | Default | Validation |
| --- | --- | --- | --- | --- |
| `type` | enum | yes | | `Keycloak`, `DynamicRegistration`; immutable |
| `issuer` | string | yes | | `https` URL, maxLength 512 |
| `keycloak.baseURL` | string | with `Keycloak` | | `https` URL, maxLength 512 |
| `keycloak.realm` | string | with `Keycloak` | | maxLength 255 |
| `dynamicRegistration.registrationEndpoint` | string | with `DynamicRegistration` | | `https` URL, maxLength 512 |
| `dynamicRegistration.initialAccessTokenSecretRef` | `{name, key}` | no | none | |
| `allowedScopes` | list of string | no | `[openid, profile, email]` | maxItems 50, each maxLength 128 |

CEL: exactly the block matching `type` is set. Cluster kind also takes
`allowedNamespaces`. Status: `ServerStatus` plus `discovery.tokenEndpoint`,
`discovery.authorizationEndpoint`, `discovery.jwksUri` read from
`/.well-known/openid-configuration`.

```yaml
apiVersion: oidc.craftainer.dev/v1alpha1
kind: ClusterIdentityProvider
metadata: {name: keycloak}
spec:
  type: Keycloak
  issuer: https://sso.example.com/realms/craftainer
  keycloak: {baseURL: https://sso.example.com, realm: craftainer}
  adminCredentialsSecretRef: {name: kc-operator, namespace: craftainer-system}
  allowedNamespaces:
    from: Selector
    selector:
      matchLabels: {craftainer.dev/oidc: allowed}
---
apiVersion: oidc.craftainer.dev/v1alpha1
kind: IdentityProvider
metadata: {name: team-idp, namespace: team-a}
spec:
  type: DynamicRegistration
  issuer: https://idp.team-a.example.com
  dynamicRegistration:
    registrationEndpoint: https://idp.team-a.example.com/register
    initialAccessTokenSecretRef: {name: team-idp-iat, key: token}
  adminCredentialsSecretRef: {name: team-idp-iat}
```

### `OidcClient` (namespaced claim, credential kind)

Includes `ClaimCommon`, `Credentials` (`rotationPolicy` allowed only when
`public: false`, CEL). No `deletionPolicy`: the client is always deleted with
the claim.

| Field | Type | Req | Default | Validation |
| --- | --- | --- | --- | --- |
| `public` | bool | no | `false` | public clients get no secret |
| `redirectUris` | list of string | yes | | minItems 1, maxItems 20, each maxLength 2048; `https`, or `http` only when the parsed host is exactly `localhost` or `127.0.0.1` (a prefix or regex match would accept `http://localhost.evil.example`; check the parsed host, for example with CEL `url(x).getHostname()`); no wildcard; unique. `[::1]` is not allowed |
| `postLogoutRedirectUris` | list of string | no | `[]` | as above |
| `webOrigins` | list of string | no | `[]` | maxItems 20; no `*` |
| `grantTypes` | list of enum | no | `[authorization_code]` | `authorization_code`, `refresh_token`, `client_credentials`; CEL: `client_credentials` requires `public: false` |
| `tokenEndpointAuthMethod` | enum | no | `client_secret_basic` (`none` if public) | `client_secret_basic`, `client_secret_post`, `none`; CEL: `none` iff `public` |
| `pkce` | enum | no | `S256` | `S256`, `Disabled`; `Disabled` is rejected when `public` |
| `scopes` | list of string | no | `[openid]` | maxItems 50; subset of the server's `allowedScopes` (controller) |

Binding keys: `type`, `provider`, `issuer`, `clientId`, `clientSecret`
(confidential only), `tokenEndpoint`, `authorizationEndpoint`, `jwksUri`,
`userinfoEndpoint`. Status: `ClaimStatus` with credentials, plus
`registrationClientUri` (DCR; the registration access token is in a Secret in
the operator namespace, never in status) and `externalId` (the `client_id`).

```yaml
apiVersion: oidc.craftainer.dev/v1alpha1
kind: OidcClient
metadata: {name: web, namespace: team-a}
spec:
  serverRef: {kind: ClusterIdentityProvider, name: keycloak}
  redirectUris: [https://app.team-a.example.com/callback]
  scopes: [openid, profile]
  binding: {secretName: web-oidc}
  rotationPolicy: {interval: 2160h, gracePeriod: 24h}
```

---

## Cloud providers (proposed)

Decided at G-api (user, 2026-11-16, all eleven questions); **Accepted** for the
shared types, credential resolution, egress and token sources that NK0
implemented in `pkg/cloud` (ADR 0002 section 16.9) and **Proposed** for the
operators' own kinds. The field names and shapes below are
the decided ones, with the reasoning, the Kubernetes API conventions they
follow, the decisions and the sources in
[ADR 0002 section 16](adrs/0002-operator-crd-conventions.md). The self-hosted
kinds above are unchanged when `spec.provider` is absent.

### `CloudServerCommon` (every server kind of a cloud-capable group)

Added to `ServerCommon`; cluster and namespaced kinds alike. In the library
(`pkg/apis/common`), `CloudServerCommon` is `ServerCommon` with an optional
`adminCredentialsSecretRef`: a cloud-capable kind embeds it in place of
`ServerCommon`, and `server.ResolveCloud`, `server.ConnectionForCloud` and a
`probe.CloudProber` read it. `provider` and `credentials` below are fields of the
operator's own spec (the provider blocks carry its target fields; `pkg/cloud`
has the types, with `cloud.Credentials` for a cluster kind and
`cloud.NamespacedCredentials` for a namespaced one), and the kind implements
`GetCredentialsSecret()` to hand `credentials.secretRef` to the library.
`status.discovered` is `common.ServerStatus.Discovered`.

| Field | Type | Req | Default | Validation |
| --- | --- | --- | --- | --- |
| `provider.type` | enum | iff `provider` | | the clouds the operator supports: `AWS`, `GCP`, `Azure`; immutable (`self == oldSelf`) |
| `provider.aws` | object | iff `type: AWS` | | see below; CEL: set exactly when `type` is `AWS`, likewise `gcp` and `azure` |
| `provider.gcp` | object | iff `type: GCP` | | |
| `provider.azure` | object | iff `type: Azure` | | |
| `credentials.source` | enum | no | `WorkloadIdentity` when `credentials` is present; an absent `credentials` is read as `WorkloadIdentity` | `WorkloadIdentity`, `Secret` |
| `credentials.secretRef` | `{name, namespace?}` | iff `source: Secret` | | `name` maxLength 253; cluster server: `namespace` required (CEL) and equal to the operator's (controller, reason `CredentialsNamespaceForbidden`); namespaced server: `namespace` forbidden (CEL). Keys are fixed per cloud (below) |

CEL on the server spec:

- `credentials` only with `provider`: `!has(self.credentials) || has(self.provider)`.
- `!has(self.credentials) || (self.credentials.source == 'Secret' ? has(self.credentials.secretRef) : !has(self.credentials.secretRef))`.
- Namespaced kinds only, no workload identity: `!has(self.provider) || (has(self.credentials) && self.credentials.source == 'Secret')`.
- `host` (db, cache, mqtt) and `endpoint` (s3) are required iff `provider` is absent; with a provider they are optional overrides of the discovered data-plane address. s3's `region`, `pathStyle` and `backend` lose their schema defaults (the controller applies them) and are forbidden with a provider.
- `adminCredentialsSecretRef` (the data-plane login) stays required on the db server kinds (unless `adminAuthMode: IAM`, Azure only); on the s3, mqtt, cache and oidc server kinds it is forbidden with a provider (the cloud API credential is `credentials`).

Common fields of each `provider.<cloud>` block (an operator adds the target
fields listed under its group):

| Block | Field | Type | Req | Validation |
| --- | --- | --- | --- | --- |
| `aws` | `region` | string | yes | `^[a-z]{2}(-[a-z]+)+-[0-9]+$`, maxLength 32 |
| `aws` | `roleARN` | string | no | starts with `arn:`, maxLength 2048; AssumeRole from the source credentials |
| `gcp` | `project` | string | yes | project ID pattern, maxLength 30 |
| `gcp` | `location` | string | where regional | maxLength 64 |
| `azure` | `tenantID` | string | yes | GUID pattern |
| `azure` | `subscriptionID`, `resourceGroup` | string | db, s3, mqtt | GUID; `^[-\w._()]{1,90}$`; absent for oidc (Graph) |
| `azure` (db only) | `entraAdmin` | string | iff `adminAuthMode: IAM` | maxLength 128; the Entra principal name of the identity behind `credentials`, which logs in to the server as its Entra administrator. CEL: `adminAuthMode` is allowed only with `provider.type: Azure` and on cluster kinds; `IAM` requires `entraAdmin`; `entraAdmin` requires `IAM` |
| every block | `endpointOverride` | string | no | cluster kinds only (CEL on namespaced kinds forbids it); `https` URL, or `http` with `EGRESS_MODE=allow-private`; honoured only in `allow-private` mode, otherwise `Ready=False`, reason `EndpointOverrideForbidden`. For emulators, never for production. maxLength 512 |

Secret keys of `credentials.secretRef` (fixed, no `key`):

| Cloud | Keys |
| --- | --- |
| AWS | `accessKeyId`, `secretAccessKey`, optional `sessionToken` |
| GCP | `credentials.json` (`"type": "service_account"` only) |
| Azure | `clientId`, `clientSecret` |

Server status adds `discovered` (map of string, maxProperties 10, values
maxLength 512): `endpoint`, `port`, `resourceId`, `accountId`, `region`, as the
operator read them. New server condition reasons: `CredentialsNamespaceForbidden`,
`CredentialsUnavailable`, `TargetNotFound`, `EndpointOverrideForbidden`; new claim
reasons: `AuthModeUnsupported`, `IAMPrincipalRequired`.

What the operator's ServiceAccount needs (the user adds it; the operator never
creates an identity or a grant):

| Cloud | Annotation or label | Also |
| --- | --- | --- |
| AWS IRSA | ServiceAccount `eks.amazonaws.com/role-arn: arn:aws:iam::<account>:role/<name>` | the role's trust policy |
| AWS Pod Identity | none | a Pod Identity association for the ServiceAccount |
| GKE | none (federated principal bound to roles) or ServiceAccount `iam.gke.io/gcp-service-account: <gsa>@<project>.iam.gserviceaccount.com` | `roles/iam.workloadIdentityUser` on the Google service account in the second form |
| Azure | ServiceAccount `azure.workload.identity/client-id: <id>` (optional `.../tenant-id`) and pod template label `azure.workload.identity/use: "true"` | a federated identity credential for `system:serviceaccount:<ns>:<sa>` |

### `CloudAuth` (credential kinds `DatabaseRole` and `CacheUser`)

| Field | Type | Req | Default | Validation |
| --- | --- | --- | --- | --- |
| `authMode` | enum | no | `Password` | `Password`, `IAM`; immutable. `IAM` needs a cloud server (controller, `AuthModeUnsupported`) |
| `iam.principal` | string | GCP and Azure with `IAM` | | maxLength 320; the consumer's cloud identity. Refused on AWS (controller, `IAMPrincipalRequired`) |

CEL: `!has(self.iam) || self.authMode == 'IAM'`, and `self.authMode ==
'Password' || !has(self.rotationPolicy)`. In `IAM` mode there is no rotation:
`status.rotation` stays empty and `craftainer.dev/rotate` raises a Warning
Event with the new reason `RotationNotApplicable`. A claim cannot choose its external
name, except that on Cloud SQL and Azure the database user is named by
`iam.principal` (ownership marker and `NameCollision` still apply).

Binding keys added on a cloud server: `cloud` (`aws`, `gcp`, `azure`),
`authMode` (db and cache), `iamResourceArn` (AWS, `IAM`), `instanceConnectionName`
(Cloud SQL), `region` (AWS db and cache). In `IAM` mode there is no `password`
and no `uri` with one; a cache binding keeps `keyPrefix`, `sslrootcert` and the
`rediss://` `uri`, as in ADR 0002 section 7.

### db × cloud

`ClusterDatabaseServer` and `DatabaseServer` include `CloudServerCommon`
(`adminCredentialsSecretRef` stays required unless `adminAuthMode: IAM`).
Extra field `adminAuthMode` (enum `Password` default, `IAM`; `IAM` only with
`provider.type: Azure` in `v1alpha1`, CEL): `IAM` means the operator logs in
as the Entra administrator named by `provider.azure.entraAdmin` (see the
`azure` table above; field name settled in NKdb-3) with a token for scope
`https://ossrdbms-aad.database.windows.net/.default` from the identity behind
`credentials`, and `adminCredentialsSecretRef` is then forbidden. Cluster
kinds only. Target fields:

| Cloud | Field | Req | Notes |
| --- | --- | --- | --- |
| `aws` | `dbInstanceIdentifier` or `dbClusterIdentifier` | exactly one | RDS instance, or Aurora cluster (writer endpoint); maxLength 63 |
| `gcp` | `instance` | yes | with `project` and `location`; the connection name `project:location:instance` is derived; maxLength 98 |
| `azure` | `serverName` | yes | PG flexible server in `resourceGroup`; 3 to 63 |

```yaml
apiVersion: db.craftainer.dev/v1alpha1
kind: ClusterDatabaseServer
metadata: {name: rds-orders}
spec:
  provider:
    type: AWS
    aws: {region: eu-west-1, dbInstanceIdentifier: orders-prod}
  credentials: {source: WorkloadIdentity}
  adminCredentialsSecretRef: {name: rds-admin, namespace: craftainer-system}
  allowedNamespaces: {from: Selector, selector: {matchLabels: {craftainer.dev/db: allowed}}}
---
apiVersion: db.craftainer.dev/v1alpha1
kind: DatabaseRole
metadata: {name: orders-app, namespace: team-a}
spec:
  serverRef: {kind: ClusterDatabaseServer, name: rds-orders}
  databaseRef: {name: orders}
  authMode: IAM
  binding: {secretName: orders-db}
```

`DatabaseRole` includes `CloudAuth`. In `IAM` mode the role is one login (no
`_a`/`_b`) and `GRANT rds_iam` on RDS. Binding in `IAM` mode: `type`,
`provider`, `cloud`, `authMode`, `host`, `port`, `database`, `username`,
`sslmode`, `sslrootcert`, plus `iamResourceArn` (AWS: `arn:aws:rds-db:<region>:<account>:dbuser:<resourceId>/<role>`)
or `instanceConnectionName` (Cloud SQL).

### s3 × cloud

`ClusterObjectStore` and `ObjectStore` include `CloudServerCommon`; no
`adminCredentialsSecretRef`.

| Cloud | Field | Notes |
| --- | --- | --- |
| `aws` | `iamPath` (default `/craftainer/`), `permissionsBoundaryARN` | for the IAM users that `BucketAccess` creates; buckets are created in `region` |
| `gcp` | `project`, `location` | bucket location; `BucketAccess` uses a service account (the emulator has no HMAC keys) |
| `azure` | `storageAccount` | 3 to 24 lowercase letters and digits; access by SAS or Entra RBAC, decided in NKs3-3 |

`BucketAccess` on GCP and Azure and `Bucket` on Azure are not drafted yet
(NKs3-2, NKs3-3). `Bucket` status: `externalName` is the bucket, `externalId` its ARN or
resource name. `BucketAccess × AWS`: `externalName` is the IAM user,
`externalId` its ARN, `rotation.active` the access key ID; binding keys as
before plus `cloud`.

### cache × cloud

`ClusterCacheServer` and `CacheServer` include `CloudServerCommon`; `provider.type`
is `AWS` or `GCP` (Azure is deferred until the access-string API is GA).

| Cloud | Field | Notes |
| --- | --- | --- |
| `aws` | `elasticache: {userGroupID, replicationGroupID}` or `memorydb: {aclName, clusterName}` | exactly one block; the group or ACL exists and is attached by the admin; ids maxLength 255 |
| `gcp` | `memorystore: {instance}` | with `project` and `location` |

`CacheUser` includes `CloudAuth`; its `profile` and `channels` apply as before
(the key pattern stays `~<ns>:*`). `externalName` is the user id (at most 40
characters on ElastiCache), `externalId` the user ARN or ACL policy name.
Status mapping for the asynchronous ElastiCache and MemoryDB users (polled
after each write, requeue 10 seconds):

| Cloud status | `Ready` | `Synced` |
| --- | --- | --- |
| `creating` | `False`, `Creating` | `True` |
| `modifying` | `False`, `Updating` | `True` |
| `active` | `True`, `Available` | `True` |
| `deleting` | `False`, `Deleting` | `True` |

```yaml
apiVersion: cache.craftainer.dev/v1alpha1
kind: ClusterCacheServer
metadata: {name: elasticache}
spec:
  provider:
    type: AWS
    aws:
      region: eu-west-1
      elasticache: {userGroupID: craftainer, replicationGroupID: sessions}
  allowedNamespaces: {from: All}
---
apiVersion: cache.craftainer.dev/v1alpha1
kind: CacheUser
metadata: {name: session-store, namespace: team-a}
spec:
  serverRef: {kind: ClusterCacheServer, name: elasticache}
  authMode: IAM
```

### mqtt × cloud

`ClusterMqttBroker` and `MqttBroker` include `CloudServerCommon`;
`provider.type` is `AWS` or `Azure` (GCP is dropped: no managed MQTT service
since 2023). Target fields: AWS none beyond `region` (the data endpoint is
read with `DescribeEndpoint`); Azure `eventGridNamespace` (in `resourceGroup`;
the API is verified in NKmqtt-2).

`MqttUser` on AWS has no `authMode`: the certificate is the credential. Binding
keys: `type`, `provider`, `cloud`, `host`, `port` (`8883`), `protocol`
(`mqtts`), `endpoint` (`mqtts://<host>:8883`), `clientId`, `certificate.pem`,
`private-key.pem`, `topicRoot`. No `username`, `password` or `sslrootcert`.
`externalName` is the IoT policy name, `externalId` the active certificate ARN,
`rotation.active` the certificate ID. Rotation replaces the certificate
(`RotateBegin` creates and attaches, `RotateFinish` deactivates, detaches and
deletes the old one after the grace period); the private key is never logged.

```yaml
apiVersion: mqtt.craftainer.dev/v1alpha1
kind: ClusterMqttBroker
metadata: {name: iot-core}
spec:
  provider: {type: AWS, aws: {region: eu-west-1}}
  allowedNamespaces: {from: All}
---
apiVersion: mqtt.craftainer.dev/v1alpha1
kind: MqttUser
metadata: {name: sensor-gateway, namespace: team-a}
spec:
  serverRef: {kind: ClusterMqttBroker, name: iot-core}
  acls: [{topic: "sensors/+/telemetry", access: Publish}]
  rotationPolicy: {interval: 2160h, gracePeriod: 24h}
```

### oidc × cloud

The discriminator is the existing `type`, extended with `Cognito` and
`Entra`; there is no `provider` field in this group (decided, ADR 0002 section 16.8).
`ClusterIdentityProvider` and `IdentityProvider` add `credentials` and:

| Field | Type | Req | Validation |
| --- | --- | --- | --- |
| `cognito.region` | string | with `Cognito` | as `provider.aws.region` |
| `cognito.userPoolID` | string | with `Cognito` | `^[\w-]+_[0-9A-Za-z]+$`, maxLength 55 |
| `cognito.domain` | string | with `Cognito` | hosted-UI domain prefix or custom domain, maxLength 255 |
| `cognito.roleARN` | string | no | as `provider.aws.roleARN` |
| `entra.tenantID` | string | with `Entra` | GUID |

CEL, with `type` standing where `provider` does in `CloudServerCommon`:
exactly the block matching `type` is set; `credentials` only with `Cognito` or
`Entra`; a namespaced server with those types must use `Secret`;
`adminCredentialsSecretRef` is forbidden for them; `issuer` is derived for the
cloud types (so it is required only for `Keycloak` and `DynamicRegistration`);
`credentials.source` follows the common rules (Azure `Secret` keys `clientId`
and `clientSecret`). `OidcClient` is unchanged: `externalName` is the client
name and `externalId` the Cognito app client ID, or the Entra application
object ID (the binding's `clientId` is the app ID); the binding adds `cloud`.
oidc × GCP is dropped (no general OAuth-client API), so there is no `GCP` value.

```yaml
apiVersion: oidc.craftainer.dev/v1alpha1
kind: ClusterIdentityProvider
metadata: {name: cognito}
spec:
  type: Cognito
  cognito: {region: eu-west-1, userPoolID: eu-west-1_AbCdEf123, domain: team-auth}
  credentials: {source: WorkloadIdentity}
  allowedNamespaces: {from: All}
```
