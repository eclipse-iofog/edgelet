# EdgeletAPI v1

The **EdgeletAPI** is the on-device operator API exposed by the Edgelet daemon. The `edgelet` CLI is a thin transport client over this API and must not implement daemon runtime logic.

> **Not the Controller API.** Remote Pot/Controller REST remains at `/api/v3/...` on the controller URL. EdgeletAPI is localhost-only administration under `/v1/...`.

## Related documents

| Document | Role |
|----------|------|
| [edgelet-api-v1-openapi.yaml](edgelet-api-v1-openapi.yaml) | OpenAPI 3.1 baseline — request/response schemas and route inventory |
| [edgelet-api-v1-rbac-resources.md](edgelet-api-v1-rbac-resources.md) | Endpoint → RBAC resource/verb mapping (deny-by-default) |
| [../cli/README.md](../cli/README.md) | CLI command reference |
| [../cli/output-schemas.md](../cli/output-schemas.md) | JSON/YAML output shapes for `-o json` |
| [models.md](models.md) | Model artifact lifecycle |
| [knowledge.md](knowledge.md) | Knowledge artifact lifecycle |
| [CONTROLLER-HANDOFF-MODELS.md](CONTROLLER-HANDOFF-MODELS.md) | Controller JSON contract (status, RuntimeClass, catalog, prune) |
| [CONTROLLER-HANDOFF-KNOWLEDGE.md](CONTROLLER-HANDOFF-KNOWLEDGE.md) | Controller Knowledge JSON (`GET knowledge`, catalog, status, prune) |
| [CONTROLLER-HANDOFF-VOLUMES.md](CONTROLLER-HANDOFF-VOLUMES.md) | Controller `volumeMappings[].scope` (`private` \| `shared`) |
| [volumes.md](volumes.md) | Persistent VOLUME retain/reclaim |

---

## Base URL and transport

| Transport | URL | Notes |
|-----------|-----|-------|
| HTTPS (default) | `https://127.0.0.1:54321` | TLS required; trust `/etc/edgelet/edgeletapi-ca.crt` |
| Unix socket | `http+unix:///run/edgelet/edgelet.sock` | Same router as HTTPS; preferred for CLI on-node |
| WebSocket | `wss://127.0.0.1:54321` | Log stream, exec attach, microservice control channel |

TLS server name (SNI): `edgelet.default.svc.bridge.local`.

Dual transport is part of the v1 contract: both HTTPS and Unix socket listeners share the same route table and middleware.

### Unauthenticated probes

These routes skip JWT auth (for orchestrators and monitoring):

| Route | Purpose |
|-------|---------|
| `GET /health/live` | Process liveness |
| `GET /health/ready` | Readiness (daemon modules up) |
| `GET /metrics` | Prometheus metrics |

---

## Authentication

Send the bearer token from `/etc/edgelet/edgelet-api`:

```bash
TOKEN=$(sudo cat /etc/edgelet/edgelet-api)
curl -sk --cacert /etc/edgelet/edgeletapi-ca.crt \
  -H "Authorization: Bearer ${TOKEN}" \
  https://127.0.0.1:54321/v1/system/status
```

### JWT modes

| Agent state | Token policy |
|-------------|--------------|
| **Unprovisioned (bootstrap)** | Unsigned bootstrap JWT accepted on all EdgeletAPI routes |
| **Provisioned** | Unsigned JWT rejected globally; signed Ed25519 JWT required |
| **Deprovisioned** | Reverts to bootstrap mode |

CLI admin tokens use:

| Claim | Value |
|-------|--------|
| `tokenUse` | `edgeletapi` |
| `aud` | `edgelet://edgeletapi/v1` |

Service account and microservice workload tokens carry additional claims (for example `iofog.org.microservice.uuid` for self-scoped routes). Use `GET /v1/auth/whoami` to inspect the caller identity.

### API-group claims mapping

RBAC rules are evaluated from JWT claims:

- Rules under `edgelet.iofog.org/v1` and `edgelet.datasance.com/v1` are normalized to the `edgelet.iofog.org` group key.
- Other API groups are passed through under their own group keys.

---

## Authorization (RBAC)

Authorization is **deny-by-default**. Every `/v1/...` route must appear in [edgelet-api-v1-rbac-resources.md](edgelet-api-v1-rbac-resources.md); unmapped routes return `403 FORBIDDEN`.

### HTTP method → verb

| HTTP method | RBAC verb |
|-------------|-----------|
| `GET` | `get` |
| `POST` | `create` |
| `PATCH`, `PUT` | `update` |
| `DELETE` | `delete` |

The evaluator accepts `patch`/`put` as aliases for `update`.

### Scope examples

- Local admin token: broad rules such as `system:localadmin:*`
- Service account token: explicit resource + verb, e.g. `microservices` + `get` with optional resource name

Microservice self routes (`/v1/microservices/config`, `/v1/microservices/control`) bind identity from the JWT claim `iofog.org.microservice.uuid`; the server resolves the caller UUID and rejects mismatches.

---

## Response envelope

Successful responses:

```json
{
  "success": true,
  "data": { }
}
```

Errors:

```json
{
  "success": false,
  "error": {
    "code": "INVALID_ARGUMENT",
    "message": "human-readable detail",
    "details": { }
  }
}
```

The `details` object is optional and appears on validation failures and RuntimeClass guard errors.

### Async operations

Long-running applies (ControlPlane, RuntimeClass, some image pulls) may return **HTTP 202** with an `operationId`. Poll status endpoints documented in OpenAPI (for example `GET /v1/deploy/controlplane:apply/{operationId}`). Terminal failure on a known operation still returns HTTP 200 with `success=true` and `data.status=failed` plus nested `data.error`.

---

## Error taxonomy

Stable error codes for API and CLI consumers:

| Code | Meaning |
|------|---------|
| `INVALID_ARGUMENT` | Malformed payload, unsupported field/value, validation error |
| `UNAUTHORIZED` | Missing or invalid authentication token |
| `FORBIDDEN` | Authenticated but RBAC denied |
| `NOT_FOUND` | Requested resource does not exist |
| `CONFLICT` | State conflict prevents the operation (e.g. apply already in progress) |
| `NOT_IMPLEMENTED` | Endpoint or operation not implemented |
| `METHOD_NOT_ALLOWED` | HTTP method not supported for route |
| `EXEC_START_TIMEOUT` | Local exec session shell did not start within 15s (HTTP 504) |
| `INTERNAL` | Unexpected server-side failure |

### CLI exit mapping

| Error code | CLI exit code |
|------------|---------------|
| `INVALID_ARGUMENT` | 2 |
| `UNAUTHORIZED`, `FORBIDDEN` | 3 |
| `NOT_FOUND` | 4 |
| `CONFLICT` | 5 |
| `NOT_IMPLEMENTED` | 6 |
| All others | 1 |

Daemon unreachable (connection failure) uses exit code **10** (not an API error code).

### RuntimeClass-specific errors

When RuntimeClass endpoints are called outside supported mode (`full` build flavor + `containerEngine=edgelet`):

- HTTP `400`
- code: `INVALID_ARGUMENT`
- message: `runtimeclass is supported only when containerEngine=edgelet on full flavor builds`

**Reserved runtime delete** (e.g. `crun`):

- HTTP `400`, code `INVALID_ARGUMENT`
- message: `runtimeclass delete is not allowed for reserved runtime name: <name>`
- `details.runtimeClassName` set

**Runtime class in use**:

- HTTP `400`, code `INVALID_ARGUMENT`
- message includes blocking microservice UUID and runtime name
- `details`: `runtimeClassName`, `runtimeNames`, `blockingMicroserviceUuids`

**RuntimeClass operation polling** (`GET .../runtimeclasses:apply/{operationId}`, `GET .../runtimeclasses:delete/{operationId}`):

- Known operation: HTTP 200, `success=true`; terminal failure uses `data.status=failed` with nested error
- Unknown operation ID: HTTP 404, code `NOT_FOUND`

---

## Route groups

### `/v1/system/*`

Daemon administration: status, info, version, provision/deprovision, config get/patch/switch, reload, stop, prune, GPS, controller certificate upload, controller connection status, ControlPlane get/restart/delete, daemon logs (HTTP and `:stream` WebSocket).

Notable behaviors:

- `GET /v1/system/status` — daemon status object. Existing scalars stay strings. `runtimeClasses` is applied classes `{ name, handler, source }`; `availableCdiDevices` is fully-qualified CDI names (empty on docker/podman/desktop).
- `POST /v1/system/reload` — SIGHUP-style config reload; rejected changes do not mutate on-disk config
- `POST /v1/system/provision` / `DELETE /v1/system/provision` — agent lifecycle; affects JWT mode
- `GET /v1/system/controlplane` — local Datasance Controller deployment status (see [control-plane.md](control-plane.md))
- `POST /v1/system/controlplane/restart` — bounce the controller container; optional `?pull=true`; allowed when provisioned (see [control-plane.md](control-plane.md#restart))

### `/v1/ms/*`

Runtime view and lifecycle for workloads (managed, local, and control-plane sources):

- `GET /v1/ms` — list microservices; **`source` query only**: `managed`, `local`, `controlplane`, or `all` (default). Pagination filters (`cursor`, `limit`, `application`, `name`, `state`) are not implemented.
- `GET /v1/ms/{id}` — inspect (UUID or `namespace.name`). Includes catalog `models` and `knowledge` (`bindPath`, `permissions`, `items[].name`) when bound, `podId` when known (edgelet = pause/sandbox; docker/podman = `containerId`), `statusText` when the start gate is waiting for download or a bound model or Knowledge Failed, and crash fields when set: `errorMessage` (current; kept until 30s continuous RUNNING), `lastError` / `lastErrorAt` (last crash; not cleared on recovery; omitted when empty), `restartCount` (omitted when 0). Docker/Podman crash text looks like `exitCode=N oomKilled=…`; the embedded engine keeps `CRI reason=…`.
- Lifecycle: `start`, `stop`, `restart`, `kill`
- Logs: `GET .../logs` (HTTP); `GET .../logs:stream` (WebSocket follow)
- Exec: session create/get/delete; `GET .../exec/sessions/{sessionId}:attach` (interactive WebSocket). See [exec-sessions.md](exec-sessions.md) for multi-session behavior, the 15s start wait, and `EXEC_START_TIMEOUT`.

### `/v1/deploy/*`

Manifest-driven local persistence and apply:

| Kind | Apply | Validate | List/get/delete |
|------|-------|----------|-----------------|
| Microservice | `POST .../microservices:apply` | `...:validate` | `GET/DELETE .../microservices/{id}` |
| Registry | `POST .../registries:apply` | `...:validate` | `GET/DELETE .../registries/{id}` (ids 1–3 are built-in and cannot be edited or removed) |
| Model | `POST .../models:apply` | `...:validate` | runtime view via `/v1/models` |
| Knowledge | `POST .../knowledge:apply` | `...:validate` | runtime view via `/v1/knowledge` |
| RuntimeClass | `POST .../runtimeclasses:apply` | `...:validate` | `GET/DELETE .../runtimeclasses/{name}` |
| ControlPlane | `POST .../controlplane:apply` (async) | `...:validate` | status via `/v1/system/controlplane` |

Manifest YAML uses `apiVersion: edgelet.iofog.org/v1`. See [manifest-reference.md](manifest-reference.md).

#### Deploy apply semantics

Apply uses `multipart/form-data`:

| Field | Required | Description |
|-------|----------|-------------|
| `manifest` | yes | YAML manifest body |
| `dryRun` | no | Validate only — HTTP 200, no persistence |
| `async` | no | HTTP 202 with `operationId` for background apply |

Poll: `GET /v1/deploy/{kind}:apply/{operationId}` (and RuntimeClass delete status route).

Registry apply is synchronous. Model and Knowledge apply persist desired state and start artifact pulls (`pulls` in the response). ControlPlane apply is asynchronous by default (long container pull/start).

### `/v1/auth/*`

| Route | Purpose |
|-------|---------|
| `GET /v1/auth/whoami` | Caller identity and effective RBAC summary |
| `GET /v1/auth/tokens` | List active service account tokens |
| `POST /v1/auth/tokens/revoke` | Revoke token by JTI |

### `/v1/images/*`

Engine image operations: list, pull (with async status poll), load, prune, remove. Requires a healthy container engine. Image pull rejects registries with `type` other than `oci`.

### `/v1/models/*`

Model artifact operations (not container images). Operator guide: [models.md](models.md).

| Route | Purpose |
|-------|---------|
| `GET /v1/models` | List deployed models (`source` is `local` or `managed`) |
| `GET /v1/models/{name}` | Inspect (`source`; managed rows also include `uuid` and `bindRefCount`) |
| `POST /v1/models:pull` | Start async pull — HTTP 202. Body `{"name"}` retries an existing row; optional `repo`, `revision`, `registryId`, `files`, `format` upsert then pull |
| `GET /v1/models:pull/{operationId}` | Pull progress / terminal status |
| `POST /v1/models:prune` | Dangling prune (`?mode=dangling`) — unused local models (managed names kept) |
| `DELETE /v1/models/{name}` | Remove row and on-disk artifacts |

Local Model apply is refused while `watchdogEnabled` is on.

### `/v1/knowledge*`

Knowledge artifact operations (not container images, not Models). Mass noun — **`/v1/knowledges` is not registered**. Operator guide: [knowledge.md](knowledge.md).

| Route | Purpose |
|-------|---------|
| `GET /v1/knowledge` | List deployed Knowledge (`source` is `local` or `managed`) |
| `GET /v1/knowledge/{name}` | Inspect (`source`; managed rows also include `uuid` and `bindRefCount`) |
| `POST /v1/knowledge:pull` | Start async pull — HTTP 202. Body `{"name"}` retries an existing row; optional `repo`, `revision`, `registryId`, `files`, `format` upsert then pull |
| `GET /v1/knowledge:pull/{operationId}` | Pull progress / terminal status |
| `POST /v1/knowledge:prune` | Dangling prune (`?mode=dangling`) — unused local Knowledge (managed names kept) |
| `DELETE /v1/knowledge/{name}` | Remove row and on-disk artifacts |

Local Knowledge apply is refused while `watchdogEnabled` is on.

### `/v1/volumes*`

Persistent `VOLUME` claims (not BIND host paths, not secret/configmap staging). Admin RBAC: `volumes` / `volumes/prune`. Operator guide: [volumes.md](volumes.md).

| Route | Purpose |
|-------|---------|
| `GET /v1/volumes` | List claims. Private rows set `uuid`; shared rows set `uuid` null and list `consumers` (local and/or controller UUIDs) |
| `GET /v1/volumes/shared/{name}` | Inspect one shared claim |
| `DELETE /v1/volumes/{uuid}` | Destroy **private** data. Optional `?name=` and `?force=`. Does not delete `volumes/shared/` |
| `DELETE /v1/volumes/shared/{name}` | Destroy a **shared** claim. Remaining consumers or a mounted path → 409 |
| `POST /v1/volumes:prune` | Dry-run by default. Destroy requires `yes: true`. 24h grace unless `force`. Control-plane volumes are never pruned |

`DELETE /v1/system/provision?purgeVolumes=true` destroys **workload** persistent volumes only (shared only if no remaining consumers). Control-plane volumes stay.

### Microservice self routes

| Route | Transport | Purpose |
|-------|-----------|---------|
| `GET /v1/microservices/config` | HTTP | Config blob for calling microservice UUID |
| `GET /v1/microservices/control` | WebSocket | Control/message channel for calling microservice |

Both require a JWT with `iofog.org.microservice.uuid` matching a running workload.

---

## WebSockets

Upgrade paths use the same TLS trust and bearer token as HTTP. Client must send `Authorization: Bearer ...` on the upgrade request.

| Route | Use |
|-------|-----|
| `GET /v1/system/logs:stream` | Follow daemon logs |
| `GET /v1/ms/{id}/logs:stream` | Follow container logs |
| `GET /v1/ms/{id}/exec/sessions/{sessionId}:attach` | Interactive exec terminal |
| `GET /v1/microservices/control` | Microservice control channel (self) |

Server → client binary opcodes on the control channel:

| Opcode | Meaning | Client action |
|--------|---------|---------------|
| `0x9` | Ping | Respond with pong |
| `0xA` | Pong | — |
| `0xB` | ACK | — |
| `0xC` | Config changed | `GET /v1/microservices/config` |
| `0xF` | Resource limits changed | Re-read agent limits / adjust behavior |

Exact message framing is defined in OpenAPI operation descriptions.

---

## RuntimeClass gating

RuntimeClass endpoints under `/v1/deploy/runtimeclasses*` require:

1. **Full** build flavor (embedded engine bundle present)
2. `containerEngine=edgelet` in active config

Other combinations return HTTP 400 with `INVALID_ARGUMENT` (see error taxonomy above). Operator guide: [container-engine.md](container-engine.md).

---

## Contract stability

EdgeletAPI in this repository is **v1-only**. The canonical contract is [edgelet-api-v1-openapi.yaml](edgelet-api-v1-openapi.yaml).

### Locked decisions

1. Route namespace `/v1/...` (no v2 fallback routes)
2. Dual transport: Unix socket `/run/edgelet/edgelet.sock` and HTTPS/WSS on port 54321
3. Bootstrap vs provisioned JWT policy (see Authentication)
4. RuntimeClass surface is part of v1 with full+edgelet gating
5. CLI remains a thin client — no daemon logic in `cmd/edgelet` beyond transport

### Breaking changes

The following require an explicit contract amendment before merge:

- Endpoint path or HTTP method change from the OpenAPI baseline
- Auth requirement changes (bootstrap/provisioned acceptance rules)
- Request/response schema changes for existing operations
- Namespace change away from `/v1/...`

Non-breaking additions (new optional fields, new routes with RBAC entries) should still update OpenAPI, RBAC mapping, and this document together.

---

## Implementation map

| Layer | Package | Role |
|-------|---------|------|
| HTTP server | `internal/edgeletapi` | Listeners, router, middleware |
| Handlers | `internal/edgeletapi/handlers` | Request parsing, envelope |
| RBAC | `internal/edgeletapi/rbac.go` | Claim → permission evaluation |
| Domain | `internal/runtimeapi` | Facade to processmanager, fieldagent, store |
| Auth | `internal/auth` | JWT validation, PKI, token file |

Runtime module details: [modules/edgeletapi.md](modules/edgeletapi.md).
