# Controller handoff — Knowledge artifacts and catalog

This document is for the **Pot / Datasance Controller** team. It is the agent-side contract for fleet **Knowledge** artifacts: curated files a microservice retrieves or uses (documents, chunks, datasets, prebuilt vector indexes).

It is **not** the Model (weights) contract. Models stay on [CONTROLLER-HANDOFF-MODELS.md](CONTROLLER-HANDOFF-MODELS.md). Do not reuse `GET models` or `spec.models` for Knowledge.

Existing Pot path prefixes stay **`/api/v3/…`**. JSON is **additive**. Older agents ignore unknown keys. A missing Knowledge GET route is an empty list; the node continues.

Operator YAML: [knowledge.md](knowledge.md) · [manifest-reference.md](manifest-reference.md). Edgelet local API stays **`/v1/…`**.

The agent already consumes every shape below. Controller CRUD and UI can implement against this page without reading internal specs.

---

## Identity and paths

| Item | Contract |
|------|----------|
| Knowledge identity | **`uuid`** (required string) + unique **`name`** (DNS-1123). No integer `id` |
| On-disk name | One directory per `name`: `{diskDirectory}/knowledge/{name}/` |
| Bind items | **`name` only** — never send uuid or a host content path in `knowledge.items[]` |
| `GET knowledge` | `{controllerUrl}/agent/knowledge` → `{ "knowledge": [ … ] }` |
| Local API | `/v1/knowledge` (mass noun — **not** `/v1/knowledges`) |
| Older controllers | Missing additive flags or GET routes must not fail the node. Status still posts |

Knowledge **names** are a separate namespace from Model names. A Model `foo` and a Knowledge `foo` may exist on the same node.

While a node is provisioned, a **managed** Knowledge **wins** that `name`. Local apply of a managed name is rejected. Controller microservices bind **managed** Knowledge names only; local microservices bind **local** names only.

### `getChanges` flags the agent consumes (additive)

| Flag | On `true` (and on initialization, except prune) | GET |
|------|--------------------------------------------------|-----|
| `knowledge` | Replace-all controller Knowledge then upsert/pull | `GET /api/v3/agent/knowledge` → `{ "knowledge": [ … ] }` |
| `microserviceKnowledge` | Catalog-only refresh of the microservice list | **same** `GET microservices` (no dedicated GET) |
| `microserviceList` + `microserviceKnowledge` | One GET; do not skip in-place catalog refresh | once |
| `prune` | Dangling **images**, unused **local** models, and unused **local** Knowledge | none |

A missing `GET knowledge` route is an **empty list**. The agent continues.

Registries are unchanged: `type` `oci` \| `hf`, plus `ca` / `insecure`. Knowledge pull uses the same registry row. Hugging Face **Knowledge** uses the Hub **dataset** API. Hugging Face **Model** still uses the Hub **model** API. There is no `repoType` field on the Knowledge object.

---

## Knowledge object

```json
{
  "uuid": "3f2c1111-2222-3333-4444-555566667777",
  "name": "product-docs",
  "repo": "acme/product-manuals",
  "revision": "9f3c111122223333444455556666777788889999",
  "registryId": 3,
  "files": ["data/**/*.jsonl", "index/faiss.index"],
  "format": "jsonl"
}
```

| Field | Type | Notes |
|-------|------|--------|
| `uuid` | string | **Required.** Controller identity |
| `name` | string | **Required.** DNS-1123. Unique on the controller |
| `repo` | string | Hub dataset id or OCI repository path **without** host |
| `revision` | string | Optional pin. OCI empty → `latest`. HF empty → `main`. Prefer commit SHA or `sha256:…` |
| `registryId` | number | Registry row id. `hf` → dataset API; `oci` → ORAS artifact |
| `files` | string[] | HF only. Empty / omit → full dataset snapshot. Names or globs. **Ignored for OCI** |
| `format` | string | Optional hint: `markdown`, `pdf`, `jsonl`, `parquet`, `arrow`, `sqlite`, `faiss`, `chroma`, `lance`, `unknown` |

Rows missing `uuid` or `name` are skipped. Unknown extra keys are ignored.

### `GET /api/v3/agent/knowledge`

```json
{
  "knowledge": [
    {
      "uuid": "3f2c…",
      "name": "product-docs",
      "repo": "acme/product-manuals",
      "revision": "9f3c111122223333444455556666777788889999",
      "registryId": 3,
      "files": ["data/**/*.jsonl"],
      "format": "jsonl"
    }
  ]
}
```

Use the mass noun **`knowledge`** as the array key — not `knowledges`.

### `getChanges` `knowledge`

| Flag | Agent behavior |
|------|----------------|
| `knowledge: true` (or initialization) | `GET knowledge` → replace-all → upsert/pull |
| `knowledge: false` or omitted | No reload. Status still includes the additive fog keys below |

---

## Microservice catalog

Additive object on the existing microservice payload, next to `models`:

```json
"knowledge": {
  "bindPath": "/knowledge",
  "permissions": "ro",
  "items": [{ "name": "product-docs" }, { "name": "wiki-faiss" }]
}
```

| Field | Type | Notes |
|-------|------|--------|
| `bindPath` | string | Required when `items` is non-empty. Absolute **container** path |
| `permissions` | string | `ro` (default) or `rw`. Catalog-level only |
| `items[].name` | string | DNS-1123 Knowledge `name`. Duplicate names are a validate error |

Container path is always **`{bindPath}/{name}/`** = that item’s Ready files. One bind of a per-microservice projection. No per-item permissions. No host content path. No auto-injected environment variables.

`bindPath` must not collide with volumes, tmpfs, or the models catalog path (`models.bindPath` or `{models.bindPath}/{modelName}`).

A microservice **may** set both `models` and `knowledge`. The container starts only when **every** named model **and** every named Knowledge item is **Ready**. Pending/Pulling keeps the microservice **QUEUED** with wait text (`waiting for knowledge download: product-docs (Pulling)`). Unknown or Failed names on a controller MS mark the microservice **FAILED**.

Local MS → local Knowledge only. Controller MS → managed Knowledge only.

### In-place vs recreate

| Change | Container |
|--------|-----------|
| Add or remove a catalog **item** (catalog already non-empty; same `bindPath` + catalog `permissions`) | **In-place** projection — no recreate |
| Knowledge re-pull (new `content/`) | **In-place** |
| Newly added item **not Ready** | **Keep the running container and the old projection** until Ready, then atomic swing |
| No catalog → first items (empty → non-empty) | **Recreate** |
| Last item removed (non-empty → empty) | **Recreate** |
| `bindPath` or catalog `permissions` | **Recreate** |
| Image, env, ports, `rebuild`, or other spec drift | **Recreate** |

### Catalog-only REST (`microserviceKnowledge`)

When the operator only changes Knowledge catalog items (add / remove / re-pull names) and **not** the rest of the microservice spec:

- Set **`getChanges.microserviceKnowledge: true`**
- Do **not** set `microserviceList` unless the rest of the spec also changed
- Agent `GET microservices` once and refreshes the catalog in place

If `microserviceList` and `microserviceKnowledge` are both true: **one GET**.

---

## Fog `PUT status` (additive)

| Key | Type | Notes |
|-----|------|--------|
| `knowledgeStatus` | string | JSON **string** (not a raw array) of status items |
| `activeKnowledge` | number | Count of **managed** Knowledge only |
| `knowledgeLastUpdate` | number | Unix milliseconds; same clock as `modelLastUpdate` |

When the controller has no Knowledge (or no `knowledge` flag), send `knowledgeStatus: "[]"`, `activeKnowledge: 0`, `knowledgeLastUpdate: 0`. **Never fail** `PUT status` because these keys are unknown on an older console.

`knowledgeStatus` item (after `JSON.parse`):

```json
{
  "uuid": "3f2c…",
  "name": "product-docs",
  "state": "Ready",
  "digest": "sha256:…",
  "resolvedRevision": "9f3c…",
  "revisionFloating": false,
  "totalBytes": 128000000,
  "lastError": "",
  "source": "managed"
}
```

`source` is `local` or `managed`. Local items **omit** `uuid`. Dashboards: parse `source`; do not treat `activeKnowledge` as “how many rows are in `knowledgeStatus`”.

Local `GET /v1/system/status` uses the same keys.

---

## Prune and watchdog

`getChanges.prune: true` runs dangling **images**, unused **local** models, and unused **local** Knowledge. It does **not** prune persistent volumes.

Keep set for Knowledge prune:

- Every managed fleet name (even unbound)
- Every microservice catalog bind (`knowledge_refs`)
- Active pulls

`edgelet system prune` does **not** prune Knowledge (same as models). Scheduled `pruningFrequency` and disk-threshold ticks run dangling Knowledge prune on the same tick as unused local models.

When `watchdogEnabled` is on:

| Action | Behavior |
|--------|----------|
| Existing local Knowledge | Rows and on-disk trees are deleted |
| Local `kind: Knowledge` apply | **Refused** |
| Managed fleet Knowledge | Unchanged |

UI: hide or disable local Knowledge deploy on a watchdog node. Fleet Knowledge CRUD is still valid.

---

## Local operator API (not Pot routes)

On the node (admin token, group `edgelet.iofog.org/v1`):

| Method | Path |
|--------|------|
| POST | `/v1/deploy/knowledge:validate` · `:apply` |
| GET | `/v1/knowledge` · `/v1/knowledge/{name}` |
| POST | `/v1/knowledge:pull` |
| GET | `/v1/knowledge:pull/{operationId}` |
| POST | `/v1/knowledge:prune` |
| DELETE | `/v1/knowledge/{name}` |

RBAC: `knowledge`, `knowledge/pull`, `knowledge/prune`, `deploy/knowledge`.

Inspect shows `source`, `state`, `resolvedRevision`, `digest`, `revisionFloating`, `totalBytes`, `format`. Managed inspect also includes `uuid` and `bindRefCount`.

CLI: `edgelet knowledge ls|inspect|pull|rm|prune`.

---

## Controller checklist

- [ ] Knowledge CRUD: `uuid` + unique `name`; no integer id
- [ ] `getChanges` flag `knowledge` and `GET /api/v3/agent/knowledge` → `{ "knowledge": [ … ] }`
- [ ] HF Knowledge published against a `type: hf` registry (dataset repos)
- [ ] Microservice object `knowledge` catalog (`bindPath`, `permissions`, `items[].name`)
- [ ] Catalog-only REST: set **only** `microserviceKnowledge` when the catalog changed
- [ ] One GET when `microserviceList` and `microserviceKnowledge` are both true
- [ ] Dashboards: parse fog `knowledgeStatus` with `source` (local omits `uuid`); `activeKnowledge` is managed count only
- [ ] Watchdog nodes: hide local Knowledge deploy
- [ ] `prune` still does not delete volumes; expect unused local Knowledge on the same flag
- [ ] `system prune` does not drop Knowledge trees

---

## Related

- [knowledge.md](knowledge.md) — operator lifecycle
- [CONTROLLER-HANDOFF-MODELS.md](CONTROLLER-HANDOFF-MODELS.md) — weights, registries, RuntimeClass
- [CONTROLLER-HANDOFF-VOLUMES.md](CONTROLLER-HANDOFF-VOLUMES.md) — persistent `VOLUME` scope
