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
| `config` | Reconcile interval, engine name, logging |

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
2. Start goroutines: `containersMonitor`, `checkTasks`
3. Task queue capacity 100

### Stop

Cancel context, close task queue, wait for goroutines, drain shutdown with configurable timeout per container.

### Reconcile loop

`containersMonitor()` ticks every `monitorContainersStatusFreqSeconds` or immediately on `Update()`:

```
reconcileControlPlane()
handleLatestMicroservices()      // Controller-managed MS
reconcileLocalDeployments()      // local_workloads
deleteRemainingMicroservices()
pruneStaleProcessManagerStatuses()
updateRunningMicroservicesCount()
updateCurrentMicroservices()
```

Skips all reconcile when `IsQuiesced()` (pending engine restart).

`Update()` from Field Agent calls `notifyMonitorThread()` to avoid waiting for the next tick.

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

ControlPlane reconcile runs **before** managed microservices on each cycle so the controller container is stable before dependent workloads.

See [../workload-continuity.md](../workload-continuity.md) for restart and engine-switch behavior.

## Configuration

| Key | Effect |
|-----|--------|
| `monitorContainersStatusFreqSeconds` | Reconcile tick interval |
| `containerEngine` | Recorded in runtime state; affects healthcheck path |
| `logReconcileCycleEveryNTicks` | Structured reconcile cycle logging |

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

## Failure modes

| Symptom | Typical cause |
|---------|----------------|
| MS stuck updating | Reconcile quiesced; engine unavailable |
| Local deploy failures | `failure_count` threshold; image pull errors |
| CP container recreated | Row still in `system_control_plane`; external `docker rm` |
| `ms rm` on CP rejected | By design — use `edgelet controlplane delete` |

## Code map

| File | Role |
|------|------|
| `manager.go` | Monitor loop, `Update`, task queue integration |
| `container_manager.go` | Engine CRUD for containers |
| `controlplane_reconcile.go` | ControlPlane desired-state machine |
| `local_launch.go` | Local manifest launch paths |
| `controlplane_ops.go` | CP-specific engine operations |
| `quiesce.go` | Engine restart quiesce flag |
| `lifecycle_*.go` | Start/stop/restart implementations |

Related: [fieldagent.md](fieldagent.md), [store.md](store.md), [../container-engine.md](../container-engine.md).
