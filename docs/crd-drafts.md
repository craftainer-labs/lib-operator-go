# CRD drafts for the five craftainer.dev groups

API for the five groups, written to the conventions of
[ADR 0002](adrs/0002-operator-crd-conventions.md) (Accepted, 2026-11-01, with
the G-api decisions folded in). All kinds are `v1alpha1`. Nothing here is
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
| `tls.caBundle.secretRef` | `{name, key}` | no | system roots | name and key maxLength 253; exactly one of `secretRef` and `configMapRef` |
| `tls.caBundle.configMapRef` | `{name, key}` | no | none | as above |
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
| `effectiveDeletionPolicy` | enum | `Delete` or `Retain`. With source `OperatorDefault` it is the default pinned at the claim's first reconcile, and deletion honours it, not the current env var. With source `Spec` it mirrors `spec.deletionPolicy`, which deletion reads directly |
| `deletionPolicySource` | enum | `Spec` or `OperatorDefault` |

The first reconcile pins the default and emits the Normal Event
`DeletionPolicyResolved`; later reconciles do not rewrite an `OperatorDefault`
value. Lost status is re-pinned from the current env var with the Warning
Event `DeletionPolicyRepinned` (ADR 0002 section 4).

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
| `region` | string | no | `us-east-1` | maxLength 64 |
| `pathStyle` | bool | no | `true` | |
| `backend` | enum | no | `RustFS` | `RustFS` (MinIO is unsupported; `AWS` later) |

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
