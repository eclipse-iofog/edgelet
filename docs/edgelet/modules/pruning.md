# Pruning Manager

The pruning manager schedules **image and unused-local-model prune operations** for the active container engine. It protects images referenced by controller-managed and local-deployed microservices, plus images still in use by running containers, and delegates to `ContainerEngine.PruneImages()` for engine-neutral behavior.

Scheduled prune does **not** delete persistent `VOLUME` data under `{diskDirectory}/volumes/data/` or `{diskDirectory}/volumes/shared/`. Reclaim those with `edgelet volume prune` — see [../volumes.md](../volumes.md).

**Code:** `internal/pruning/`

## Purpose

- Threshold-based prune when disk usage crosses configured limit
- Interval-based scheduled prune (`frequencyInterval`)
- Protect controller-managed and local-deployed microservice images from the configured callback (running or stopped)
- Protect images used by running containers (managed and unmanaged) and images with a known in-use count
- Support docker, podman, and edgelet/containerd engines via injected engine
- Never timer-delete persistent `VOLUME` directories

## Dependencies

| Depends on | Reason |
|------------|--------|
| `config` | Threshold, frequency, disk directory |
| `statusreporter` | Pruning status fields |
| `ContainerEngine` | Injected by Supervisor |

| Used by | Reason |
|---------|--------|
| `supervisor` | Started after Process Manager; engine + MS callback wired |
| `runtimeapi` | On-demand `POST /v1/system/prune`, `POST /v1/images:prune` (persistent VOLUME reclaim is `POST /v1/volumes:prune`) |

## Lifecycle

### Start

`GetInstance().Start()`:

1. Reset contexts (supports supervisor restart)
2. Start **threshold worker** (main ctx) — watches disk usage
3. Start **frequency worker** (separate ctx) — periodic prune at `frequencyInterval`

Start does **not** run an immediate frequency prune; the first scheduled image prune waits for the ticker. Disk-threshold prune still fires when usage crosses the limit.

`SetEngine(engine)` and `SetGetMicroservicesCallback()` must be set by Supervisor before or during start.

### Config update

`ChangePruningFreqInterval()` cancels only the frequency worker and restarts it with the new interval. Enabling or changing `pruningFrequency` does **not** prune immediately; the next run is the ticker. Disk-threshold prune and `edgelet system prune` stay on-demand.

## Prune order (scheduled)

When prune runs:

1. Optional unmanaged-container prune hook (not desired-state workloads)
2. Image prune via engine — **excluding** protected microservice images (controller and local) and images still referenced by running containers
3. Dangling model prune (unused **local** model rows/trees plus unreferenced OCI blobs; managed fleet names are kept)

Persistent `VOLUME` directories (`volumes/data/` and `volumes/shared/`) are **not** in this job. `edgelet system prune volumes` does not destroy them either; use `edgelet volume prune` / `POST /v1/volumes:prune`.

On-demand API prune follows similar engine delegation paths through `runtimeapi.Facade.Prune()`. Model prune: `POST /v1/models:prune` / `edgelet model prune` — see [../models.md](../models.md). Controller `getChanges.prune` runs dangling images **and** unused local models — not volumes.

## Configuration

| Key | Effect |
|-----|--------|
| `threshold` | Disk usage percentage triggering threshold prune |
| `frequencyInterval` | Seconds between scheduled prunes (0 disables frequency worker) |

Legacy top-level `edgelet prune` removed — use `edgelet system prune` or `edgelet image prune` (EdgeletAPI).

## External APIs

| Route | Role |
|-------|------|
| `POST /v1/system/prune` | System prune modes (images / optional unmanaged containers; **not** persistent VOLUME data) |
| `POST /v1/images:prune` | Image-focused prune |
| `POST /v1/models:prune` | Dangling model artifacts |
| `POST /v1/volumes:prune` | Persistent VOLUME reclaim (dry-run default) — see [../volumes.md](../volumes.md) |

## Observability

- Log module: `"Edgelet Pruning Manager"`
- Status fields on StatusReporter pruning section
- `isPruning` mutex prevents concurrent prune runs

## Failure modes

| Symptom | Typical cause |
|---------|----------------|
| No scheduled prune | `frequencyInterval=0` |
| Needed images removed | Microservice callback not wired; local deploy or running container image missing from keep-set |
| Prune errors | Engine socket unavailable |

## Code map

| File | Role |
|------|------|
| `manager.go` | Workers, threshold logic, engine delegation |
| `manager_test.go` | Unit tests |

Related: [engines.md](engines.md), [supervisor.md](supervisor.md), [runtimeapi.md](runtimeapi.md), [../volumes.md](../volumes.md).
