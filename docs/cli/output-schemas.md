# edgelet CLI JSON output schemas

Structured output (`-o json` or `-o yaml`) emits the EdgeletAPI v1 **data** envelope contents directly (no extra CLI wrapper). Shapes below match golden fixtures in `internal/cli/output/testdata/golden/`.

## `edgelet system status -o json`

Route: `GET /v1/system/status`

| Field | Type | Description |
|-------|------|-------------|
| `connectionToController` | string | Controller connectivity summary |
| `agentCpuPercent` | number | Control-plane CPU percent |
| `agentMemoryMiB` | number | Control-plane RSS in MiB |
| `runtimeCpuPercent` | number | Embedded containerd child CPU (embedded engine only) |
| `runtimeMemoryMiB` | number | Embedded containerd child RSS in MiB |
| `runtimeAvailable` | boolean | Embedded runtime process present |
| `runtimeDegraded` | boolean | Embedded engine configured but runtime child missing |
| `edgeletTotalCpuPercent` | number | Edgelet stack CPU total |
| `edgeletTotalMemoryMiB` | number | Edgelet stack memory total in MiB |
| `cpuUsage` | number | Edgelet stack CPU total (controller alias) |
| `edgeletDaemon` | string | Daemon lifecycle state (e.g. `running`) |
| `memoryUsage` | number | Edgelet stack memory total in MiB (controller alias) |
| `diskUsage` | string | Optional disk summary |
| `runningMicroservices` | number | Optional running MS count |
| `systemAvailableDisk` | string | Optional free disk |
| `systemAvailableMemory` | string | Optional free memory |
| `systemTime` | string | Optional agent time |
| `systemTotalCpu` | string | Host CPU usage percent |
| `availableNetworkInterfaces` | string | Comma-separated interfaces |
| `availableRuntimes` | string or string[] | Discovered runtime handler names |
| `runtimeClasses` | object[] | Applied classes `{ name, handler, source }` (`local` \| `managed`), sorted by name. Empty on docker/podman |
| `availableCdiDevices` | string[] | Unique sorted fully-qualified CDI names. Empty on docker/podman/desktop |

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

## `edgelet ms inspect -o json`

Route: `GET /v1/ms/{id}`

Default human output is the full inspect JSON (`raw.engineInspect` included). `--summary` prints the short card. Structured `-o json` / `-o yaml` is the inspect object.

| Field | Type | Description |
|-------|------|-------------|
| `uuid` | string | Microservice UUID |
| `state` | string | Real runtime state (`RUNNING`, `EXITING`, `STUCK_IN_RESTART`, …). Crash-loop backoff does **not** invent a new state name |
| `podId` | string | Pause/sandbox id on the edgelet engine; same as `containerId` on docker/podman; omitted when unknown |
| `errorMessage` | string | Current failure. Kept through STARTING / UPDATING / brief RUNNING. Cleared to `""` after **30 seconds** of continuous RUNNING |
| `lastError` | string | Last crash text. Not cleared on recovery. Omitted when empty |
| `lastErrorAt` | integer | Unix milliseconds for `lastError`. Omitted when 0 |
| `restartCount` | integer | Real restart events since last operator rebuild. Omitted when 0 |
| `statusText` | string | Wait/fail text when a bound model or Knowledge is still downloading or Failed |
| `models` | object | Catalog bind (`bindPath`, `permissions`, `items[].name`) when bound |
| `knowledge` | object | Catalog bind (`bindPath`, `permissions`, `items[].name`) when bound |
| `raw.engineInspect` | object | Engine inspect payload (full inspect only) |

Docker/Podman crash text looks like `exitCode=N oomKilled=…` (plus `error=…` when the engine error is set). The embedded engine keeps `CRI reason=…`. Last crash text for controller-managed workloads is in-memory; an agent restart may drop it until the next failure.

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

Apply response includes async poll metadata:

| Field | Type | Description |
|-------|------|-------------|
| `status` | string | Terminal or in-progress status |
| `deploymentId` | string | Deployment identifier on success |
| `operationId` | string | Async operation id when polling |
| `stage` | string | Latest apply stage |
| `stages` | array | Observed stage sequence (CLI augmentation) |

Dry-run (`--dry-run`) returns validate payload:

| Field | Type | Description |
|-------|------|-------------|
| `valid` | boolean | Manifest validity |
| `kind` | string | Detected manifest kind |
| `name` | string | Manifest name |
| `apiVersion` | string | Manifest apiVersion |

## Streaming commands (no JSON)

These reject or ignore `-o json|yaml`:

- `edgelet ms logs --follow`
- `edgelet ms exec`
- `edgelet system logs --follow`

## Error shape (stderr, human)

Human mode prints `Error[CODE]: message` on stderr. There is no structured error JSON on stdout today; non-zero exit codes follow [README.md](README.md#exit-codes).
