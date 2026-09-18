# Model artifacts

Edgelet can pull, store, reconcile, and prune **AI model artifacts** on the node. Models are a first-class deploy kind (`kind: Model`), stored under `{diskDirectory}/models/` — parallel to container images, not routed through the container engine image pull.

This page covers operator lifecycle: deploy, pull, catalog bind into a microservice, prune, on-disk layout, and revision pinning. Manifest schema: [manifest-reference.md](manifest-reference.md). Examples: [examples/model.yaml](examples/model.yaml), [examples/registry.yaml](examples/registry.yaml), [examples/microservice.yaml](examples/microservice.yaml).

---

## Quick start

```bash
# 1. Public Hub is built-in (id 3). Deploy a user registry only for
#    private tokens, enterprise Hub, or extra OCI hosts.
edgelet deploy -f examples/registry.yaml

# 2. Apply Model documents (persists desired state and downloads artifacts)
edgelet deploy -f examples/model.yaml

# 3. Inspect
edgelet model ls
edgelet model inspect llama-2-7b-q2k
```

`edgelet deploy -f model.yaml` persists the row and starts the download. The CLI waits until each model is **`Ready`** or **`Failed`**. `--dry-run` validates only. `edgelet model pull` upserts from flags (or retries an existing name) and also waits.

---

## Registry types

A Model always references a **registry row id** (`spec.registry`). Registry `type` selects the pull adapter:

| `type` | Use | Typical `url` |
|--------|-----|----------------|
| **`oci`** (default) | OCI registries and AI artifacts (Docker model-spec, ModelPack, ModelKit, ORAS) | `docker.io`, `quay.io`, host:port |
| **`hf`** | Hugging Face Hub or a self-hosted Hub | `https://huggingface.co` or enterprise base URL |

- **`edgelet image pull`** and microservice image pull accept **`type: oci` only**. An `hf` registry id is rejected.
- Private **oci**: `username` + `password` required.
- Private **hf**: `password` is the Hub token; `username` is optional.
- **`spec.ca`**: optional base64 PEM CA, used for both types. Extra trust only — it does not replace the system CA pool.
- **`spec.insecure`**: default `false`. When `true`, allow `http://` and skip TLS certificate verification for `https://`.
- Container **image** pull honors `ca` / `insecure` when `containerEngine` is **edgelet**. Docker and Podman image pull use daemon credentials only and do not apply those fields.

Built-in rows (cannot be edited or removed): **id 1** `docker.io` (`oci`), **id 2** `from_cache` (`oci`, not for remote pulls), **id 3** `https://huggingface.co` (`hf`). Add a user `hf` row (id 4+) for a private Hub token or enterprise host. See [manifest-reference.md](manifest-reference.md#registry).

---

## Deploy and reconcile

```bash
edgelet deploy -f examples/model.yaml
edgelet deploy -f examples/model.yaml --dry-run
```

| Rule | Behavior |
|------|----------|
| Identity | Upsert by `metadata.name` (DNS-1123 label) |
| Spec change | Bumps generation and **automatically re-pulls** on reconcile |
| Multi-doc YAML | All `Model` documents are applied; **fail fast** on the first error; then each name is pulled (global cap 2 concurrent) |
| Deploy | Persist, then start downloads. The CLI waits for **Ready** / **Failed**. `--dry-run` does not persist or pull |
| Pull | **Asynchronous** on the daemon (mirrors `edgelet image pull`); CLI polls until finished |

Delete:

```bash
edgelet model rm llama-2-7b-q2k
```

Remove is refused while a pull for that name is in progress, and while any microservice still lists that name in `spec.models.items`.

---

## Pull

```bash
# Retry an already-deployed name
edgelet model pull llama-2-7b-q2k

# YAML-as-flags: upsert the same row, then pull
edgelet model pull tiny-gpt2 \
  --repo hf-internal-testing/tiny-random-gpt2 \
  --revision 71034c5d8bde858ff824298bdedc65515b97d2b9 \
  --registry 3 \
  --files config.json \
  --format unknown

edgelet model inspect llama-2-7b-q2k
```

| Limit | Value |
|-------|--------|
| Concurrent pulls per `metadata.name` | 1 |
| Global concurrent pulls | 2 |
| Default pull timeout | 6 hours |
| Disk | Pre-check against available disk / configured threshold before download |
| Resume | Partial downloads resume; files are atomically renamed into place |

Progress (bytes and percent) is available on the async pull status API: `POST /v1/models:pull` then `GET /v1/models:pull/{operationId}`.

### Lifecycle states

`Pending` → `Pulling` → **`Ready`** or **`Failed`**.

On **`Ready`**, Edgelet always materializes **`content/`** under the model directory (OCI and HF). Inspect shows `source` (`local` \| `managed`), `state`, `resolvedRevision`, `digest`, `revisionFloating`, and `totalBytes`. Managed inspect also includes controller `uuid` and `bindRefCount`.

---

## Bind into a microservice

Ready artifacts are files on disk. A microservice catalog bind makes them visible inside the container.

```yaml
spec:
  models:
    bindPath: /models
    permissions: ro          # default; rw is an explicit opt-in
    items:
      - name: test-model     # Model metadata.name only — never a host path
      - name: qwen3-8-27b
```

| Rule | Behavior |
|------|----------|
| Container path | Always **`{bindPath}/{name}/`** = that model's Ready **`content/`**. One directory per item; the catalog is never flattened |
| Host source | `{diskDirectory}/models/{name}/content/`. Operators and the controller never send a host content path |
| Projection | One bind of a per-microservice directory at `bindPath` with catalog `permissions`. Item add/remove/re-pull updates files in place |
| Permissions | Catalog-level `ro` (default) or `rw`. No per-item mode |
| Identity | Bind YAML/JSON uses **name** only (DNS-1123). No uuid in `items[]` |
| Source scope | Local microservices bind **local** models only. Controller-managed microservices bind **managed** models only |
| Name ownership | While the node is provisioned, a managed model **wins** that name (pull spec + on-disk tree). Local `kind: Model` apply for a managed name is rejected |
| Collisions | Duplicate `items[].name`, or a catalog path that matches a volume `containerDestination` or `tmpfs.containerPath`, is a validate error |
| Env | No model environment variables are injected |

`bindPath` is required when `items` is non-empty and must be an absolute container path. Manifest schema: [manifest-reference.md](manifest-reference.md#specmodels-catalog-bind).

### Start gate

The container is created only when **every** named item is **Ready**.

| Item state | Local apply | Runtime |
|------------|-------------|---------|
| Ready | Allowed | Start / stay running |
| Pending or Pulling | Persist; microservice **QUEUED** | Wait text names the model and state (`waiting for model download: test-model (Pulling)`) |
| Unknown or Failed | **Validate error** (Failed includes the model `lastError` when present) | **FAILED** with the same text |

`edgelet ms inspect` prints the full inspect JSON by default, including catalog `models` (`bindPath`, `permissions`, item names) and `raw.engineInspect`. `--summary` is the short card. `statusText` is set when the start gate is waiting or failed. Crash fields (`errorMessage`, `lastError`, `lastErrorAt`, `restartCount`) use the same rules as fog `microserviceStatus`.

### In-place updates vs recreate

| Change | Container |
|--------|-----------|
| Add or remove a catalog item (catalog already non-empty; same `bindPath` + permissions) | In-place projection — **no** recreate |
| Newly added item not Ready | Keep the **running** container and the **old** projection until Ready, then atomic swing |
| Model re-pull (new `content/`) | In-place — **no** recreate |
| Empty catalog → first items, or last item removed | **Recreate** |
| `bindPath` or catalog `permissions` | **Recreate** |
| Image, env, ports, or other container spec drift | **Recreate** (same as today) |

### Prune and remove while bound

`model_refs` records every catalog name a microservice uses. `edgelet model rm` / `DELETE /v1/models/{name}` is refused while any microservice still references the name.

Dangling prune **keeps**:

- Every **managed** fleet model name (even unbound)
- Every `model_refs` name
- Active pulls

Unbound **local** Model rows and on-disk trees are **deleted**. A bound local artifact is kept.

Local `kind: Model` apply for a name that is already **managed** (provisioned fleet model) is rejected.

---

## Revision pinning and floating refs

`spec.revision` is a single pin field. The adapter interprets it from the registry type:

| Registry | Empty `revision` | Digest / commit | Anything else |
|----------|------------------|-----------------|---------------|
| **oci** | tag **`latest`** (floating) | `sha256:` + 64 hex → `repo@sha256:…` | tag → `repo:revision` |
| **hf** | branch **`main`** (floating) | 40-char hex → Hub commit | branch or tag name |

**Floating** revisions (`latest`, `main`, branches, non-digest tags) are allowed. After a successful pull:

- Status sets **`revisionFloating: true`** and the daemon logs a warning.
- `{metadata.name}/manifest.json` records **`resolvedRevision`** (HF commit) and **`digest`** (OCI manifest digest).

Prefer a commit SHA (HF) or `sha256:…` digest (OCI) on production nodes so a later reconcile does not silently pick up a new artifact.

Changing `spec.revision` (or other identity fields) bumps generation and triggers a re-pull.

---

## Hugging Face `spec.files`

`spec.files` applies to **`type: hf` only**. OCI pulls always extract the **full** artifact; the list is ignored.

| `files` | Behavior |
|---------|----------|
| omitted or `[]` | Hub snapshot at the pinned revision (config, tokenizer, index JSON, and all shards the index references) |
| exact names | Only those repo-relative paths |
| globs | `*`, `**`, `?` on repo-relative paths |

A Hub repo that contains **more than one `*.gguf`** must list the files (or a glob that selects them). An empty `files` list on a multi-GGUF repo fails validation (or pull, if Hub metadata is not available at validate time).

---

## OCI artifacts

Format detection order:

1. Docker model-spec
2. CNCF ModelPack
3. KitOps ModelKit
4. Generic ORAS fallback

Tag and digest pulls of the same blob share storage in `oci-store/`. Model pull never uses the container-engine image pull path.

---

## On-disk layout

Base: **`{diskDirectory}/models/`** (default `/var/lib/edgelet/models/`).

```
{diskDirectory}/models/
  oci-store/                          # shared OCI blob store
    layout.json
    models.json
    blobs/sha256/<hex>
    manifests/sha256/<hex>
  {metadata.name}/
    manifest.json                     # resolved state after pull
    content/                          # materialized files (always present on Ready)
```

Include this tree in node backups together with `edgelet.db` — see [persistence.md](persistence.md).

### `manifest.json` (after a successful pull)

```json
{
  "metadataName": "llama-2-7b-q2k",
  "registryId": 5,
  "registryType": "hf",
  "repo": "second-state/Llama-2-7B-Chat-GGUF",
  "requestedRevision": "064fe43ea8c1e1f93477ef4a170bdc2b244ef02c",
  "resolvedRevision": "064fe43ea8c1e1f93477ef4a170bdc2b244ef02c",
  "digest": "sha256:…",
  "format": "gguf",
  "files": ["llama-2-7b-chat.Q5_K_M.gguf"],
  "contentPaths": ["content/llama-2-7b-chat.Q5_K_M.gguf"],
  "totalBytes": 2840000000,
  "revisionFloating": false,
  "revisionKind": "commit",
  "pulledAt": "2026-09-04T12:00:00Z"
}
```

---

## Prune

```bash
edgelet model prune
edgelet model prune dangling
edgelet model prune --mode dangling
```

The only mode is **`dangling`**: remove unused **local** model rows and trees, then drop unreferenced OCI blobs. Keep set: managed fleet names (even unbound), catalog binds (`model_refs`), and active pulls. Shared blobs stay if another model still needs them.

Scheduled image prune (`pruningFrequency`) also runs dangling model prune on the same tick. Controller `getChanges.prune` runs dangling **images** and unused local models together.

---

## Watchdog

When `watchdogEnabled` is on, Edgelet treats local models like local workloads: **out of scope**.

| Action | Behavior |
|--------|----------|
| Existing local models | Rows and on-disk trees are deleted |
| `edgelet deploy -f model.yaml` / local Model apply | **Refused** (`local models are disabled while watchdog is enabled`) |
| Managed fleet models | Unchanged |

Disable watchdog to deploy local `kind: Model` documents again.

Controller need not send local Model CRUD while watchdog is on. See [CONTROLLER-HANDOFF-MODELS.md](CONTROLLER-HANDOFF-MODELS.md).

---

## CLI and API

| Action | CLI | EdgeletAPI |
|--------|-----|------------|
| Validate / apply | `edgelet deploy -f model.yaml` | `POST /v1/deploy/models:validate`, `:apply` |
| List | `edgelet model ls` | `GET /v1/models` |
| Inspect | `edgelet model inspect <name>` | `GET /v1/models/{name}` |
| Pull | `edgelet model pull <name> [--repo … --registry …]` | `POST /v1/models:pull` |
| Pull status | (CLI waits / prints) | `GET /v1/models:pull/{operationId}` |
| Prune | `edgelet model prune` | `POST /v1/models:prune?mode=dangling` |
| Remove | `edgelet model rm <name>` | `DELETE /v1/models/{name}` |

RBAC resources: `models`, `models/pull`, `models/prune`, `deploy/models`. See [edgelet-api-v1-rbac-resources.md](edgelet-api-v1-rbac-resources.md).

Generated CLI pages: [../cli/generated/](../cli/generated/) (`edgelet_model*.md`).

---

## Related docs

| Document | Topic |
|----------|--------|
| [manifest-reference.md](manifest-reference.md) | Registry + Model + Microservice catalog YAML |
| [examples/model.yaml](examples/model.yaml) | HF GGUF + OCI tag/digest samples |
| [examples/microservice.yaml](examples/microservice.yaml) | Catalog bind + container fields |
| [persistence.md](persistence.md) | Schema v2 and `{diskDirectory}/models/` backup |
| [edgelet-api-v1.md](edgelet-api-v1.md) | HTTP contract |
| [CONTROLLER-HANDOFF-MODELS.md](CONTROLLER-HANDOFF-MODELS.md) | Controller contract: HAL drop, RuntimeClass, status, catalog, TLS, prune |
