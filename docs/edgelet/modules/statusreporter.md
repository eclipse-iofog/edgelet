# Status Reporter

StatusReporter is the **in-memory aggregation hub** for module health and telemetry. It is started first among Supervisor modules and backs `GET /v1/system/status`, Field Agent status POST payloads, and portions of system info.

**Code:** `internal/statusreporter/`

## Purpose

- Hold structured status objects for each major module
- Track daemon lifecycle (`STARTING`, `RUNNING`, `WARNING`, …)
- Maintain `modulesStatus[]` array (fixed indexes; Resource Manager slot unused) for Supervisor reporting
- Background worker for system time synchronization
- Assemble composite status for API and Controller export

## Dependencies

| Depends on | Reason |
|------------|--------|
| `config` | Hostname, version metadata |
| `store` | Optional runtime class listing for status enrichment |

| Used by | Reason |
|---------|--------|
| All major modules | `Update*Status()` callbacks |
| `edgeletapi/handlers/status.go` | System status HTTP response |
| `fieldagent` | Status POST body construction |
| `resourceconsumption` | Writes CPU/memory/disk usage |

## Lifecycle

### Start

First module in Supervisor start order:

1. Initialize default status structs (`NewSupervisorStatus(7)`, etc.)
2. Start `setSystemTimeWorker()` goroutine

### Stop

Cancel context; wait for worker.

## Module index mapping

Supervisor updates `SupervisorStatus.modulesStatus[i]` using constants from `internal/utils/constants.go`:

| Index | Module |
|-------|--------|
| 0 | Resource Consumption Manager |
| 1 | Process Manager |
| 2 | Status Reporter |
| 3 | EdgeletAPI |
| 4 | Field Agent |
| 5 | *(unused — former Resource Manager; indexes are not reshuffled)* |
| 6 | GPS Manager |

`SupervisorStatus` fields: `daemonStatus`, `daemonLastStart`, `operationDuration`, `warningMessage`.

## Status object types

| Getter | Model type |
|--------|------------|
| `GetSupervisorStatus` | `SupervisorStatus` |
| `GetProcessManagerStatus` | `ProcessManagerStatus` |
| `GetFieldAgentStatus` | `FieldAgentStatus` |
| `GetEdgeletAPIStatus` | `EdgeletAPIStatus` |
| `GetResourceConsumptionManagerStatus` | Resource usage |
| `GetSSHProxyManagerStatus` | SSH tunnel state |
| `GetVolumeMountManagerStatus` | Active mount count |

Process Manager status includes the per-microservice map (runtime state, `errorMessage`, `lastError`, `lastErrorAt`, `restartCount`), registry status, and running count.

Current `errorMessage` stays set until **30 seconds** of continuous RUNNING, then is sent as `""`. `lastError` is the last crash text and is **not** cleared on recovery. For controller-managed workloads those last-crash fields are **in-memory** (lost on agent restart until the next failure). Local inspect may also surface SQLite `last_error` / `restart_count`.

## External APIs

| Surface | Role |
|---------|------|
| `GET /v1/system/status` | Primary operator view |
| Controller status POST | Field Agent aggregates reporter state |

External reporter helpers in `reporter_external.go` format Pot-compatible payloads.

## Configuration

No direct config keys; updated via module callbacks on config reload.

## Observability

- Log module: `"Status Reporter"`
- Module index: `2` (Status Reporter itself in the array)

## Failure modes

| Symptom | Typical cause |
|---------|----------------|
| Stale module status | Module failed to update on stop |
| WARNING stuck | Edge Guard or engine degraded; check `warningMessage` |
| Missing MS in status | Process Manager not publishing map |
| Last crash gone after `edgelet` restart | Controller-managed last-error is in-memory; next inspect/exit fills it |
| Dashboard still shows a crash after recovery | Grace window: `errorMessage` clears only after 30s continuous RUNNING |

## Code map

| File | Role |
|------|------|
| `reporter.go` | Singleton, getters/updaters, system time worker |
| `reporter_external.go` | Controller export formatting |

Related: [supervisor.md](supervisor.md), [fieldagent.md](fieldagent.md), [edgeletapi.md](edgeletapi.md).
