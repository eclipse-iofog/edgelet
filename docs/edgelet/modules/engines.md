# Container Engines

Edgelet routes all container operations through the **`ContainerEngine` interface** (`pkg/engine`). The `internal/engines` package is the **factory** that constructs the correct implementation from `containerEngine` config. Platform build tags determine which engines are linked.

**Code:** `internal/engines/`, implementations in `pkg/engine/{edgelet,docker,podman}/`

**Operator guide:** [../container-engine.md](../container-engine.md)

## Purpose

- Select engine at runtime: `edgelet`, `docker`, or `podman`
- Apply consistent `EngineConfig` (socket URL, API version, log dir)
- Wrap external engines with structured debug logging
- Warn when Docker/Podman sockets exist alongside `containerEngine=edgelet`

## Dependencies

| Depends on | Reason |
|------------|--------|
| `config` | `containerEngine`, `containerEngineUrl` |
| `pkg/engine/*` | Concrete engine implementations |

| Used by | Reason |
|---------|--------|
| `supervisor` | Creates engine before Process Manager start |
| `processmanager` | All container CRUD |
| `pruning` | Image prune delegation |
| `healthcheck` | Exec-based checks (edgelet engine) |
| `runtimeapi` | Image pull/list/load |

## Factory behavior

### Linux (`factory_linux.go`)

| `containerEngine` | Implementation |
|-------------------|----------------|
| `edgelet` | `pkg/engine/edgelet` — embedded containerd + CNI + DNS |
| `docker` | `pkg/engine/docker` |
| `podman` | `pkg/engine/podman` |

`NewContainerEngine(type, cfg)` returns `(ContainerEngine, error)`.

`WrapWithLoggingIfExternal()` adds API call logging for docker/podman only.

### Desktop (`factory_desktop.go`)

Only **docker** and **podman** — no embedded `edgelet` engine.

## Edgelet engine specifics

When `containerEngine=edgelet`:

- Uses isolated paths: `/var/lib/edgelet-containerd/`, `/run/edgelet/containerd.sock`
- Embedded containerd started in **bootstrap** before Supervisor (not by Supervisor)
- Integrates `dnsresolver`, RuntimeClass shims, pause image, CNI
- Supports `HealthcheckEngine` exec path

`warnIfExternalRuntimePresent()` logs if host Docker/Podman sockets are visible (informational; no interference).

## External engine degraded mode

Supervisor retries docker/podman init; if socket unavailable:

- Daemon continues in **WARNING** state
- Background recovery goroutine retries until engine connects
- Process Manager reconcile may quiesce during engine switch

## Configuration

| Key | Effect |
|-----|--------|
| `containerEngine` | Factory selector |
| `containerEngineUrl` | Socket/endpoint for external engines |
| `dockerApiVersion` | Docker API version string |
| `logDirectory` | Container log root passed to edgelet engine |

## Interface surface

Key `ContainerEngine` operations (see `pkg/engine/engine.go`):

- `Init`, `PullImage`, `CreateContainer`, `Start`, `Stop`, `Remove`
- `ListContainers`, `Inspect`, `Logs`, `Exec`
- `PruneImages` (used by pruning manager)

## Observability

- Factory log module: `"ContainerEngine"`
- Engine name recorded in `runtime.GetState()` at startup
- External engine API logs via `LoggingEngine` wrapper

## Failure modes

| Symptom | Typical cause |
|---------|----------------|
| Start failure on linux edgelet | Containerd not prestarted |
| WARNING daemon | Docker/Podman socket down |
| Wrong network/DNS | Engine mismatch vs config reload |
| Crash looks empty on docker/podman | Inspect must include `exitCode=` / `oomKilled=` / `error=` when not healthily running; embedded engine keeps `CRI reason=…` |

## Code map

| File | Role |
|------|------|
| `factory_linux.go` | Linux engine construction |
| `factory_desktop.go` | Desktop engine construction |
| `factory_common.go` | Shared warnings |

Implementations: `pkg/engine/edgelet/`, `pkg/engine/docker/`, `pkg/engine/podman/`.

Related: [supervisor.md](supervisor.md), [processmanager.md](processmanager.md), [dnsresolver.md](dnsresolver.md), [healthcheck.md](healthcheck.md), [../container-engine-lifecycle.md](../container-engine-lifecycle.md).
