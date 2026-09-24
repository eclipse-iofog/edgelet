# Service Account

The service account manager **mints, rotates, and projects** workload JWTs for Controller-managed microservices. Tokens are persisted in SQLite and written to host paths bind-mounted into containers at `/var/run/secrets/edgelet.iofog.org/serviceaccount`.

**Code:** `internal/serviceaccount/`

## Purpose

- Create Ed25519-signed JWTs with microservice-scoped RBAC rules
- Project `token`, CA PEM, and public signing JWK atomically per microservice UUID
- Rotate tokens before expiry; revoke on microservice removal
- Reconcile projections when managed microservice set changes

## Dependencies

| Depends on | Reason |
|------------|--------|
| `auth` | `GenerateEdgeletAPITokenJWT` / JWTManager |
| `store` | `local_service_account_tokens` CRUD |
| `config` | Disk directory, provisioning state |

| Used by | Reason |
|---------|--------|
| `processmanager` | Reconcile after managed MS set changes |
| `fieldagent` | `serviceAccountTokenRotationWorker` |
| `runtimeapi` | Token list/revoke via EdgeletAPI |
| Container bind mounts | Engine maps projection dir into workload |

## Projection layout

Host staging root:

```
{diskDirectory}/volumes/serviceaccounts/{microserviceUUID}/edgelet.iofog.org~serviceaccount/default/
  token
  ca.crt
  edgelet.jwk
```

In-container mount: **`/var/run/secrets/edgelet.iofog.org/serviceaccount`** (`MountPath` constant).

| File | Contents |
|------|----------|
| `token` | Workload JWT (`tokenUse: serviceaccount`) |
| `ca.crt` | EdgeletAPI HTTPS CA PEM |
| `edgelet.jwk` | Public signing JWK (`kty=OKP`, `crv=Ed25519`, `alg=EdDSA`, `x`) — no private key |

Workloads on the same node can verify another microservice's projected token with `edgelet.jwk` (Ed25519) and then authorize custom `rulesByGroup` API groups from the token claims. Do not parse the verifier's own `token` to obtain the node public key.

Writes use atomic directory rename (`writeProjectionAtomic`) to avoid partial reads.

## Lifecycle

### ReconcileManagedMicroservices

Called when Process Manager finishes a managed-microservice reconcile cycle:

1. For each active managed MS UUID, mint or rotate token
2. Write projection directory
3. Remove staging dirs for UUIDs no longer in the active set

### RotateExpiringManagedTokens

Field Agent worker rotates tokens approaching expiry (default TTL **1 hour**; rotation lead window derived from JWT manager policy).

### Revocation

EdgeletAPI `POST /v1/auth/tokens/revoke` sets `revoked_at` in SQLite; middleware rejects revoked JTIs.

## Token claims

Minted tokens include:

- `tokenUse: serviceaccount`
- `iofog.org.microservice.uuid`, application/name metadata
- RBAC rules under `edgelet.iofog.org` (from Controller role binding projection)
- Standard `iat`, `nbf`, `exp`, `jti`

Self-scoped EdgeletAPI routes (`/v1/microservices/config`, `/v1/microservices/control`) require matching UUID claim.

## Configuration

No dedicated YAML section; behavior tied to managed microservice lifecycle and provision state.

| Constant | Value |
|----------|-------|
| Token TTL | 1 hour |
| Staging root | `{diskDirectory}/volumes/serviceaccounts/` |

## Data and persistence

| Table | Role |
|-------|------|
| `local_service_account_tokens` | JTI, SHA256, expiry, revocation, rules JSON |

See [store.md](store.md).

## External APIs

| Surface | Role |
|---------|------|
| EdgeletAPI `/v1/auth/tokens*` | List/revoke (admin) |
| Workload mount | `token`, `ca.crt`, and `edgelet.jwk` files read by microservice |
| EdgeletAPI self routes | Bearer from projected token |

## Observability

- Errors logged from reconcile/rotate paths in callers (Process Manager, Field Agent)
- No dedicated StatusReporter module index

## Failure modes

| Symptom | Typical cause |
|---------|----------------|
| MS can't call EdgeletAPI | Missing projection; token expired |
| `403` on self routes | UUID claim mismatch |
| Orphan projection dirs | MS removed from Controller; reconcile cleanup delayed |

## Code map

| File | Role |
|------|------|
| `manager.go` | Projection, reconcile, rotate, revoke helpers |

Related: [auth.md](auth.md), [processmanager.md](processmanager.md), [edgeletapi.md](edgeletapi.md), [../edgelet-api-v1-rbac-resources.md](../edgelet-api-v1-rbac-resources.md).
