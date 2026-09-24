# Knowledge artifacts

Edgelet can pull, store, reconcile, and prune **curated retrieval artifacts** on the node — documents, chunks, datasets, and prebuilt vector indexes. Knowledge is a first-class deploy kind (`kind: Knowledge`), stored under `{diskDirectory}/knowledge/` — parallel to Models and container images, not routed through the container engine image pull.

Edgelet does **not** chunk, embed, index, or query. A microservice reads `{bindPath}/{name}/` after a catalog bind.

This page covers operator lifecycle: deploy, pull, catalog bind into a microservice, prune, on-disk layout, revision pinning, and how Knowledge differs from Model (weights) and `VOLUME` (mutable runtime data). Manifest schema: [manifest-reference.md](manifest-reference.md). Examples: [examples/knowledge.yaml](examples/knowledge.yaml), [examples/registry.yaml](examples/registry.yaml), [examples/microservice.yaml](examples/microservice.yaml).

Knowledge **names** are a separate namespace from Model names. A Model `foo` and a Knowledge `foo` may exist on the same node.

---

## Quick start

```bash
# 1. Public Hub is built-in (id 3). Deploy a user registry only for
#    private tokens, enterprise Hub, or extra OCI hosts.
edgelet deploy -f examples/registry.yaml

# 2. Apply Knowledge documents (persists desired state and downloads artifacts)
edgelet deploy -f examples/knowledge.yaml

# 3. Inspect
edgelet knowledge ls
edgelet knowledge inspect product-docs
```

`edgelet deploy -f knowledge.yaml` persists the row and starts the download. The CLI waits until each Knowledge is **`Ready`** or **`Failed`**. `--dry-run` validates only. `edgelet knowledge pull` upserts from flags (or retries an existing name) and also waits.

---

## Registry types

A Knowledge always references a **registry row id** (`spec.registry`). Registry `type` selects the pull adapter — the same registry rows Models use:

| `type` | Use | Typical `url` |
|--------|-----|----------------|
| **`oci`** (default) | Generic OCI / ORAS artifacts | `docker.io`, `quay.io`, host:port |
| **`hf`** | Hugging Face Hub or a self-hosted Hub | `https://huggingface.co` or enterprise base URL |

- Hugging Face **Knowledge** always uses the Hub **dataset** API (`/api/datasets/…`). Hugging Face **Model** still uses the Hub **model** API (`/api/models/…`). There is no `spec.repoType`.
- Knowledge + **`type: oci`** extracts a generic ORAS artifact. Edgelet does **not** run Docker model-spec, ModelPack, or ModelKit detectors on Knowledge.
- Knowledge pull never uses the container-engine image pull path.
- **`edgelet image pull`** and microservice image pull still accept **`type: oci` only**. An `hf` registry id is rejected.
- Private **oci**: `username` + `password` required.
- Private **hf**: `password` is the Hub token; `username` is optional.
- **`spec.ca`** / **`spec.insecure`**: same extra-trust and TLS skip rules as Model. See [models.md](models.md#registry-types).

Built-in rows (cannot be edited or removed): **id 1** `docker.io` (`oci`), **id 2** `from_cache` (`oci`, not for remote pulls), **id 3** `https://huggingface.co` (`hf`). See [manifest-reference.md](manifest-reference.md#registry).

---

## Deploy and reconcile

```bash
edgelet deploy -f examples/knowledge.yaml
edgelet deploy -f examples/knowledge.yaml --dry-run
```

| Rule | Behavior |
|------|----------|
| Identity | Upsert by `metadata.name` (DNS-1123 label) |
| Spec change | Bumps generation and **automatically re-pulls** on reconcile |
| Multi-doc YAML | All `Knowledge` documents are applied; **fail fast** on the first error; then each name is pulled (global Knowledge cap 2 concurrent — **not** shared with the Model pool) |
| Deploy | Persist, then start downloads. The CLI waits for **Ready** / **Failed**. `--dry-run` does not persist or pull |
| Pull | **Asynchronous** on the daemon (mirrors `edgelet model pull`); CLI polls until finished |

Delete:

```bash
edgelet knowledge rm product-docs
```

Remove is refused while a pull for that name is in progress, and while any microservice still lists that name in `spec.knowledge.items`.

---

## Pull

```bash
# Retry an already-deployed name
edgelet knowledge pull product-docs

# YAML-as-flags: upsert the same row, then pull
edgelet knowledge pull wiki-faiss \
  --repo acme/wiki \
  --revision 9f3c111122223333444455556666777788889999 \
  --registry 3 \
  --files 'data/**/*.jsonl' \
  --format jsonl

edgelet knowledge inspect product-docs
```

| Limit | Value |
|-------|--------|
| Concurrent pulls per `metadata.name` | 1 |
| Global concurrent Knowledge pulls | 2 (separate from the Model pool) |
| Default pull timeout | 6 hours |
| Disk | Pre-check against available disk / configured threshold before download |
| Resume | Partial downloads resume; files are atomically renamed into place |

Progress (bytes and percent) is available on the async pull status API: `POST /v1/knowledge:pull` then `GET /v1/knowledge:pull/{operationId}`.

### Lifecycle states

`Pending` → `Pulling` → **`Ready`** or **`Failed`**.

On **`Ready`**, Edgelet always materializes **`content/`** under the Knowledge directory (OCI and HF). Inspect shows `source` (`local` \| `managed`), `state`, `resolvedRevision`, `digest`, `revisionFloating`, `totalBytes`, and `format`. Managed inspect also includes controller `uuid` and `bindRefCount`.

---

## Bind into a microservice

Ready artifacts are files on disk. A microservice catalog bind makes them visible inside the container.

```yaml
spec:
  models:
    bindPath: /models
    permissions: ro
    items:
      - name: llama-2-7b-q2k
  knowledge:
    bindPath: /knowledge
    permissions: ro          # default; rw is an explicit opt-in
    items:
      - name: product-docs   # Knowledge metadata.name only — never a host path
      - name: wiki-faiss
```

The container sees `/models/llama-2-7b-q2k/` and `/knowledge/product-docs/`, `/knowledge/wiki-faiss/`. A microservice **may** bind both catalogs.

| Rule | Behavior |
|------|----------|
| Container path | Always **`{bindPath}/{name}/`** = that item’s Ready **`content/`**. One directory per item; the catalog is never flattened |
| Host source | `{diskDirectory}/knowledge/{name}/content/`. Operators and the controller never send a host content path |
| Projection | One bind of a per-microservice directory at `bindPath` with catalog `permissions`. Item add/remove/re-pull updates files in place |
| Permissions | Catalog-level `ro` (default) or `rw`. No per-item mode |
| Identity | Bind YAML/JSON uses **name** only (DNS-1123). No uuid in `items[]` |
| Source scope | Local microservices bind **local** Knowledge only. Controller-managed microservices bind **managed** Knowledge only |
| Name ownership | While the node is provisioned, a managed Knowledge **wins** that name (pull spec + on-disk tree). Local `kind: Knowledge` apply for a managed name is rejected |
| Collisions | Duplicate `items[].name`, or a catalog path that matches a volume `containerDestination`, `tmpfs.containerPath`, or `spec.models.bindPath` / `{models.bindPath}/{modelName}`, is a validate error |
| Env | No Knowledge environment variables are injected |
| File subset | No `content[]` on the microservice. File selection stays on `kind: Knowledge` `spec.files` |

`bindPath` is required when `items` is non-empty and must be an absolute container path. Manifest schema: [manifest-reference.md](manifest-reference.md#specknowledge-catalog-bind).

### Start gate

The container is created only when **every** `spec.models` item **and** every `spec.knowledge` item is **Ready**.

| Item state | Local apply | Runtime |
|------------|-------------|---------|
| Ready | Allowed | Start / stay running |
| Pending or Pulling | Persist; microservice **QUEUED** | Wait text names the Knowledge and state (`waiting for knowledge download: product-docs (Pulling)`) |
| Unknown or Failed | **Validate error** (Failed includes the Knowledge `lastError` when present) | **FAILED** with the same text |

`edgelet ms inspect` prints the full inspect JSON by default, including catalog `models` and `knowledge` (`bindPath`, `permissions`, item names) and `raw.engineInspect`. `--summary` is the short card. `statusText` is set when the start gate is waiting or failed.

### In-place updates vs recreate

| Change | Container |
|--------|-----------|
| Add or remove a catalog item (catalog already non-empty; same `bindPath` + permissions) | In-place projection — **no** recreate |
| Newly added item not Ready | Keep the **running** container and the **old** projection until Ready, then atomic swing |
| Knowledge re-pull (new `content/`) | In-place — **no** recreate |
| Empty catalog → first items, or last item removed | **Recreate** |
| `bindPath` or catalog `permissions` | **Recreate** |
| Image, env, ports, or other container spec drift | **Recreate** (same as today) |

### Prune and remove while bound

`knowledge_refs` records every catalog name a microservice uses. `edgelet knowledge rm` / `DELETE /v1/knowledge/{name}` is refused while any microservice still references the name.

Dangling prune **keeps**:

- Every **managed** fleet Knowledge name (even unbound)
- Every `knowledge_refs` name
- Active pulls

Unbound **local** Knowledge rows and on-disk trees are **deleted**. A bound local artifact is kept.

Local `kind: Knowledge` apply for a name that is already **managed** (provisioned fleet Knowledge) is rejected.

---

## Revision pinning and floating refs

`spec.revision` is a single pin field. The adapter interprets it from the registry type — same rules as Model:

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
| omitted or `[]` | Hub **dataset** snapshot at the pinned revision (full tree) |
| exact names | Only those repo-relative paths |
| globs | `*`, `**`, `?` on repo-relative paths |

An empty `files` list on a **huge** dataset is allowed (operator choice). Disk pre-check still applies before download — pin `files` or a glob when you do not need the whole tree.

This is **not** a Model pull. Model + `type: hf` still uses `/api/models/…` and Model `spec.files` rules (including multi-GGUF). Knowledge + `type: hf` always uses `/api/datasets/…`.

---

## OCI artifacts

Knowledge + `type: oci` is **generic ORAS extract only**. Edgelet does not run Docker model-spec, ModelPack, or ModelKit detectors on Knowledge.

Tag and digest pulls of the same blob share storage in `knowledge/oci-store/`. That store is **not** the Model DMR store (`models/oci-store/`) and is never written by Knowledge pull.

How to **package and push** a corpus or index as ORAS: [oci-artifacts.md](oci-artifacts.md).

---

## On-disk layout

Base: **`{diskDirectory}/knowledge/`** (default `/var/lib/edgelet/knowledge/`).

```
{diskDirectory}/knowledge/
  oci-store/                          # Knowledge OCI blobs only — not DMR
    layout.json
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
  "metadataName": "product-docs",
  "registryId": 3,
  "registryType": "hf",
  "repo": "acme/product-manuals",
  "requestedRevision": "9f3c111122223333444455556666777788889999",
  "resolvedRevision": "9f3c111122223333444455556666777788889999",
  "digest": "",
  "format": "jsonl",
  "files": ["data/guide.jsonl", "index/faiss.index"],
  "contentPaths": ["content/data/guide.jsonl", "content/index/faiss.index"],
  "totalBytes": 128000000,
  "revisionFloating": false,
  "revisionKind": "commit",
  "pulledAt": "2026-09-20T12:00:00Z"
}
```

---

## Prune

```bash
edgelet knowledge prune
edgelet knowledge prune dangling
edgelet knowledge prune --mode dangling
```

The only mode is **`dangling`**: remove unused **local** Knowledge rows and trees, then drop unreferenced blobs in `knowledge/oci-store/`. Keep set: managed fleet names (even unbound), catalog binds (`knowledge_refs`), and active pulls. Shared blobs stay if another Knowledge still needs them.

Scheduled image prune (`pruningFrequency`) and disk-threshold ticks also run dangling Knowledge prune on the same tick as unused local models. Controller `getChanges.prune` runs dangling **images**, unused local models, and unused local Knowledge together. It does **not** prune persistent volumes.

`edgelet system prune` does **not** prune Knowledge (same as models).

---

## Watchdog

When `watchdogEnabled` is on, Edgelet treats local Knowledge like local workloads: **out of scope**.

| Action | Behavior |
|--------|----------|
| Existing local Knowledge | Rows and on-disk trees are deleted |
| `edgelet deploy -f knowledge.yaml` / local Knowledge apply | **Refused** (`local knowledge is disabled while watchdog is enabled`) |
| Managed fleet Knowledge | Unchanged |

Disable watchdog to deploy local `kind: Knowledge` documents again.

Controller need not send local Knowledge CRUD while watchdog is on. See [CONTROLLER-HANDOFF-KNOWLEDGE.md](CONTROLLER-HANDOFF-KNOWLEDGE.md).

---

## CLI and API

| Action | CLI | EdgeletAPI |
|--------|-----|------------|
| Validate / apply | `edgelet deploy -f knowledge.yaml` | `POST /v1/deploy/knowledge:validate`, `:apply` |
| List | `edgelet knowledge ls` | `GET /v1/knowledge` |
| Inspect | `edgelet knowledge inspect <name>` | `GET /v1/knowledge/{name}` |
| Pull | `edgelet knowledge pull <name> [--repo … --registry …]` | `POST /v1/knowledge:pull` |
| Pull status | (CLI waits / prints) | `GET /v1/knowledge:pull/{operationId}` |
| Prune | `edgelet knowledge prune` | `POST /v1/knowledge:prune?mode=dangling` |
| Remove | `edgelet knowledge rm <name>` | `DELETE /v1/knowledge/{name}` |

Routes use the mass noun **`knowledge`** — **`/v1/knowledges` is not registered**.

RBAC resources: `knowledge`, `knowledge/pull`, `knowledge/prune`, `deploy/knowledge`. See [edgelet-api-v1-rbac-resources.md](edgelet-api-v1-rbac-resources.md).

Generated CLI pages: [../cli/generated/](../cli/generated/) (`edgelet_knowledge*.md`).

---

## Related docs

| Document | Topic |
|----------|--------|
| [manifest-reference.md](manifest-reference.md) | Registry + Knowledge + Microservice catalog YAML |
| [oci-artifacts.md](oci-artifacts.md) | Publish Model or Knowledge as an OCI / ORAS artifact |
| [examples/knowledge.yaml](examples/knowledge.yaml) | HF dataset + OCI tag/digest samples |
| [examples/microservice.yaml](examples/microservice.yaml) | Catalog bind + container fields |
| [models.md](models.md) | Model (weights) lifecycle — separate kind and store |
| [persistence.md](persistence.md) | Schema v4 and `{diskDirectory}/knowledge/` backup |
| [edgelet-api-v1.md](edgelet-api-v1.md) | HTTP contract |
| [CONTROLLER-HANDOFF-KNOWLEDGE.md](CONTROLLER-HANDOFF-KNOWLEDGE.md) | Controller contract: `GET knowledge`, catalog flag, status, prune |
