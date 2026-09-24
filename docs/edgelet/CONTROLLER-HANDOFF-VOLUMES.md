# Controller handoff — microservice `VOLUME` scope

This page is for the **Pot / Datasance Controller** team. It is the agent-side contract for persistent **`VOLUME`** mappings: default **private** (per microservice UUID) and opt-in **shared** (node-global name).

Existing Pot path prefixes stay **`/api/v3/…`**. JSON is **additive**. No new `getChanges` flag. `deleteWithCleanup` is unused; do not wait on it for reclaim.

Operator YAML: [volumes.md](volumes.md) · [manifest-reference.md](manifest-reference.md). Local API stays **`/v1/…`**.

The agent consumes the shape below on **controller-managed** microservices (`GET microservices` → `volumeMappings[]`) and on **local** deploy YAML. Both can consume the same shared name on one node.

---

## Additive field

On each `volumeMappings[]` object:

| Field | Type | Required | Default |
|-------|------|----------|---------|
| `scope` | string | no | **`private`** |

Allowed values: **`private`** \| **`shared`**. Case is not significant; the agent stores lowercase.

`scope` is meaningful **only** when `type` is **`volume`**. For `bind` / `volumeMount` (and omitted type treated as bind), the agent **ignores** `scope` and treats the mapping as **private**. Do not send `scope: shared` expecting a host `BIND` path to become an Edgelet-managed claim.

Omit, empty, or unknown `scope` → **`private`**. The agent **must not** fail microservice apply. Unknown values may be warn-logged. Controller UI/API should still validate so operators see typos.

Older agents that do not know `scope` ignore the key. Every `VOLUME` is then private (per UUID). Two microservices that both send `scope: shared` get **two** UUID directories until both run an agent that understands `scope`. There is **no** automatic merge of those directories into a shared claim after upgrade. private → shared does **not** copy files.

---

## Identity and on-disk paths

| `type` + `scope` | Host path the agent bind-mounts |
|------------------|----------------------------------|
| `volume` + `private` (default) | `{diskDirectory}/volumes/data/{microservice-uuid}/{hostDestination}/` |
| `volume` + `shared` | `{diskDirectory}/volumes/shared/{hostDestination}/` |
| `bind` | Operator `hostDestination` (absolute). Never deleted |
| `volumeMount` | Projected secret/configmap staging (unchanged) |

`hostDestination` for `volume` is a **name** (same charset as today), not a host path.

**Shared names are node-global.** Two microservices that both use `hostDestination: shared-config` and `scope: shared` mount the **same** directory — including one **local** workload and one **controller** workload. Use unique names (`nodered-config`), not generic `config`.

Private `config` and shared `config` are **different** disks.

On-node ControlPlane db/log volumes stay **private** (per controller UUID). Do not send `scope: shared` for those mappings.

Docker and Podman use the same bind paths. New private mappings do **not** create a node-global Docker volume named only `hostDestination`. Existing Docker volumes of that name are **not** copied automatically.

---

## Reclaim (agent; not a controller API)

The agent does **not** delete `VOLUME` data when a container is missing, on OTA, on `pruningFrequency`, or on `delete-node`.

- Removing one microservice that used a **shared** name does **not** delete `{diskDirectory}/volumes/shared/{name}/` while another consumer (local or controller) is still desired.
- Operators reclaim with local `edgelet volume rm` / `edgelet volume rm --shared <name>` / `edgelet volume prune`, or `edgelet deprovision --purge-volumes`.
- `delete-node` **preserves** `volumes/data/` and `volumes/shared/`.
- `--purge-volumes` never removes ControlPlane volumes. A **local-only** deprovision with purge does **not** drop a shared name still consumed by a remaining controller microservice.
- `deleteWithCleanup` is unused. Absence of the flag is the fleet default.

`getChanges.prune` stays dangling **images** and unused **local models**. It does not prune volumes.

On-node reclaim (not a Pot route):

| Method | Path | Meaning |
|--------|------|---------|
| GET | `/v1/volumes` | List claims. Shared rows have `uuid` null and `consumers` (local and/or controller UUIDs) |
| GET | `/v1/volumes/shared/{name}` | Inspect one shared claim |
| POST | `/v1/volumes:prune` | Dry-run by default. Destroy requires `yes: true`. 24h after last consumer unless `force`. Control-plane volumes skipped |
| DELETE | `/v1/volumes/{uuid}` | Destroy **private** data for that microservice UUID |
| DELETE | `/v1/volumes/shared/{name}` | Destroy a **shared** claim by name. Remaining consumers → 409 |

Mounted paths return **409** even with `force`. Missing shared name returns **404**. Admin RBAC: resource `volumes` (`get` / `delete`), `volumes/prune` (`create`). Group **`edgelet.iofog.org/v1`**.

Do not add a controller reclaim flag this release. Operators use `edgelet volume` / the local API above.

---

## `runAsUser`

For a **new** shared directory, the agent may chown to the first consumer’s `runAsUser`. If the directory **already exists**, the agent does **not** chown again. Every consumer of a shared name should use the **same** `runAsUser` (or an entrypoint that can write). Mixed uids are not supported as a product.

---

## Example (controller microservice JSON fragment)

```json
{
  "volumeMappings": [
    {
      "hostDestination": "data",
      "containerDestination": "/var/lib/app",
      "accessMode": "rw",
      "type": "volume"
    },
    {
      "hostDestination": "nodered-config",
      "containerDestination": "/data",
      "accessMode": "rw",
      "type": "volume",
      "scope": "shared"
    }
  ]
}
```

The first mapping is private (omitted `scope`). The second is shared across any other microservice on this node that uses the same name and `scope: shared`.

Local deploy YAML uses the same fields:

```yaml
volumes:
  - hostDestination: nodered-config
    containerDestination: /data
    accessMode: rw
    type: volume
    scope: shared
```
