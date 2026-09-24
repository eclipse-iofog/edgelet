# Process Manager

The Process Manager reconciles **desired workload state** (from Controller, local manifests, and ControlPlane SQLite rows) with the active **ContainerEngine**. It runs a periodic monitor loop, a task queue for lifecycle operations, and publishes running counts to StatusReporter.

**Code:** `internal/processmanager/`

## Purpose

- Pull/create/start/stop/remove containers for managed microservices
- Reconcile `local_workloads` (CLI `edgelet deploy`) and `system_control_plane` deployments
- Map container runtime state back to status structures for Field Agent POST
- Queue lifecycle tasks (start/stop/restart/kill) from EdgeletAPI
- Honor quiesce during engine restart (`SetQuiesced`)
- Apply workload metadata labels/env via `workloadmeta` helpers

## Dependencies

| Depends on | Reason |
|------------|--------|
| `fieldagent` | `MicroserviceManagerInterface` — latest microservice list, registries |
| `engine.ContainerEngine` | Docker, Podman, or edgelet/containerd CRI |
| `store` | Local workloads, control plane row, `runtime_container_refs` |
| `network` | Bridge/network setup for workloads |
| `statusreporter` | ProcessManager status, running counts |
| `config` | Engine name, reconcile-cycle logging |

| Used by | Reason |
|---------|--------|
| `supervisor` | Started after engine wired |
| `edgeletapi` / `runtimeapi` | MS lifecycle, deploy apply, logs, exec |
| `pruning` | Image list callback from latest microservices |
| `healthcheck` | Edgelet-engine healthcheck runner |

## Lifecycle

### Start

`(*ProcessManager).Start(engine, microserviceManager)`:

1. Store engine reference and create `ContainerManager`
2. Start goroutines: `containersMonitor`, `containerStatsLoop`, `checkTasks`
3. Task queue capacity 100

### Stop

Cancel context, close task queue, wait for goroutines, drain shutdown with configurable timeout per container.

### Reconcile loop

`containersMonitor()` wakes about every 5 seconds, and immediately when a workload is marked. A pass reconciles only the workloads that are due.

A workload is reconciled when:

- its spec changes
- its container starts, exits, is OOM-killed, or is deleted
- a backoff or volume wait is due
- a catalog item it needs becomes ready or failed

A full compare of every workload still runs about once a minute.

With the embedded engine and a healthy event stream, an idle workload is not inspected every 5 seconds. If the event stream is down, inspection returns to every 5 seconds until the stream is healthy. Docker and Podman still check running-or-not every 5 seconds; a container that is not running is reconciled on that check. CPU and memory on status refresh about every 10 seconds, on `containerStatsLoop()`, separate from reconcile.

When a pass has work, due workloads run in this order:

```
reconcileControlPlane()              // when that workload is due
reconcileControllerMicroservices()   // marked controller UUIDs
reconcileMarkedLocal()               // marked local_workloads
deleteRemainingMicroservices()
pruneStaleProcessManagerStatuses()
updateRunningMicroservicesCount()
updateCurrentMicroservices()
```

An idle pass with a healthy event stream does not load containers. Reconcile is skipped while `IsQuiesced()` (pending engine restart). When the engine is ready again, the next pass is a full compare.

Startup and reconnect call `Update()`, which marks every workload once and wakes the monitor.

## Workload sources

| Source | SQLite | EdgeletAPI `source` filter |
|--------|--------|------------------------------|
| Controller | `controller_microservices` | `managed` |
| Local deploy | `local_workloads` | `local` |
| ControlPlane | `system_control_plane` | `controlplane` |

Each running workload may have a row in `runtime_container_refs` linking microservice UUID, scope (`controller` \| `local`), workload ID, and sandbox ID.

## Local and ControlPlane reconcile

Local and ControlPlane deployments use desired-state fields (`desired_state`, `runtime_state`, `generation`, `observed_generation`, `failure_count`):

- **running** — ensure container exists and matches manifest generation
- **stopped** — stop container, keep record
- **deleted** — remove container and **delete** the `local_workloads` row (no persistent tombstone). Reconcile re-reads the row before write and never inserts a missing UUID.

A local recreate is stored as starting, with the new generation not yet observed, before the old container is removed. A delete of that container does not start another container or treat the new generation as observed while that apply is still in progress.

When the ControlPlane workload is due, it is reconciled **before** managed microservices so the controller container is stable before dependent workloads.

See [../workload-continuity.md](../workload-continuity.md) for restart and engine-switch behavior.

## Configuration

| Key | Effect |
|-----|--------|
| `containerEngine` | Recorded in runtime state; affects healthcheck path and whether reconcile uses the event stream |
| `logReconcileCycleEveryNTicks` | Structured reconcile cycle logging |

The 5-second wake, the full compare (about once a minute), and the CPU and memory sample (about every 10 seconds) are built in. `config.yaml` has no keys for them.

## External APIs

Process Manager does not serve HTTP directly. EdgeletAPI routes delegate through `internal/runtimeapi`:

| Operation | EdgeletAPI examples |
|-----------|---------------------|
| List/inspect MS | `GET /v1/ms`, `GET /v1/ms/{id}` |
| Lifecycle | `POST /v1/ms/{id}/start|stop|restart|kill` |
| Local deploy | `POST /v1/deploy/microservices:apply` |
| Logs/exec | `GET /v1/ms/{id}/logs`, exec session routes |

## Observability

- Log module name: `"Process Manager"`
- StatusReporter index: `1` (`utils.ProcessManager`)
- `ProcessManagerStatus`: per-microservice status map, registry status, running count
- Reconcile cycle events when logging enabled (`runtimeops` emit helpers)

Debug codes: `PMCM` (containers monitor), `PMCT` (check tasks).

## Status, crashes, and restarts

Process Manager publishes per-microservice status to StatusReporter. Field Agent and `edgelet ms inspect` read the same fields.

| Field | Behavior |
|-------|----------|
| `errorMessage` | Current failure. Kept while the workload is failing, restarting, or has been RUNNING for less than **30 seconds** after a crash. After 30 seconds of continuous RUNNING, the current field is cleared to `""` |
| `lastError` / `lastErrorAt` | Last crash text and unix-ms timestamp. Overwritten on a new failure. **Not** cleared on recovery or rebuild |
| `restartCount` | Real restart events (RUNNING→EXITING, failed start, or a scheduled crash-driven recreate). Omit when 0. Reset to 0 on operator **rebuild** only |

Crash text:

| Engine | Example |
|--------|---------|
| Docker / Podman | `exitCode=1 oomKilled=false error=config missing` |
| Embedded (`edgelet`) | `CRI reason=… exitCode=… message=…` |

Crash loops delay recreate (**10s, 20s, … up to 5 minutes**). Status stays the real runtime state (`EXITING`, `CREATED`, `QUEUED` if the container is gone) — there is no extra state name for “waiting to recreate”. Operator rebuild, catalog becoming Ready, and a single non-restartable CRI recreate skip the delay.

`STUCK_IN_RESTART` means **10 real restarts in 10 minutes**, not monitor ticks. Operator **rebuild** retries. After five exhausted lifecycle tasks the workload stays **FAILED** until rebuild (existing skip-until-rebuild).

Last crash text for controller-managed workloads is **in-memory**. An agent restart may drop it until the next failure. Local and ControlPlane rows keep existing SQLite `last_error` / `restart_count` (not cleared on first start).

## Failure modes

| Symptom | Typical cause |
|---------|----------------|
| MS stuck updating | Reconcile quiesced; engine unavailable |
| Crash text missing after agent restart | Controller-managed last crash is in-memory; wait for the next exit or inspect |
| Recreate not immediate after a crash | Crash-loop backoff (10s … 5 min); status is still `EXITING` (or similar), not a new state |
| `STUCK_IN_RESTART` | Ten real restarts in ten minutes; use operator rebuild |
| Local deploy failures | `failure_count` threshold; image pull errors |
| CP container recreated | Row still in `system_control_plane`; external `docker rm` |
| `ms rm` on CP rejected | By design — use `edgelet controlplane delete` |

## Code map

| File | Role |
|------|------|
| `manager.go` | Monitor loop, `Update`, task queue, crash-loop backoff |
| `reconcile_schedule.go` | Which workloads are due on a pass |
| `container_stats.go` | CPU and memory sample loop |
| `container_manager.go` | Engine CRUD for containers |
| `status_sync.go` | Current vs last error, 30s RUNNING grace |
| `restart_checker.go` | Real restart events → `STUCK_IN_RESTART` |
| `controlplane_reconcile.go` | ControlPlane desired-state machine |
| `local_launch.go` | Local manifest launch paths |
| `controlplane_ops.go` | CP-specific engine operations |
| `quiesce.go` | Engine restart quiesce flag |
| `lifecycle_*.go` | Start/stop/restart implementations |

Related: [fieldagent.md](fieldagent.md), [store.md](store.md), [../container-engine.md](../container-engine.md), [../troubleshooting.md](../troubleshooting.md#microservice-crash--restart-loop).
