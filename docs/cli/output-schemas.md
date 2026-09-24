# edgelet CLI JSON output schemas

Structured output (`-o json` or `-o yaml`) emits the EdgeletAPI v1 **data** envelope contents directly (no extra CLI wrapper). Shapes below match golden fixtures in `internal/cli/output/testdata/golden/`.

## `edgelet system status -o json`

Route: `GET /v1/system/status`

| Field | Type | Description |
|-------|------|-------------|
| `connectionToController` | string | Controller connectivity summary |
| `agentCpu` | number | Control-plane CPU in **cores** (float) |
| `agentMemory` | number | Control-plane RSS in **bytes** |
| `runtimeCpu` | number | Embedded containerd child CPU in **cores** (embedded engine only) |
| `runtimeMemory` | number | Embedded containerd child RSS in **bytes** |
| `runtimeAvailable` | boolean | Embedded runtime process present |
| `runtimeDegraded` | boolean | Embedded engine configured but runtime child missing |
| `runtimeTracked` | boolean | Embedded runtime metrics are tracked |
| `edgeletStackCpu` | number | Edgelet stack CPU total in **cores** |
| `edgeletStackMemory` | number | Edgelet stack memory total in **bytes** |
| `cpuUsage` | number | Controller alias: stack CPU **per-core scale** (`100` = one logical CPU) |
| `edgeletDaemon` | string | Daemon lifecycle state (e.g. `running`) |
| `memoryUsage` | number | Controller alias: stack memory in **MiB** |
| `diskUsage` | number | Edgelet data usage under `diskDirectory` in **GiB** |
| `runningMicroservices` | number | Optional running MS count |
| `systemCpus` | number | Logical CPU count |
| `systemOs` | string | Host OS family (`linux`, `darwin`, `windows`, …) |
| `systemOsVersion` | string | Distro/release display (`PRETTY_NAME` on Linux when available; may be empty) |
| `systemKernelVersion` | string | Linux kernel version; `""` on non-Linux |
| `systemTotalMemory` | number | Host RAM capacity (bytes) |
| `systemTotalDisk` | number | `diskDirectory` filesystem size (bytes) |
| `systemAvailableDisk` | number | Free space on that filesystem (bytes) |
| `systemAvailableMemory` | number | Host available RAM (bytes) |
| `systemTime` | string | Optional agent time |
| `systemTotalCpu` | number | Host CPU usage 0–100% (not core count) |
| `availableNetworkInterfaces` | string | Comma-separated interfaces |
| `availableRuntimes` | string or string[] | Discovered runtime handler names |
| `runtimeClasses` | object[] | Applied classes `{ name, handler, source }` (`local` \| `managed`), sorted by name. Empty on docker/podman |
| `availableCdiDevices` | string[] | Unique sorted fully-qualified CDI names. Empty on docker/podman/desktop |

Human **`edgelet system status`** (no `-o json`) formats stack and host memory/disk with **GiB/MiB**, stack CPU with **cores**, and hides duplicate **`cpuUsage`** / **`memoryUsage`** when stack totals are present.

Golden fixture (minimal subset):

```json
{
  "connectionToController": "ok",
  "cpuUsage": 12,
  "edgeletDaemon": "running"
}
```

## `edgelet ms ls -o json`

Route: `GET /v1/ms`

| Field | Type | Description |
|-------|------|-------------|
| `items` | array | Microservice rows |

Each `items[]` element:

| Field | Type | Description |
|-------|------|-------------|
| `uuid` | string | Microservice UUID |
| `application` | string | Application name |
| `name` | string | Microservice name |
| `state` | string | Runtime state |
| `containerId` | string | Container identifier |
| `podId` | string | Pause/sandbox id on the edgelet engine; same as `containerId` on docker/podman; omitted when unknown |
| `image` | string | Image reference |
| `type` | string | Source/type label |

## `edgelet --version -o json` / `edgelet system version -o json`

Combined CLI + daemon payload:

| Field | Type | Description |
|-------|------|-------------|
| `cli.version` | string | CLI version |
| `cli.buildTime` | string | CLI build timestamp |
| `cli.gitCommit` | string | CLI git commit |
| `daemon.version` | string | Daemon version when reachable |
| `daemon.buildTime` | string | Daemon build timestamp |
| `daemon.gitCommit` | string | Daemon git commit |
| `daemon.allowedContainerEngine` | string | Allowed engines |
| `daemon.error` | string | Present when daemon unreachable |

## `edgelet deploy -f FILE -o json`

Route: `POST /v1/deploy`

Returns the deploy result object from the daemon (status, applied resources, errors).
