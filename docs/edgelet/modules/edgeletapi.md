# EdgeletAPI

EdgeletAPI is the **on-device HTTPS/WebSocket server** for operator and workload administration. Routes live under `/v1/...` on port **54321** and on Unix socket `/run/edgelet/edgelet.sock`. Handlers validate JWTs, enforce RBAC, and delegate domain work to `internal/runtimeapi`.

**Code:** `internal/edgeletapi/`

## Purpose

- Expose stable v1 REST and WebSocket surface for CLI and automation
- Enforce bootstrap vs provisioned JWT policy and deny-by-default RBAC
- Wrap responses in `{ success, data | error }` envelope
- Serve unauthenticated health and metrics probes
- Bridge microservice exec/log streaming over WebSocket

Operator guide: [../edgelet-api-v1.md](../edgelet-api-v1.md). OpenAPI: [../edgelet-api-v1-openapi.yaml](../edgelet-api-v1-openapi.yaml).

## Dependencies

| Depends on | Reason |
|------------|--------|
| `auth` | JWT validation, PKI paths |
| `runtimeapi` | Facade to processmanager, fieldagent, store, config |
| `serviceaccount` | Token list/revoke persistence |
| TLS material | `/etc/edgelet/edgeletapi-*.crt/key`, CA for clients |

| Used by | Reason |
|---------|--------|
| `supervisor` | Started after core modules; monitored every 10s |
| `cmd/edgelet` CLI | Default transport via Unix socket |
| Workload microservices | Self config/control routes with SA JWT |

## Lifecycle

### Start

`(*EdgeletAPI).Start()`:

1. `NewServer(54321)` — dual listeners (TCP TLS + Unix)
2. Start router in goroutine; wait on `Ready()` channel (max 15s)
3. Set startup state `Listening` for readiness handler

Unix socket path: `{varRun}/edgelet.sock` (typically `/run/edgelet/edgelet.sock`).

### Stop

Graceful `http.Server.Shutdown` on both listeners.

## Request pipeline

```mermaid
flowchart LR
    REQ["HTTP request"]
    RID["requestIDMiddleware"]
    LOG["accessLoggingMiddleware"]
    AUTH["authMiddlewareV1"]
    H["handler"]

    REQ --> RID --> LOG --> AUTH --> H
```

`authMiddlewareV1` (`middleware.go`):

1. Require `Authorization: Bearer <JWT>`
2. `auth.ValidateEdgeletAPIJWT(token)` — bootstrap unsigned OK when unprovisioned
3. Map route → RBAC permission (`rbac.go` + [../edgelet-api-v1-rbac-resources.md](../edgelet-api-v1-rbac-resources.md))
4. Deny unmapped routes and failed rule checks with `403 FORBIDDEN`

Health/metrics routes skip auth middleware.

## Router layout

Registered in `router.go` — all `/v1/...` routes use `chainMiddleware(..., authMiddlewareV1, accessLoggingMiddleware, requestIDMiddleware)`.

Exception: `GET /v1/microservices/control` uses WebSocket control handler directly (auth inside handler).

Handler packages:

| Package | Role |
|---------|------|
| `handlers/api.go` | Bulk of REST handlers |
| `handlers/api_volumes.go` | `/v1/volumes*` list, inspect, delete, prune |
| `handlers/auth.go` | `whoami` |
| `handlers/status.go`, `info.go`, `version.go` | System readouts |
| `handlers/api_envelope.go` | Success/error JSON helpers |
| `websocket/control.go` | Microservice control WS |

Domain logic stays in `runtimeapi` — handlers parse HTTP, call facade, map errors to stable codes.

## Configuration

| Path / key | Effect |
|------------|--------|
| `/etc/edgelet/edgelet-api` | Default CLI bearer JWT |
| `/etc/edgelet/edgeletapi-ca.crt` | Client TLS trust |
| PKI files under `/etc/edgelet/` | Server cert for `:54321` |
| Provision state in config | Bootstrap vs signed JWT mode |

Token lifecycle: `internal/auth/edgeletapi_token_lifecycle.go` (rotated by Field Agent worker).

## Data and persistence

EdgeletAPI handlers read/write via `runtimeapi` → `store`:

- Local deploy manifests → `local_workloads`, `local_registries`, `local_runtime_classes`
- Service account tokens → `local_service_account_tokens`
- Provision → config + `agent_credentials`

EdgeletAPI does not embed SQL.

## WebSocket routes

| Route | Handler |
|-------|---------|
| `/v1/system/logs:stream` | Daemon log follow |
| `/v1/ms/{id}/logs:stream` | Container log follow |
| `/v1/ms/{id}/exec/sessions/{sessionId}:attach` | Interactive exec |
| `/v1/microservices/control` | Workload control channel |

Upgrade requests require bearer JWT with appropriate RBAC (or microservice self binding).

## Observability

- Log module names: `"Edgelet API"`, `"Edgelet API Router"`, `"Edgelet API Server"`
- StatusReporter index: `3` (`utils.EdgeletAPI`)
- Access log middleware with request ID
- `GET /metrics` Prometheus exposition
- Startup state exposed to `/health/ready`

## Failure modes

| Symptom | Typical cause |
|---------|----------------|
| CLI exit 10 | Daemon down or socket missing |
| `401 UNAUTHORIZED` | Missing/invalid JWT; provisioned agent with bootstrap token |
| `403 FORBIDDEN` | RBAC deny or unmapped route |
| Readiness fails | Listener timeout (15s) or TLS misconfiguration |

See [../troubleshooting.md](../troubleshooting.md).

## Code map

| File | Role |
|------|------|
| `api.go` | Singleton start/stop |
| `server.go` | TLS + Unix listeners |
| `router.go` | Route registration |
| `middleware.go` | Auth, logging, request ID |
| `rbac.go` | Permission mapping and evaluation |
| `request_context.go` | Per-request auth metadata |

Related: [../edgelet-api-v1-rbac-resources.md](../edgelet-api-v1-rbac-resources.md), [store.md](store.md), [processmanager.md](processmanager.md).
