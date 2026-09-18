# Controller handoff — registries, models, RuntimeClass, status, and catalog

This document is for the **Pot / Datasance Controller** team. It is the agent-side contract for:

- Coordinated **removals** (host hardware/USB inventory and `deviceScanFrequency`)
- Registry extras and **image-pull** TLS
- Fleet **models** and **RuntimeClass**
- Microservice catalog + container fields
- `getChanges` flags and GET routes
- Fog `PUT status` (and the matching local status keys)
- Microservice last-crash extras (`lastError`, `lastErrorAt`, `restartCount`) — **no controller release required**
- Prune and watchdog

Existing Pot path prefixes stay **`/api/v3/…`**. JSON is **additive** except the coordinated removals below. Do not rename existing keys or routes.

Operator YAML: [manifest-reference.md](manifest-reference.md) · [models.md](models.md) · [container-engine.md](container-engine.md). Edgelet local API stays **`/v1/…`**.

The agent already consumes every shape below. Controller CRUD and UI can implement against this page without reading internal specs.

---

## Identity and paths

| Item | Contract |
|------|----------|
| Model identity | **`uuid`** (required string) + unique **`name`** (DNS-1123). No integer `id` |
| On-disk name | One directory per `name`: `{diskDirectory}/models/{name}/` |
| Bind items | **`name` only** — never send uuid or a host content path in `models.items[]` |
| RuntimeClass identity | **`name`** (DNS-1123) + **`handler`**. No uuid |
| `GET models` | `{controllerUrl}/agent/models` (same style as `registries`, `microservices`) |
| `GET runtimeClasses` | `{controllerUrl}/agent/runtimeClasses` |
| Older controllers | Missing additive flags or GET routes must not fail the node. Status still posts. Additive keys may be ignored |

While a node is provisioned, a **managed** model or RuntimeClass **wins** that `name`. Local apply of a managed name is rejected. Controller microservices bind **managed** model names only; local microservices bind **local** names only.

### `getChanges` flags the agent consumes

| Flag | On `true` (and on initialization, except prune) | GET |
|------|--------------------------------------------------|-----|
| `registries` | Reload registry rows including `type`, `ca`, `insecure` | existing `GET registries` |
| `models` | Replace-all `controller_models` then upsert/pull | `GET /api/v3/agent/models` → `{ "models": [ … ] }` |
| `runtimeClasses` | Replace-all `controller_runtime_classes` then apply | `GET /api/v3/agent/runtimeClasses` → `{ "runtimeClasses": [ … ] }` |
| `microserviceList` | Reload microservice list | `GET microservices` |
| `microserviceModels` | Catalog-only refresh of the same list | **same** `GET microservices` (no dedicated GET) |
| `microserviceConfig` | Config blobs on the same list | **same** `GET microservices` |
| `prune` | Dangling **images** and unused **local** models | none |

If `microserviceList` and `microserviceModels` are both true: **one GET**. Do not skip catalog in-place refresh. Do not fetch twice.

A missing `GET models` or `GET runtimeClasses` route is an **empty list**. The agent continues.

---

## Coordinated removals

These are **not** ignored for older controllers — drop them in the same release train as the agent.

### Hardware / USB inventory

Stop calling, and remove UI for:

| Method | Path | Notes |
|--------|------|--------|
| `PUT` | `{controllerUrl}/agent/hal/hw` | Agent no longer posts host hardware inventory |
| `PUT` | `{controllerUrl}/agent/hal/usb` | Agent no longer posts USB inventory |
| `GET` | hardware / USB inventory routes that existed for HAL | Agent no longer polls localhost HAL |

There is no Resource Manager module. Do not wait for HW/USB payloads.

**Edge Guard is not HAL.** Host fingerprint / deprovision attestation stays. Do not remove Edge Guard CRUD or `edgeGuardFrequency`.

### `deviceScanFrequency`

Remove from:

- Agent YAML
- `GET config` / `PATCH config` key maps
- Console config forms

The agent does **not** accept or ignore the key. Sending it is a coordinated drop, not a silently skipped extra.

---

## Registry object (existing payload, extras)

`type` defaults to `"oci"` when omitted.

```json
{
  "id": 5,
  "url": "https://huggingface.co",
  "isPublic": false,
  "userName": "",
  "password": "hf_…",
  "userEmail": "",
  "type": "hf",
  "ca": "",
  "insecure": false
}
```

| Field | Type | Notes |
|-------|------|--------|
| `type` | string | `"oci"` (default) or `"hf"` |
| `ca` | string | Optional base64 PEM CA bundle. **Additional trust** — does not replace system CAs |
| `insecure` | boolean | Default `false`; `true` allows `http://` **and** skips TLS verify |

Existing **`registries: true`** on `getChanges` reloads these extended rows.

### Image pull TLS

| Engine | `ca` / `insecure` on **container image** pull |
|--------|-----------------------------------------------|
| **`edgelet`** | Applied (same semantics as model artifact pull) |
| **`docker`** / **`podman`** | **Not applied.** Those engines use daemon credentials only. Document the gap in UI; do not treat it as an agent failure |

Image pull still **rejects** `type: hf`. Microservice images and `edgelet image pull` require `type: oci`.

---

## Model object

```json
{
  "uuid": "3f2c8a1e-2b64-4c0d-9f11-0a1b2c3d4e5f",
  "name": "test-model",
  "repo": "second-state/Llama-2-7B-Chat-GGUF",
  "revision": "064fe43ea8c1e1f93477ef4a170bdc2b244ef02c",
  "registryId": 5,
  "files": ["llama-2-7b-chat.Q5_K_M.gguf"],
  "format": "gguf"
}
```

| Field | Type | Notes |
|-------|------|--------|
| `uuid` | string | **Required.** Controller primary key. No integer `id` |
| `name` | string | **Required.** Unique in the controller. DNS-1123; matches Edgelet `metadata.name` |
| `repo` | string | Upstream path **without** host |
| `revision` | string | Pin; empty means `latest` (OCI) or `main` (HF) |
| `registryId` | integer | Registry row id; `type` must match the pull adapter |
| `files` | string[] | HF only; ignored for OCI |
| `format` | string | Optional hint: `gguf`, `safetensors`, `onnx`, `pytorch`, `tensorrt`, `unknown` |

Unknown extra keys on the model object are ignored.

### `GET /api/v3/agent/models`

Response envelope:

```json
{
  "models": [
    {
      "uuid": "3f2c8a1e-2b64-4c0d-9f11-0a1b2c3d4e5f",
      "name": "test-model",
      "repo": "second-state/Llama-2-7B-Chat-GGUF",
      "revision": "064fe43ea8c1e1f93477ef4a170bdc2b244ef02c",
      "registryId": 5,
      "files": ["llama-2-7b-chat.Q5_K_M.gguf"],
      "format": "gguf"
    }
  ]
}
```

Rows missing `uuid` or `name` are skipped. A controller with no models route is treated as an empty list (agent continues).

### `getChanges` `models`

| Flag | Agent behavior |
|------|----------------|
| `models: true` (or initialization) | `GET models` → replace-all `controller_models` → upsert/pull |
| `models: false` or omitted | No reload. Status still includes the additive fog keys below |
| `registries: true` | Reload registry rows including `type`, `ca`, `insecure` |

---

## RuntimeClass (fleet)

Attach only when the agent’s `containerEngine` is **`edgelet`**. Validate engine in the controller UI **before** attach. Docker and podman nodes: ignore fleet rows; **do not** fail the node. Fog `runtimeClasses` is `[]` on those engines.

### Wire object

Matches the local `kind: RuntimeClass` manifest: **`name`** + **`handler`**. Unknown extra keys are ignored.

```json
{
  "name": "spin",
  "handler": "spin"
}
```

| Field | Type | Notes |
|-------|------|--------|
| `name` | string | **Required.** DNS-1123. Unique on the node |
| `handler` | string | **Required.** Catalog or custom OCI/CRI handler |

### `GET /api/v3/agent/runtimeClasses`

```json
{
  "runtimeClasses": [
    { "name": "spin", "handler": "spin" },
    { "name": "nvidia-cdi", "handler": "nvidia-cdi" }
  ]
}
```

Rows missing `name` or `handler` are skipped. A controller with no runtimeClasses route is treated as an empty list (agent continues).

### `getChanges` `runtimeClasses`

| Flag | Agent behavior |
|------|----------------|
| `runtimeClasses: true` (or initialization) | `GET runtimeClasses` → replace-all desired rows → apply when engine is `edgelet` |
| `runtimeClasses: false` or omitted | No reload |
| Docker / podman | Snapshot is stored; apply is skipped; node stays healthy |

### Apply rules

| Rule | Behavior |
|------|----------|
| Engine | Apply only when `containerEngine=edgelet` |
| Managed wins `name` | While provisioned, a managed class owns that name. Local `kind: RuntimeClass` apply for a managed name is **rejected** |
| Catalog handlers | `spin`, `edgelet-wasmtime`, `wasmtime`, `wasmedge`, `nvidia-cdi`, and other catalog entries apply **without** bouncing the data plane |
| Non-catalog handler | **May** restart the data plane. Warn the operator in the attach UI |
| In-use delete | Refused while any microservice still pins `spec.container.runtime` to that class (agent and controller). Include blocking MS uuid in the error |
| Reserved names | Agent skips reserved runtime names on the fleet list |

Pin a microservice with `spec.container.runtime: <name>` (the RuntimeClass **name**, not `containerEngine`).

---

## Microservice extras

Additive keys on the existing microservice object. Unknown keys are ignored.

### Catalog

```json
"models": {
  "bindPath": "/models",
  "permissions": "ro",
  "items": [{ "name": "test-model" }, { "name": "qwen3-8-27b" }]
}
```

| Field | Type | Notes |
|-------|------|--------|
| `bindPath` | string | Required when `items` is non-empty. Absolute **container** path |
| `permissions` | string | `ro` (default) or `rw`. Catalog-level only |
| `items[].name` | string | DNS-1123. Duplicate names are a validate error |

Container path is always **`{bindPath}/{name}/`** = that model's Ready `content/`. One bind of a per-microservice projection. No per-item permissions. No host content path. No auto-injected model env vars.

Local MS → local models only. Controller MS → managed models only.

### Catalog-only REST (`microserviceModels`)

When the operator only changes catalog items (add / remove / re-pull names) and **not** the rest of the microservice spec:

- Set **`getChanges.microserviceModels: true`**
- Do **not** set `microserviceList`
- Agent `GET microservices` once and refreshes catalog in place

Do **not** send a full list rewrite for a catalog-only edit.

### Process argv

| Field | Notes |
|-------|--------|
| `entrypoint` | Array or omit. Empty / `[]` / omit = image default (agent does not send empty argv) |
| `commands` | Preferred argv after entrypoint. Empty / `[]` / omit = image default |
| `cmd` | **Alias of `commands`**. If both are present, **`commands` wins**. Keep `cmd` forever |

There is no per-microservice `stopSignal`.

### Container fields (agent-applied)

| Field | Type | Units / notes |
|-------|------|----------------|
| `runAsGroup` | string | Separate from `runAsUser` |
| `readOnlyRootFilesystem` | boolean | No auto-inject of `/tmp` |
| `workingDir` | string | Absolute container path |
| `cpus` | number | Docker `--cpus` (float count), not node `cpuLimit` percent |
| `memoryLimit` | integer | **MiB** |
| `memoryReservation` | integer | **MiB**; allowed without `memoryLimit` |
| `memorySwap` | integer | **`-1`** unlimited; else MiB **memory+swap total**. Requires `memoryLimit` unless `-1` |
| `shmSize` | integer | `/dev/shm` in **MiB** |
| `sysctls` | object | String map; safe-sysctl allowlist |
| `ulimits` | object | Map of `{soft, hard}` only |
| `devices` | array | `{hostPath, containerPath, permissions}` |
| `tmpfs` | array | `{containerPath, size?, mode?}`. `size` is MiB |
| `healthCheck` | object | Times in **seconds** |
| `annotations` | string or object | Applied |
| `cdiDevices` | string[] | Fully-qualified CDI names to inject into **this** workload (not host discovery) |

---

## Validation rules (copy onto Pot)

The agent still enforces these. Implement the same checks on create/update so operators see errors before sync.

### Catalog

- Empty or omitted `items` → `bindPath` not required.
- Non-empty `items` → `bindPath` required and must be an absolute container path.
- `permissions` omitted → `ro`. Only `ro` or `rw`.
- Duplicate `items[].name` → error.
- `bindPath` or `{bindPath}/{name}` colliding with a volume `containerDestination` or `tmpfs.containerPath` → error.
- Controller MS item naming a local-only model → error (agent: microservice **FAILED**).
- Local MS item naming a managed model → error.

### Start gate (agent runtime)

- Do not start until every named item is **Ready**.
- Pending/Pulling → persist; MS **QUEUED**; status explains wait for download.
- Unknown or Failed name on a controller MS → MS **FAILED** + status text (include model `lastError` when present).

### In-place vs recreate

| Change | Container |
|--------|-----------|
| Add or remove a catalog **item** (catalog already non-empty; same `bindPath` + catalog `permissions`) | **In-place** projection — no recreate |
| Model re-pull (new `content/`) | **In-place** |
| Newly added item **not Ready** | **Keep the running container and the old projection** until Ready, then atomic swing. Do not stop or QUEUED a running MS for the wait |
| No catalog → first items (empty → non-empty) | **Recreate** |
| Last item removed (non-empty → empty) | **Recreate** |
| `bindPath` or catalog `permissions` | **Recreate** |
| Image, env, ports, `rebuild`, or other spec drift | **Recreate** |

### `memorySwap`

- Omitted → no swap constraint from this field.
- `-1` → unlimited; `memoryLimit` not required.
- Any other value must be a positive MiB **memory+swap total** and **requires `memoryLimit`**.

### `runAsUser` / `runAsGroup`

- `runAsUser` containing `:` **and** `runAsGroup` set → error.

### Sysctls

Allowlist (Kubernetes safe set): `kernel.shm_rmid_forced`, `net.ipv4.ip_local_port_range`, `net.ipv4.tcp_syncookies`, `net.ipv4.ping_group_range`, `net.ipv4.ip_unprivileged_port_start`, `net.ipv4.ip_local_reserved_ports`, `net.ipv4.tcp_keepalive_time`, `net.ipv4.tcp_fin_timeout`, `net.ipv4.tcp_keepalive_intvl`, `net.ipv4.tcp_keepalive_probes`, `net.ipv4.tcp_rmem`, `net.ipv4.tcp_wmem`, `net.ipv4.tcp_slow_start_after_idle`, `net.ipv4.tcp_notsent_lowat`.

- Unknown key → error.
- `hostNetworkMode: true` → reject `net.*`.
- `ipcMode: host` → reject IPC-namespaced names (`kernel.shm*`, `kernel.msg*`, `kernel.sem*`, `fs.mqueue.*`).
- `pidMode: host` does not change sysctl validation.

### Ulimits

Allowlist: `core`, `cpu`, `data`, `fsize`, `locks`, `memlock`, `msgqueue`, `nice`, `nofile`, `nproc`, `rss`, `rtprio`, `rttime`, `sigpending`, `stack`.

- Must be `{soft, hard}` objects. Scalar values → error.
- Unknown key → error.
- `-1` = unlimited. Unlimited soft requires unlimited hard.
- If neither side is `-1`, `soft` must be `<= hard`.
- Nested `ulimits.cpu` is RLIMIT_CPU (seconds), not `cpus`.

### Devices

- `hostPath` required, absolute, and must be under **`/dev`**.
- `containerPath` required.
- `permissions` is Docker-style `r` / `w` / `m` (any combination). Empty → `rwm`.

### `tmpfs`

- `containerPath` required and absolute.
- `size` when set must be `> 0` (MiB).

### `workingDir`

- When set, must be an absolute container path.

### `cpus` / `memoryReservation` / `shmSize`

- When set, must be `> 0`.

---

## Fog status (`PUT` status)

Older controllers **ignore extra keys**. Never fail `PUT status` because a key is unknown.

### Runtime discovery vs applied classes

Keep **`availableRuntimes`**: discovered host runtime **names** only (catalog-discovered handlers that are not applied stay here).

Add:

```json
"runtimeClasses": [
  { "name": "spin", "handler": "spin", "source": "managed" }
],
"availableCdiDevices": ["nvidia.com/gpu=0"]
```

| Field | Type | Notes |
|-------|------|--------|
| `availableRuntimes` | string[] (existing) | Discovered handlers. Unchanged meaning |
| `runtimeClasses` | object[] | **Applied** classes only, sorted by `name`. Each `{ name, handler, source }` with `source` `local` \| `managed`. Docker / podman: `[]` |
| `availableCdiDevices` | string[] | Unique sorted **fully-qualified** CDI names (`nvidia.com/gpu=0`). Linux `edgelet` engine scans `/etc/cdi`, `/var/run/cdi`, plus extra `cdi_spec_dirs` from containerd `config.d`. Docker / podman / desktop: `[]` |

`availableCdiDevices` is **host discovery**. Per-microservice injection is still `cdiDevices` on the MS spec.

### Microservice status `podId`

Additive key next to `containerId` on each fog `microserviceStatus` item (same field on local inspect and `edgelet ms inspect`).

| Engine | `podId` | `containerId` |
|--------|---------|---------------|
| `edgelet` | Pause / sandbox id | App container |
| `docker` / `podman` | Same as `containerId` when that id is set | App container |

**Omit** `podId` when unknown (`omitempty`). Do not send `""`.

### Microservice status extras (current vs last crash)

**No controller release is required** for these keys. Older controllers ignore unknown fields. Never fail `PUT status`.

Existing `microserviceStatus[].errorMessage` stays populated while the workload is failing, restarting, or has been RUNNING for **less than 30 seconds** after a crash. After 30 seconds of continuous RUNNING, Edgelet sends `errorMessage:""` so the current dashboard field clears.

Additive keys on each `microserviceStatus` item (same fields on local inspect and `edgelet ms inspect`). Ignore if unknown:

```json
{
  "id": "<uuid>",
  "status": "RUNNING",
  "errorMessage": "",
  "lastError": "exitCode=1 oomKilled=false error=config missing",
  "lastErrorAt": 1726660000123,
  "restartCount": 4
}
```

| Field | Type | Notes |
|-------|------|--------|
| `errorMessage` | string (existing) | Current failure. Kept through STARTING, UPDATING, and brief RUNNING. Cleared to `""` only after **30 seconds** of continuous RUNNING. Always send explicit `""` on recovery so the dashboard field clears |
| `lastError` | string | Last crash text. **Not** cleared on recovery. Overwritten only on a new failure. Omit when empty |
| `lastErrorAt` | integer | Unix milliseconds for `lastError`. Omit when 0 |
| `restartCount` | integer | Real restart events since the last operator **rebuild**. Omit when 0. Rebuild resets this to 0 and does **not** wipe `lastError` |

Crash text:

| Engine | Format |
|--------|--------|
| `docker` / `podman` | `exitCode=N oomKilled=true\|false`, plus ` error=<engine error>` when that text is non-empty |
| `edgelet` | Unchanged `CRI reason=… exitCode=… message=…` |

Last crash text for controller-managed workloads is **in-memory**. An agent restart may drop it until the next failure.

No new `getChanges` flags. No new REST paths. `PUT status` top-level keys are unchanged.

### Model status

`modelStatus` lists **local and managed** models.

| Field | Type | Notes |
|-------|------|--------|
| `modelStatus` | string | JSON **string** (not a raw array) of status items |
| `activeModels` | integer | Count of **managed** fleet models (`controller_models` length) — **not** total status rows |
| `modelLastUpdate` | integer | Unix seconds; `0` when the list is empty |

When the controller has no models (or no `models` flag), send `modelStatus: "[]"`, `activeModels: 0`, `modelLastUpdate: 0`.

`modelStatus` item (after `JSON.parse`):

```json
{
  "uuid": "3f2c8a1e-2b64-4c0d-9f11-0a1b2c3d4e5f",
  "name": "test-model",
  "source": "managed",
  "state": "Ready",
  "digest": "sha256:…",
  "resolvedRevision": "064fe43…",
  "revisionFloating": false,
  "totalBytes": 2840000000,
  "lastError": ""
}
```

Local item (no controller uuid):

```json
{
  "name": "lab-gguf",
  "source": "local",
  "state": "Ready",
  "digest": "",
  "resolvedRevision": "",
  "revisionFloating": false,
  "totalBytes": 12000000,
  "lastError": ""
}
```

| Field | Meaning |
|-------|---------|
| `source` | `local` \| `managed` |
| `uuid` | Present on **managed** items. **Omit** on local items |
| `state` | `Pending`, `Pulling`, `Ready`, `Failed` |
| `digest` | OCI manifest digest after pull |
| `resolvedRevision` | HF commit (or resolved OCI ref) |
| `revisionFloating` | `true` when the requested revision is a floating tag/branch |
| `lastError` | Pull/reconcile error text; empty when none |

Dashboards: parse `source`; do not treat `activeModels` as “how many rows are in `modelStatus`”.

---

## Prune

`getChanges.prune: true` **must run** (not log-only):

1. Dangling **images** (existing keep-set: images referenced by microservices)
2. Unused **local** models: local Model **rows and trees** that are not bound to any microservice

Keep set for model prune:

- Every **managed** `controller_models` name (even unbound)
- Every `model_refs` name (catalog binds)
- Active pulls

Do **not** keep an unbound `local_models` row. Managed trees are not deleted by watchdog; prune may still collect unmanaged on-disk names that are not in the keep set.

Scheduled `pruningFrequency` and `edgelet model prune` use the same unused-local-model rule. Controller **prune-agent** should expect both image and local-model work.

---

## Watchdog

When `watchdogEnabled` is on:

| Behavior | Detail |
|----------|--------|
| Local models | Delete **all** local Model rows and trees (same idea as no local workloads) |
| Local `kind: Model` apply | **Refused**. Controller need not send local Model CRUD while watchdog is on |
| Managed models | Unchanged — fleet pull/reconcile continues |
| Local microservices | Already out of scope (existing watchdog) |

UI: hide or disable local Model deploy on a watchdog node. Fleet model CRUD is still valid.

---

## Local EdgeletAPI (same shapes)

`GET /v1/system/status` uses a JSON object: existing scalars stay **strings**; `runtimeClasses` and `availableCdiDevices` are typed arrays (not comma-joined strings). `edgelet system status` and `edgelet ms inspect` show `podId` when present. Inspect also shows `errorMessage`, `lastError`, `lastErrorAt`, and `restartCount` with the same omitempty rules as fog `microserviceStatus`.

---

## Controller work remaining (CRUD / UI)

The agent consume path is implemented. Remaining controller work:

Microservice last-crash extras (`lastError`, `lastErrorAt`, `restartCount`) need **no controller release**. Ignore unknown keys. Dashboards that only read `errorMessage` keep working.

- [ ] Drop HAL hardware/USB GET/PUT and any HW/USB dashboards. Keep Edge Guard.
- [ ] Drop `deviceScanFrequency` from GET/PATCH config and forms
- [ ] Registry REST: persist and return `type`, `ca`, `insecure`. Show image-pull TLS only for engine `edgelet`; document docker/podman gap
- [ ] Model CRUD (create / read / update / delete) with **`uuid` + unique `name`**
- [ ] `getChanges` flag `models` and `GET /api/v3/agent/models` → `{ "models": [ … ] }`
- [ ] RuntimeClass CRUD + attach UI: `getChanges` `runtimeClasses` and `GET /api/v3/agent/runtimeClasses` → `{ "runtimeClasses": [ { "name", "handler" } ] }`
- [ ] Attach RuntimeClass only when agent engine is `edgelet`; warn on non-catalog handlers; refuse in-use delete
- [ ] Catalog-only REST: set **only** `microserviceModels` (not `microserviceList`)
- [ ] Microservice REST: catalog + container fields; accept `cmd` and `permissions`; prefer `commands`
- [ ] Copy the validation tables above onto create/update (including in-place vs recreate and not-Ready keep-running)
- [ ] Dashboards: `availableRuntimes` (discovered); `runtimeClasses` (applied `{ name, handler, source }`); `availableCdiDevices`; MS `podId`; parse fog `modelStatus` with `source` (local omits `uuid`); `activeModels` is managed count only
- [ ] Prune-agent: dangling images **and** unused local models
- [ ] Watchdog: do not send local Model CRUD while enabled

---

## Engine note

`edgelet` and `docker` apply every new container field. Podman reuses the Docker HostConfig mapping; `cdiDevices` is not wired on Podman. Image-pull `ca` / `insecure` is **edgelet engine only**. See [container-engine.md](container-engine.md).
