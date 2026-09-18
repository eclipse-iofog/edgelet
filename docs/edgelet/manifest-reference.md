# Deploy manifest reference

Edgelet accepts **`edgelet.iofog.org/v1`** YAML manifests via:

```bash
edgelet deploy -f manifest.yaml
edgelet deploy -f manifest.yaml --timeout=20m   # ControlPlane async apply
```

Validation runs in the daemon before apply. Shapes are defined in `internal/models/` and surfaced through EdgeletAPI `/v1/deploy/*`.

**Example files:** [examples/](examples/)

| Kind | Example | Guide section |
|------|---------|---------------|
| Microservice | [examples/microservice.yaml](examples/microservice.yaml) | [Microservice](#microservice) |
| Registry | [examples/registry.yaml](examples/registry.yaml) | [Registry](#registry) |
| Model | [examples/model.yaml](examples/model.yaml) | [Model](#model) |
| RuntimeClass | [examples/runtimeclass.yaml](examples/runtimeclass.yaml), [examples/runtimeclass-edgelet-wasmtime.yaml](examples/runtimeclass-edgelet-wasmtime.yaml) | [RuntimeClass](#runtimeclass) |
| ControlPlane | [examples/controlplane.yaml](examples/controlplane.yaml) | [ControlPlane](#controlplane) |

---

## Common fields

| Field | Value |
|-------|--------|
| `apiVersion` | **`edgelet.iofog.org/v1`** (required) |
| `kind` | `Microservice`, `Registry`, `Model`, `RuntimeClass`, or `ControlPlane` |

Legacy `apiVersion: v3` and Java-era kinds are rejected.

---

## Microservice

Local or operator-managed workload deployed through Edgelet (not Pot controller snapshot).

**Annotated reference:** [examples/microservice.yaml](examples/microservice.yaml) lists every YAML key with inline comments. Catalog bind lifecycle: [models.md](models.md#bind-into-a-microservice). Engine coverage: [container-engine.md](container-engine.md).

### Schema vs implemented

| Field | Status |
|-------|--------|
| `metadata.namespace` | Parsed; **not used** — runtime application is always `edgelet` |
| `spec.config` | Parsed; **not applied** |
| All other fields in the example | **Applied** (`healthCheck` and `annotations` included) |

`containerEngine: edgelet` and `docker` apply every container field below. `podman` reuses the Docker HostConfig mapping; `cdiDevices` is not wired on Podman — see [container-engine.md](container-engine.md#podman-field-coverage).

### Top-level shape

```yaml
apiVersion: edgelet.iofog.org/v1
kind: Microservice
metadata:
  name: <dns-label>           # required
  namespace: edgelet          # optional; use edgelet for local deploy scope
  labels: {}                  # optional user labels (protected keys stripped)
spec:
  image: <image-ref>          # required
  registry: <id>              # optional registry row ID
  models: { ... }             # optional catalog bind — see below
  container: { ... }          # see below
  schedule: <int>             # optional ordering hint
  config: {}                  # optional opaque config map (not applied)
```

### `spec.models` (catalog bind)

Omit `spec.models` (or use an empty `items` list) when the workload does not bind artifacts. When `items` is non-empty:

| Field | Type | Notes |
|-------|------|-------|
| `bindPath` | string | **Required.** Absolute **container** path. The catalog is one bind of a per-microservice projection at this path. |
| `permissions` | string | `ro` (default) or `rw`. Catalog-level only — no per-item mode. |
| `items[].name` | string | DNS-1123 Model `metadata.name`. Duplicate names are a validate error. Never send a host content path. |

In the container, each item appears at **`{bindPath}/{name}/`** and lists that model's Ready **`content/`** files. Example: `bindPath: /models` + `name: test-model` → `/models/test-model/`.

`bindPath` and each `{bindPath}/{name}` must not collide with a volume `containerDestination` or `tmpfs.containerPath`.

Local deploy binds **local** models only. The container starts only when every named item is **Ready**; unknown or Failed names are a validate error; Pending/Pulling persist the workload as **QUEUED** with wait text. Add/remove/re-pull of items updates the projection **in place** (no recreate). Changing `bindPath` or catalog `permissions` **does** recreate. `edgelet model rm` is refused while any microservice still names that model.

### `spec.container` (common fields)

| Field | Type | Notes |
|-------|------|-------|
| `hostNetworkMode` | bool | Host network — disables bridge DNS |
| `isPrivileged` | bool | Privileged container |
| `runAsUser` | string | User ID or name. Must not contain `:` when `runAsGroup` is set |
| `runAsGroup` | string | Group ID or name (separate from `runAsUser`) |
| `readOnlyRootFilesystem` | bool | Applied. Edgelet does not auto-inject `/tmp`; add a `tmpfs` at `/tmp` if the image needs it |
| `runtime` | string | OCI runtime name (embed engine + RuntimeClass) |
| `cdiDevices` | []string | CDI device IDs (GPU, etc.); see [container-engine.md](container-engine.md#cdi-devices-gpu--accelerators) |
| `platform` | string | Platform selector when pulling |
| `ipcMode`, `pidMode` | string | Passed to engine |
| `capAdd`, `capDrop` | []string | Linux capabilities |
| `env` | `{key,value}[]` | User env (`EDGELET_*` reserved) |
| `extraHosts` | `{name,address}[]` or legacy strings | `/etc/hosts` + docker ExtraHosts |
| `ports` | `{internal,external,protocol}[]` | Port mappings |
| `volumes` | `{hostDestination,containerDestination,accessMode,type}[]` | `BIND`, `VOLUME`, or controller `VOLUME_MOUNT`. **Delete does not remove `VOLUME` data** on the embedded engine — see [volumes.md](volumes.md). |
| `tmpfs` | `{containerPath,size?,mode?}[]` | In-memory mounts. `size` is MiB. Absolute `containerPath` required |
| `sysctls` | string map | Kubernetes **safe sysctls** only (see allowlist below). `hostNetworkMode: true` rejects `net.*`. `ipcMode: host` rejects IPC-namespaced names (`kernel.shm*`, `kernel.msg*`, `kernel.sem*`, `fs.mqueue.*`). `pidMode: host` does not change sysctl validation |
| `ulimits` | map of `{soft,hard}` | Keys are Docker/RLIMIT names (see allowlist). `-1` = unlimited. Nested `cpu` is RLIMIT_CPU (seconds), not `cpus`. If neither side is `-1`, `soft` must be `<= hard`; unlimited soft requires unlimited hard. Scalar values are rejected |
| `devices` | `{hostPath,containerPath,permissions?}[]` | `hostPath` must be under **`/dev`**. `permissions` is Docker-style `r`/`w`/`m` (default `rwm`) |
| `entrypoint` | []string | Omit or `[]` = image default ENTRYPOINT (empty argv is not sent to the engine) |
| `commands` | []string | Omit or `[]` = image default CMD. Controller JSON may send `cmd` as an alias of `commands` |
| `workingDir` | string | Absolute container working directory |
| `cpuSetCpus` | string | cpuset |
| `cpus` | float | Docker `--cpus` (float CPU count). Not node `cpuLimit` percent |
| `memoryLimit` | int64 | Memory limit (**MiB**) |
| `memoryReservation` | int64 | Soft reservation (**MiB**). Allowed without `memoryLimit` |
| `memorySwap` | int64 | **`-1`** unlimited; else MiB **memory+swap total** (Docker `--memory-swap`). Requires `memoryLimit` unless `-1` |
| `shmSize` | int64 | `/dev/shm` size (**MiB**), not a generic tmpfs entry |
| `annotations` | map | **Applied** as container annotations |
| `healthCheck` | object | **Applied.** `test` argv; `interval`, `timeout`, `startPeriod`, `retries` in **seconds** |

There is no per-microservice `stopSignal`. The image STOPSIGNAL and engine default SIGTERM apply.

#### Sysctl allowlist

`kernel.shm_rmid_forced`, `net.ipv4.ip_local_port_range`, `net.ipv4.tcp_syncookies`, `net.ipv4.ping_group_range`, `net.ipv4.ip_unprivileged_port_start`, `net.ipv4.ip_local_reserved_ports`, `net.ipv4.tcp_keepalive_time`, `net.ipv4.tcp_fin_timeout`, `net.ipv4.tcp_keepalive_intvl`, `net.ipv4.tcp_keepalive_probes`, `net.ipv4.tcp_rmem`, `net.ipv4.tcp_wmem`, `net.ipv4.tcp_slow_start_after_idle`, `net.ipv4.tcp_notsent_lowat`.

#### Ulimit allowlist

`core`, `cpu`, `data`, `fsize`, `locks`, `memlock`, `msgqueue`, `nice`, `nofile`, `nproc`, `rss`, `rtprio`, `rttime`, `sigpending`, `stack`. Omit unused names.

### Apply

```bash
edgelet deploy -f examples/microservice.yaml
edgelet ms ls --source local
edgelet ms inspect <uuid-or-name>
```

`edgelet ms inspect` prints the full inspect JSON by default (`models` catalog plus `raw.engineInspect`). `--summary` is the short card. Wait/fail `statusText` is on the object when a bound model is still downloading or Failed. Crash fields: `errorMessage` (current; clears after 30s continuous RUNNING), `lastError` / `lastErrorAt` (last crash; not cleared on recovery), `restartCount` (omitted when 0). Docker/Podman text is `exitCode=N oomKilled=…`; the embedded engine keeps `CRI reason=…`.

DNS: [dns.md](dns.md) · Metadata: [workload-metadata.md](workload-metadata.md)

---

## Registry

Credentials for **container image** and **model artifact** pulls, stored in local SQLite.

**Annotated reference:** [examples/registry.yaml](examples/registry.yaml).

Built-in rows (cannot be edited or removed): **id 1** `docker.io` (`oci`), **id 2** `from_cache` (`oci`), **id 3** `https://huggingface.co` (`hf`). User registries start at **id 4**.

```yaml
apiVersion: edgelet.iofog.org/v1
kind: Registry
spec:
  id: 10                      # optional — upsert that local id; omit to allocate a new id
  type: oci                   # oci (default) or hf
  url: <registry-host>        # required — host, or Hub/enterprise base URL
  private: true|false         # required
  username: <string>          # required when private=true and type=oci; optional for hf
  password: <string>          # required when private=true (Hub token when type=hf)
  email: <string>             # optional — oci only
  ca: <base64-pem>            # optional — extra CA bundle
  insecure: false             # optional — default false
```

| Field | Notes |
|-------|--------|
| `spec.id` | When set, upsert that row. When omitted, Edgelet allocates the next unused id after built-ins (4+). Ids 1–3 are refused. Same user id with a different `(type, url)` is a validate error. |
| `spec.type` | `oci` (default) or `hf`. Microservice image pull and `edgelet image pull` require **`oci`**. |
| `spec.ca` | Base64-encoded PEM. Applied to both `oci` and `hf` when set. |
| `spec.insecure` | Default `false`. When `true`, allow `http://` URLs and skip TLS certificate verification for `https://`. |
| `spec.email` | Rejected when `type: hf`. |

### Apply

```bash
edgelet deploy -f examples/registry.yaml
edgelet registry ls
edgelet registry inspect 10
```

Registry apply is **synchronous**. `edgelet registry ls` / `inspect` show **`type`** and **`insecure`** (secrets are not printed unless `--password-plain`). Treat YAML as sensitive.

---

## Model

AI model artifact desired state. Pulls store files under `{diskDirectory}/models/` — not through the container engine. Operator guide: [models.md](models.md).

**Annotated reference:** [examples/model.yaml](examples/model.yaml).

```yaml
apiVersion: edgelet.iofog.org/v1
kind: Model
metadata:
  name: llama-2-7b-q2k        # required — DNS-1123 label; on-disk directory name
  labels: {}                  # optional
spec:
  repo: org/name              # required — upstream path, no scheme or host
  revision: <pin>             # optional — see revision table
  registry: 3                 # required — registry row id (type selects the adapter)
  files:                      # HF only; ignored for oci
    - weights.gguf
  format: gguf                # optional — gguf, safetensors, onnx, pytorch, tensorrt, unknown
```

| Field | Notes |
|-------|--------|
| `metadata.name` | Lowercase DNS-1123 label (no `/`). Upsert key. |
| `spec.repo` | Hub repo id or OCI repository path **without** registry host. |
| `spec.revision` | OCI empty → `latest`; `sha256:` + 64 hex → digest; else tag. HF empty → `main`; 40-char hex → commit. Floating refs (`latest`, `main`, branches, tags) set `revisionFloating: true` and log a warning. |
| `spec.registry` | Required. Must exist; `hf` vs `oci` must match the source. |
| `spec.files` | **HF only.** Empty list → Hub snapshot at the pinned revision. Multi-`*.gguf` repos require an explicit list or glob. Globs: `*`, `**`, `?`. **Ignored for OCI** (full artifact). |
| `spec.format` | Hint only; does not change pull behavior. |

Deploy apply persists the desired row and starts artifact download. The CLI waits until each model is **Ready** or **Failed**. `--dry-run` validates only. `edgelet model pull <name>` retries an existing row; with `--repo` and `--registry` it upserts the same row then pulls. Spec generation bumps also re-pull on reconcile.

### Apply

```bash
edgelet deploy -f examples/model.yaml
edgelet model ls
edgelet model inspect llama-2-7b-q2k
```

---

## RuntimeClass

Maps a **handler name** to OCI runtime configuration on **`containerEngine: edgelet`** (linux embed only).

> **Naming:** `containerEngine: edgelet` is the embedded engine product. WASM workload runtimes use distinct handler keys — for example **`edgelet-wasmtime`** (Datasance shim, `io.containerd.edgelet.v2`) vs upstream **`wasmtime`** (`io.containerd.wasmtime.v1`). See [examples/runtimeclass-edgelet-wasmtime.yaml](examples/runtimeclass-edgelet-wasmtime.yaml).

```yaml
apiVersion: edgelet.iofog.org/v1
kind: RuntimeClass
metadata:
  name: <dns-label>           # required; lowercase DNS label (e.g. edgelet-wasmtime)
handler: <handler>            # required; catalog handler (e.g. edgelet-wasmtime, spin)
```

Reserved name: **`crun`** (built-in default).

### Apply

```bash
edgelet deploy -f examples/runtimeclass.yaml
edgelet runtimeclass ls
```

Reference microservice `spec.container.runtime` to the RuntimeClass name. See [container-engine.md](container-engine.md).

---

## ControlPlane

Deploys **one** Datasance Controller container per Edgelet node (optional — remote `controllerUrl` is valid without local ControlPlane).

**Annotated reference:** [examples/controlplane.yaml](examples/controlplane.yaml) lists every YAML key (active + commented optional blocks).

### Required fields

| Field | Notes |
|-------|-------|
| `spec.controller.image` | Controller container image |
| `spec.auth.mode` | `embedded` or `external` |
| `spec.auth.bootstrap` | Required when `mode: embedded` (`username`, `password` with complexity rules) |
| `spec.auth.issuerUrl` + `spec.auth.client` | Required when `mode: external` |

### Forbidden fields

| Field | Reason |
|-------|--------|
| `metadata.labels` | Rejected at validate |
| `spec.siteCA`, `spec.localCA` | Import via Controller REST after deploy |

```yaml
apiVersion: edgelet.iofog.org/v1
kind: ControlPlane
metadata:
  name: <ms-name>             # required; DNS-1123 label
  namespace: <namespace>      # optional; default applied if empty
spec:
  controller:
    image: <image>            # required
    registry: <id>            # optional
    port: 51121               # optional API port
    publicUrl: <url>          # recommended — CONTROLLER_PUBLIC_URL
    trustProxy: true|false    # optional
  console:
    port: 8008                # optional — host port 80 maps here
    url: <url>                # optional — CONSOLE_URL
  auth:                         # required
    mode: embedded|external
    insecureAllowHttp: false
    insecureAllowBootstrapLog: false
    bootstrap:                  # required when mode=embedded
      username: admin
      password: "<secret>"      # ≥12 chars, 1 uppercase, 1 special
    issuerUrl: <url>            # required when mode=external
    client:                     # required when mode=external
      id: <id>
      secret: <secret>
    consoleClient: ecn-viewer
    consoleClientEnabled: false
    rateLimit: { enabled, maxRequestsPerWindow, windowMs }
    sessionStore: { type, ttlMs, secret }
    tokenTtl: { accessTokenTtlSeconds, refreshTokenTtlSeconds }
    oidcTtl: { interactionTtlSeconds, grantTtlSeconds, sessionTtlSeconds, idTokenTtlSeconds }
  systemMicroservices:        # optional router/nats image maps per arch
    router: { amd64: "...", arm64: "..." }
    nats: { ... }
  nats:
    enabled: true|false
  # events, database, tls, vault, logLevel — see control-plane.md
```

### Rules

- **`metadata.labels` forbidden** on ControlPlane manifests.
- **`spec.siteCA` / `spec.localCA` forbidden** — import CAs via Controller REST after deploy.
- At most **one** ControlPlane row per node; delete via `edgelet controlplane delete`.

### Apply (async)

```bash
edgelet deploy -f examples/controlplane.yaml
edgelet controlplane get
```

Default poll budget **15 minutes**. See [control-plane.md](control-plane.md).

### DNS identity

FQDNs derive from `metadata.namespace` + `metadata.name` — see [dns.md](dns.md).

---

## CLI quick reference

| Action | Command |
|--------|---------|
| Apply manifest | `edgelet deploy -f <file>` |
| List local MS | `edgelet ms ls --source local` |
| List registries | `edgelet registry ls` |
| List models | `edgelet model ls` |
| List runtime classes | `edgelet runtimeclass ls` |
| Control plane status | `edgelet controlplane get` |
| Validate only | EdgeletAPI `POST /v1/deploy/microservices:validate` (and `:validate` for other kinds) |

---

## Related docs

- [models.md](models.md) — model pull, catalog bind, prune, on-disk layout
- [installation.md](installation.md) — install and provisioning
- [deployment.md](deployment.md) — production topology
- [control-plane.md](control-plane.md) — operator guide
- [CONTROLLER-HANDOFF-MODELS.md](CONTROLLER-HANDOFF-MODELS.md) — controller JSON and validation
- [edgelet-api-v1-openapi.yaml](edgelet-api-v1-openapi.yaml) — HTTP contract
