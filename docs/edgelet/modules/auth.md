# Auth

The auth package centralizes **cryptography and JWT lifecycle** for Edgelet: agent signing keys, Controller JWTs, EdgeletAPI tokens, service account minting, Edge Guard signatures, TLS material for EdgeletAPI, and bootstrap vs provisioned validation policy.

**Code:** `internal/auth/`

## Purpose

- Generate and validate Ed25519-signed JWTs for Controller REST and EdgeletAPI
- Reconcile `/etc/edgelet/edgelet-api` token file with agent provisioning state
- Issue bootstrap unsigned JWTs when unprovisioned
- Provide PKI paths and TLS config for EdgeletAPI server
- Stable `hash` extraction for Edge Guard attestation comparison

## Dependencies

| Depends on | Reason |
|------------|--------|
| `store` | Hydrate agent private key from `agent_credentials` |
| `config` | Provisioning state (`iofogUuid`, in-memory private key) |
| Filesystem | Token file, cert/key paths under `/etc/edgelet/` |

| Used by | Reason |
|---------|--------|
| `edgeletapi` | `ValidateEdgeletAPIJWT`, middleware |
| `fieldagent` | Controller JWT, token rotation workers |
| `serviceaccount` | Mint projected workload tokens |
| `edgeguard` | Sign attestation JWTs |
| `cmd/edgelet` bootstrap | PKI generation on first start |

## JWT token uses

| `tokenUse` claim | Audience | Consumer |
|------------------|----------|----------|
| `controller` | Controller URL | Field Agent → `/api/v3/...` |
| `edgeletapi` | `edgelet://edgeletapi/v1` | CLI, EdgeletAPI middleware |
| `serviceaccount` | Edgelet bridge DNS | Workload pods (projected mount) |
| `edgeguard` | `edgelet://edgeguard/v1` | Attestation baseline in SQLite |

Issuer constant: `https://edgelet.default.svc.bridge.local`.

## EdgeletAPI validation policy

`ValidateEdgeletAPIJWT()` in `edgeletapi_jwt.go`:

| State | Accepted tokens |
|-------|-----------------|
| Unprovisioned | **Unsigned** (`alg: none`) bootstrap JWT only; `tokenUse` must be `edgeletapi` |
| Provisioned | **Signed** Ed25519 only; validated via `JWTManager.ValidateJWT()` + claim checks |

Required temporal claims on all tokens: `iat`, `nbf`, `exp`, `jti`.

## Token file lifecycle

`EnsureEdgeletAPITokenForCurrentState()` (`edgeletapi_token_lifecycle.go`):

- **Unprovisioned:** write short-TTL bootstrap JWT (`sub: system:edgeletadmin:bootstrap`) with wildcard RBAC rules
- **Provisioned:** signed admin JWT with `edgelet.iofog.org` rules `*:*`

Called on provision, deprovision, config reload, and from Field Agent rotation worker.

Token persistence: `edgeletapi_token_file.go` → `/etc/edgelet/edgelet-api` (mode `0600`).

## Agent private key

- Generated at provision; stored in SQLite `agent_credentials` (singleton row)
- Hydrated into config + `JWTManager` at Field Agent start and on reload
- Missing key blocks Controller auth paths and Edge Guard

## PKI and TLS

`edgeletapi_pki.go`, `certificates.go`, `tls_config.go`:

- EdgeletAPI server cert/key and CA for CLI trust
- Paths documented in [../architecture.md](../architecture.md) persistence table

## Configuration

Auth reads provisioning state from config + DB, not standalone YAML keys except indirectly via provision flow.

| Artifact | Path |
|----------|------|
| CLI bearer token | `/etc/edgelet/edgelet-api` |
| Client CA | `/etc/edgelet/edgeletapi-ca.crt` |
| Server TLS | `/etc/edgelet/edgeletapi-*.crt/key` |

## External APIs

No HTTP server in this package. Surfaces through:

- EdgeletAPI middleware (validation)
- Field Agent Controller client (outbound signed JWT)
- Service account projection files (`token`, `ca.crt`, `edgelet.jwk`)

## Observability

- Log module name: `"JWT Manager"`, `"Edgelet API JWT"`
- Failed validation returns generic `401` at middleware (no claim leakage)

## Failure modes

| Symptom | Typical cause |
|---------|----------------|
| CLI `401` after provision | Stale bootstrap token; run token reconcile or re-read `edgelet-api` file |
| Controller auth failures | Private key not hydrated from DB |
| Edge Guard won't start | Unprovisioned agent with frequency > 0 (forced to 0) |

## Code map

| File | Role |
|------|------|
| `jwt.go` | JWTManager, Controller/SA/EdgeGuard token generation |
| `edgeletapi_jwt.go` | EdgeletAPI validate + claim policy |
| `edgeletapi_token_lifecycle.go` | Bootstrap/signed reconcile |
| `edgeletapi_token_file.go` | Read/write token file |
| `edgeletapi_pki.go` | PKI bootstrap |
| `crypto.go` | Ed25519 helpers |
| `certificates.go`, `tls_config.go` | TLS for EdgeletAPI |

Related: [edgeletapi.md](edgeletapi.md), [serviceaccount.md](serviceaccount.md), [edgeguard.md](edgeguard.md), [../edgelet-api-v1.md](../edgelet-api-v1.md).
