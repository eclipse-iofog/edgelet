# Supervisor

The Supervisor is the daemon orchestrator. It owns process lifecycle for all runtime modules, wires the container engine into Process Manager, handles config reload (SIGHUP), graceful shutdown, and embedded containerd watchdog behavior on Linux.

**Code:** `internal/supervisor/`

## Purpose

- Open and close the SQLite store at daemon start/stop
- Start modules in a fixed dependency order and publish status via StatusReporter
- Create and inject the `ContainerEngine` implementation based on `containerEngine` config
- Register config reload and GPS config callbacks
- Monitor EdgeletAPI listener health
- Coordinate daemon restart requests (embedded containerd exit, engine recovery)

## Dependencies

| Depends on | Reason |
|------------|--------|
| `store` | Must open DB before any module reads/writes |
| `config` | Active `config.yaml` and reload callbacks |
| Prestarted containerd | When `containerEngine=edgelet`, bootstrap starts containerd before Supervisor |

| Used by | Reason |
|---------|--------|
| `cmd/edgelet` daemon entry | Main runtime bootstrap |
| EdgeletAPI | `POST /v1/system/reload`, `POST /v1/system/stop` invoke Supervisor paths |

## Lifecycle

### Start

Entry: `(*Supervisor).Start()` in `supervisor.go`.

1. `store.Open(diskDirectory)` — runs migrations, integrity check
2. Seed default local registries if missing
3. Start StatusReporter; set daemon `STARTING`
4. Start Network → ResourceConsumption → FieldAgent
5. Instantiate container engine (`edgelet`, `docker`, or `podman`)
   - External engines: retry with degraded mode + background recovery if socket unavailable
   - Edgelet engine: requires prestarted `containerdSvc`; starts socket watchdog goroutine
6. `processmanager.Start(engine, fieldAgent)`
7. Optional HealthcheckRunner when engine is `edgelet`
8. GPS → EdgeletAPI (waits up to 15s for listeners)
9. Pruning Manager (engine + microservice image list wired)
10. Edge Guard Manager

Host hardware/USB inventory posting is not started. Edge Guard remains.
11. Set daemon `RUNNING` (or `WARNING` if external engine degraded)

### Stop

`(*Supervisor).Stop()` stops modules in reverse order, drains Process Manager tasks, stops EdgeletAPI, and calls `store.Close()` last (WAL truncate checkpoint).

### Config reload

`ReloadConfig()` is registered on `config.SetReloadCallback`. Successful reload propagates to Field Agent (`Update()`), engine URL changes, DNS resolver, and modules that subscribe to config change events. Failed reload leaves prior on-disk config authoritative.

## Configuration

Key `config.yaml` fields affecting Supervisor:

| Key | Effect |
|-----|--------|
| `containerEngine` | `edgelet`, `docker`, or `podman` |
| `containerEngineUrl` | Engine socket/URL |
| `diskDirectory` | SQLite path root (`edgelet.db`) |
| `leaveRunningOnControlStop` | Shutdown policy for workloads |
| `monitorContainersStatusFreqSeconds` | Process Manager reconcile tick (wired at PM start) |

See [../installation.md](../installation.md) for install paths and [../deployment.md](../deployment.md) for systemd unit `edgelet.service`.

## Data and persistence

Supervisor does not own tables directly. It ensures `store` is open and passes singletons (`fieldagent`, `processmanager`, `edgeletapi`) to each other:

- Field Agent implements `MicroserviceManagerInterface` for Process Manager
- Process Manager callbacks for local/control-plane deploy are registered during wiring

## External APIs

Supervisor does not expose HTTP routes. Operator-facing control:

| EdgeletAPI route | Action |
|------------------|--------|
| `POST /v1/system/reload` | Config reload |
| `POST /v1/system/stop` | Graceful daemon stop |
| `GET /v1/system/status` | Module status array |

## Observability

- Log module name: `"Supervisor"`
- `SupervisorStatus` in status POST: `daemonStatus`, `modulesStatus[]`, `daemonLastStart`, `operationDuration`, `warningMessage`
- EdgeletAPI monitor ticker (10s): logs if local API unhealthy

## Failure modes

| Symptom | Typical cause |
|---------|----------------|
| Daemon exits on start | DB migration failure, containerd not prestarted for `edgelet` engine |
| `WARNING` daemon status | Docker/Podman socket unavailable after retry budget |
| Immediate restart | Embedded containerd unexpected exit (fail-fast handler) |
| Reload rejected | Config validation failed; Field Agent skips fog config POST |

See [../troubleshooting.md](../troubleshooting.md).

## Code map

| File | Role |
|------|------|
| `supervisor.go` | Start/stop, module wiring, API monitor |
| `engine_lifecycle.go` | Engine init, degraded recovery, containerd handlers |
| `module.go` | `Module` interface (`Start`, `Stop`, `GetName`, `GetModuleIndex`) |

Related modules: [fieldagent.md](fieldagent.md), [processmanager.md](processmanager.md), [edgeletapi.md](edgeletapi.md), [store.md](store.md).
