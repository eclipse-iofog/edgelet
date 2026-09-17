# Pruning Manager

The pruning manager schedules **image (and related) prune operations** for the active container engine. It protects images referenced by any configured microservice and delegates to `ContainerEngine.PruneImages()` for engine-neutral behavior.

**Code:** `internal/pruning/`

## Purpose

- Threshold-based prune when disk usage crosses configured limit
- Interval-based scheduled prune (`frequencyInterval`)
- Protect all microservice images from configured callback (running or stopped)
- Support docker, podman, and edgelet/containerd engines via injected engine

## Dependencies

| Depends on | Reason |
|------------|--------|
| `config` | Threshold, frequency, disk directory |
| `statusreporter` | Pruning status fields |
| `ContainerEngine` | Injected by Supervisor |

| Used by | Reason |
|---------|--------|
| `supervisor` | Started after Process Manager; engine + MS callback wired |
| `runtimeapi` | On-demand `POST /v1/system/prune`, `POST /v1/images:prune` |

## Lifecycle

### Start

`GetInstance().Start()`:

1. Reset contexts (supports supervisor restart)
2. Start **threshold worker** (main ctx) — watches disk usage
3. Start **frequency worker** (separate ctx) — periodic prune at `frequencyInterval`

`SetEngine(engine)` and `SetGetMicroservicesCallback()` must be set by Supervisor before or during start.

### Config update

`ChangePruningFreqInterval()` cancels only frequency worker and restarts with new interval; triggers immediate prune when enabling (0→N) or changing frequency.

## Prune order (scheduled)

When prune runs:

1. Optional container prune hook
2. Optional volume prune hook
3. Image prune via engine — **excluding** protected microservice images
4. Dangling model prune (unused **local** model rows/trees plus unreferenced OCI blobs; managed fleet names are kept)

On-demand API prune follows similar engine delegation paths through `runtimeapi.Facade.Prune()`. Model prune: `POST /v1/models:prune` / `edgelet model prune` — see [../models.md](../models.md). Controller `getChanges.prune` runs dangling images **and** unused local models.

## Configuration

| Key | Effect |
|-----|--------|
| `threshold` | Disk usage percentage triggering threshold prune |
| `frequencyInterval` | Seconds between scheduled prunes (0 disables frequency worker) |

Legacy top-level `edgelet prune` removed — use `edgelet system prune` or `edgelet image prune` (EdgeletAPI).

## External APIs

| Route | Role |
|-------|------|
| `POST /v1/system/prune` | System prune modes |
| `POST /v1/images:prune` | Image-focused prune |
| `POST /v1/models:prune` | Dangling model artifacts |

## Observability

- Log module: `"Docker Pruning Manager"` (historical name; all engines)
- Status fields on StatusReporter pruning section
- `isPruning` mutex prevents concurrent prune runs

## Failure modes

| Symptom | Typical cause |
|---------|----------------|
| No scheduled prune | `frequencyInterval=0` |
| Needed images removed | Microservice callback not wired; image not in latest MS list |
| Prune errors | Engine socket unavailable |

## Code map

| File | Role |
|------|------|
| `manager.go` | Workers, threshold logic, engine delegation |
| `manager_test.go` | Unit tests |

Related: [engines.md](engines.md), [supervisor.md](supervisor.md), [runtimeapi.md](runtimeapi.md).
