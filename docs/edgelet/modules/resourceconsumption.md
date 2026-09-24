# Resource Consumption Manager

The resource consumption manager samples **edgelet stack and host** usage against configured limits and publishes results to StatusReporter. It also participates in limit enforcement signaling for the agent.

**Code:** `internal/resourceconsumption/`

## Purpose

- Sample control-plane RSS/CPU via gopsutil (`process.MemoryInfo`, `process.Percent`)
- When `containerEngine=edgelet` on an embedded build, include the `--edgelet-containerd-child` data-plane process(es)
- Compare stack totals against configured limits (bytes / CPU percentage)
- Update `ResourceConsumptionManagerStatus` on StatusReporter
- Collect initial sample immediately on start (no wait for first tick)

## Metrics

| Field | Meaning |
|-------|---------|
| `agentCpu` / `agentMemory` | Control-plane `edgelet daemon` process (cores / bytes) — **local API only** |
| `runtimeCpu` / `runtimeMemory` | Embedded containerd child (embedded engine only; cores / bytes) — **local API only** |
| `edgeletStackCpu` / `edgeletStackMemory` | Stack totals (cores / bytes) — **local API only** |
| `cpuUsage` / `memoryUsage` | **Edgelet stack total** (agent + runtime when available) — also sent to Pot in `PUT status`. Stack CPU uses per-core scale (`100` = one logical CPU); human CLI prints stack breakdown in **cores** and hides duplicate `cpuUsage` when `edgeletStackCpu` is present. |
| `diskUsage` | Edgelet data directory usage in **GiB** (not host filesystem total) |
| `systemCpus` | Logical CPU count |
| `systemOs` / `systemOsVersion` / `systemKernelVersion` | GOOS family (`linux`, `darwin`, `windows`); Linux `PRETTY_NAME` or platform+version; Linux kernel only (`""` on non-Linux) |
| `systemTotalMemory` / `systemAvailableMemory` | Host RAM capacity / free (bytes) |
| `systemTotalDisk` / `systemAvailableDisk` | Filesystem of `diskDirectory` total / free (bytes) |
| `systemTotalCpu` | Host CPU busy 0–100% (not core count) |

External `docker` / `podman` engines report agent-only stack totals (no runtime child tracking).

## Dependencies

| Depends on | Reason |
|------------|--------|
| `statusreporter` | Publish usage metrics |
| `config` | Limits and poll frequency |
| `gopsutil` | Process and host metrics |
| `pkg/containerd` | Discover embedded containerd child PIDs on Linux |

| Used by | Reason |
|---------|--------|
| `supervisor` | Started early (before Field Agent) |

## Lifecycle

### Start

1. `InstanceConfigUpdated()` — load limits from config
2. `collectUsageData()` immediately
3. Start periodic worker at configured frequency

### Config update

`InstanceConfigUpdated()` refreshes disk/CPU/memory limits and may restart sampling logic.

## Configuration

Limits loaded from config profile (typical keys):

| Key | Unit |
|-----|------|
| Disk limit | bytes |
| Memory limit | bytes |
| CPU limit | stack cores × 100 (5–400; default 80 = 0.8 CPU; monitor-only) |

Exact YAML names match `config.yaml` profiles — see default config in `internal/config/default_config.yaml`.

## Module status

| Property | Value |
|----------|-------|
| StatusReporter index | `0` (`utils.ResourceConsumptionManager`) |
| First slot in `modulesStatus[]` | Resource Consumption |

Status fields include stack breakdown plus legacy `memoryUsage`, `cpuUsage`, `diskUsage`.

## External APIs

Exposed indirectly via `GET /v1/system/status` resource section and Controller status POST.

## Observability

- Log module: `"Resource Consumption Manager"`
- Initial and periodic debug logs with MiB/GiB formatting
- Warn when multiple embedded containerd child PIDs are detected

## Failure modes

| Symptom | Typical cause |
|---------|----------------|
| Zero usage reported | gopsutil error on platform |
| `runtimeDegraded=true` | Embedded engine configured but containerd child not running |
| Limit warnings | Stack usage exceeded configured thresholds |

## Code map

| File | Role |
|------|------|
| `manager.go` | Sampling loop, limit comparison, status updates |
| `stats.go` | Parallel process/host sampling and CPU smoothing |
| `runtime_linux.go` | Embedded runtime PID discovery |
| `host_cpu_linux.go` | Linux host CPU fallback |

Related: [statusreporter.md](statusreporter.md), [supervisor.md](supervisor.md).
