# Workload volumes and lifecycle

How Edgelet stores microservice volume data on disk, what is retained when a workload is deleted, and how operators reclaim space.

Applies to **all** container engines (`edgelet`, `docker`, `podman`). Local deploy YAML and controller-managed microservices use the same on-disk layout and the same `scope` field.

---

## Why this matters

A `VOLUME` mapping is an Edgelet-managed directory under `{diskDirectory}/volumes/`, bind-mounted into the container. It is **not** Docker's named-volume subsystem.

**Deleting a microservice removes the container, not the volume data.** Persistent `VOLUME` directories survive OTA, data-plane restart, crash, recreate, microservice delete, and deprovision. They are destroyed only by **explicit reclaim** (`edgelet volume`, `deprovision --purge-volumes`, or `controlplane delete` for controller DB/log).

Scheduled prune (`pruningFrequency`), disk-threshold prune, and `edgelet system prune` do **not** delete `volumes/data/` or `volumes/shared/`.

---

## On-disk layout

Base path: `{diskDirectory}` (default `/var/lib/edgelet/`).

```
volumes/
  data/{microservice-uuid}/{volume-name}/     # private VOLUME (all engines)
  shared/{volume-name}/                       # shared VOLUME (node-global name)
  microservices/{microservice-uuid}/          # Per-MS mount staging (secrets/configmaps)
  secrets/{volume-name}/                      # Controller secret payloads
  configMaps/{volume-name}/                   # Controller configmap payloads
```

Image layers and container filesystems live under `/var/lib/edgelet-containerd/` (embedded engine), **not** under `volumes/data/` or `volumes/shared/`. Backing up `edgelet.db` alone does **not** back up `VOLUME` data — see [persistence.md](persistence.md).

---

## Volume mapping types

| Type | `hostDestination` | Edgelet behavior |
|------|-------------------|------------------|
| **`VOLUME`** | Relative name, e.g. `mydata` | Bind-mounts an Edgelet-managed directory (private or shared; see [scope](#volume-scope-private-vs-shared)) |
| **`VOLUME_MOUNT`** | Controller volume name, e.g. `my-secret` or `my-secret/key` | Materializes under `volumes/microservices/{ms-uuid}/` from shared `secrets/` or `configMaps/` trees. `scope` is ignored |
| **`BIND`** | Absolute host path, e.g. `/opt/data` | Bind-mounts that path. Use this to pin a path **outside** `diskDirectory`. Edgelet never deletes it. `scope` is ignored |

Local deploy manifests (`edgelet deploy -f`) support **`BIND`** and **`VOLUME`** only. Controller workloads may also use **`VOLUME_MOUNT`** for secrets and configmaps synced from Pot.

---

## VOLUME scope (private vs shared)

`scope` is meaningful **only** when `type` is `volume` (YAML `VOLUME`). Omit, empty, or unknown `scope` → **private**. The apply does not fail. `scope` on `BIND` / `VOLUME_MOUNT` is ignored.

The same field is used on local YAML and on controller `volumeMappings[]`. A **local** workload and a **controller** workload may both consume the same shared name.

| `scope` | Host path | Identity |
|---------|-----------|----------|
| **`private`** (default) | `{diskDirectory}/volumes/data/{microservice-uuid}/{name}/` | Per microservice UUID |
| **`shared`** | `{diskDirectory}/volumes/shared/{name}/` | Node-global **name** |

Private `config` and shared `config` are **different disks**. Shared names are node-global — use unique names (`nodered-config`), not generic `config`.

On-node ControlPlane db/log volumes are always **private**.

```yaml
volumes:
  - hostDestination: config
    containerDestination: /app/config
    accessMode: rw
    type: volume          # omit scope → private
  - hostDestination: nodered-config
    containerDestination: /data
    accessMode: rw
    type: volume
    scope: shared
```

### Identity changes

| Change | Result |
|--------|--------|
| New UUID + **private** | New empty directory under `volumes/data/{new-uuid}/` |
| New UUID + **shared** same name | Remounts `volumes/shared/{name}/` (existing data) |
| Old private UUID after delete | Data remains until explicit reclaim |
| private → shared | New empty `volumes/shared/{name}/`; old `{uuid}/{name}` is **not** copied |
| shared → private | New empty per-UUID directory; the shared claim loses this consumer |

### Shared `runAsUser`

Every consumer of a shared name should use the **same** `runAsUser`. Edgelet chowns a shared directory **only if it is new**. An existing shared directory is not chowned again (that would steal the tree from another consumer).

---

## What happens when a microservice is deleted

Deletion paths:

- **Controller:** microservice marked `delete: true` in Pot snapshot → process manager reconciles removal
- **Local:** `edgelet ms rm <id>` or EdgeletAPI delete of a local workload

In all cases Edgelet:

1. Stops and removes the container
2. Runs volume-mount cleanup for that microservice UUID (secret/configmap staging)
3. Drops this UUID as a consumer of any **shared** name (the disk stays if another consumer remains)

`edgelet ms rm --cleanup` records a reserved bit for a later explicit `edgelet volume prune`. It does **not** delete data now and does not start a sweeper.

### Always removed

| Path | When |
|------|------|
| Container / CRI sandbox | On delete |
| `volumes/microservices/{uuid}/` | On delete (per-MS secret/configmap staging) |

### Not removed on delete

| Path | Reason |
|------|--------|
| `volumes/data/{uuid}/` | Private **VOLUME** data is retained until explicit reclaim |
| `volumes/shared/{name}/` | Shared disk stays while any other consumer remains; last consumer still retains until explicit reclaim |
| `volumes/secrets/`, `volumes/configMaps/` | Shared controller artifacts; other workloads may still reference them |
| **`BIND`** host paths | Operator-managed; Edgelet never deletes them |

---

## `deleteWithCleanup`

The controller flag `deleteWithCleanup` is **unused** for persistent `VOLUME` reclaim. Edgelet does not auto-delete `volumes/data/` or `volumes/shared/` when a microservice is removed. Operators reclaim with `edgelet volume`.

Local `edgelet ms rm` always uses non-cleanup removal (container only; no automatic image or volume data delete). `--cleanup` is reserved as described above.

---

## When volume data is actually deleted

Persistent `VOLUME` data is destroyed **only** by these paths:

| Command | What it destroys |
|---------|------------------|
| `edgelet volume rm <uuid> [<name>]` | **Private** claim under `volumes/data/{uuid}/` |
| `edgelet volume rm --shared <name>` | **Shared** claim under `volumes/shared/{name}/` |
| `edgelet volume prune` | Unreferenced claims. **Dry-run by default.** Destroy requires `--yes`. Skips claims younger than **24h** after the last consumer left unless `--force`. Never control-plane volumes |
| `edgelet deprovision --purge-volumes` | **Workload** persistent volumes only. Shared names are destroyed **only if no remaining consumers**. Control-plane volumes are never purged. A local-only purge does not drop a shared name still consumed by a remaining controller microservice |
| `edgelet controlplane delete` | Control-plane DB/log volumes for that controller UUID only (always private) |

`DELETE /v1/volumes/{uuid}` is private only. `DELETE /v1/volumes/shared/{name}` is shared only. Remaining consumers or a still-desired UUID return **409**. Missing shared name returns **404**.

`--force` bypasses the desired-state / 24h gate **only when nothing is mounted**. Even `--force` **fails** if a listed container still bind-mounts that host path.

### Not destroy paths

| Action | Persistent `VOLUME` data |
|--------|--------------------------|
| `pruningFrequency` tick / disk-threshold prune | **Not deleted** |
| `edgelet system prune` (`dangling`, `volumes`, `all`) | **Not deleted.** `volumes` errors and points at `edgelet volume prune` |
| Controller `getChanges.prune` | Images + unused local models only |
| Deprovision without `--purge-volumes` (including `delete-node` / Edge Guard) | **Preserved** (`volumes/data/` and `volumes/shared/`) |
| OTA / data-plane restart / crash / recreate | **Preserved** |

Do not `rm -rf` under `volumes/data/` or `volumes/shared/` while the daemon is running. Use `edgelet volume`.

---

## Docker and Podman

On Docker and Podman, new **private** `VOLUME` mappings bind `{diskDirectory}/volumes/data/{uuid}/{name}/`. New **shared** mappings bind `{diskDirectory}/volumes/shared/{name}/`. Edgelet does **not** create a node-global Docker volume named only `hostDestination`.

Existing node-global Docker volumes that were named only `hostDestination` are **not** copied automatically onto the new per-UUID or `volumes/shared/` paths. Copy data yourself if you still need it, then reclaim with `edgelet volume`.

Scheduled prune does not call unrestricted Docker/Podman volume prune.

---

## Operator checklist

| Goal | Action |
|------|--------|
| Delete workload, keep data for redeploy | `edgelet ms rm` or controller delete — private data remains under `volumes/data/{uuid}/`; shared disk stays while another consumer remains |
| Share one directory between two microservices | `type: volume` + `scope: shared` + a unique name. Same `runAsUser` on every consumer |
| Pin an absolute host path outside `diskDirectory` | `type: bind` — Edgelet never deletes it |
| List claims | `edgelet volume ls` |
| Destroy a private UUID | `edgelet volume rm <uuid>` (add `--force` only if unmounted and you intend to drop desired-state data) |
| Destroy a shared name | `edgelet volume rm --shared <name>` |
| Preview orphans | `edgelet volume prune` (dry-run) |
| Destroy orphans | `edgelet volume prune --orphans --yes` (add `--force` to skip the 24h window when unmounted) |
| Wipe workload volumes on deprovision | `edgelet deprovision --purge-volumes` (control-plane volumes kept; shared only if consumers=0) |
| Remove on-node controller DB/log | `edgelet controlplane delete` |
| Backup stateful workloads | Copy `edgelet.db` **and** `volumes/data/` **and** `volumes/shared/` (and BIND paths if used) |
| Wipe node completely | Stop services, remove `diskDirectory` (see [persistence.md](persistence.md)) |

---

## Troubleshooting

### Disk usage grows after microservices are deleted

1. List remaining volume claims:

   ```bash
   edgelet volume ls
   sudo du -sh /var/lib/edgelet/volumes/data/* /var/lib/edgelet/volumes/shared/* 2>/dev/null
   ```

2. Confirm no container still references the UUID or shared name:

   ```bash
   edgelet ms ls
   ```

3. Preview then reclaim:

   ```bash
   edgelet volume prune
   edgelet volume prune --orphans --yes
   ```

`edgelet system prune volumes` does **not** delete this data.

### Redeployed microservice has empty volume

Private `VOLUME` data is keyed by **microservice UUID**. A new deployment with a new UUID gets a **new** empty directory under `volumes/data/`. Old UUID data remains until you reclaim it.

To keep data across UUID changes:

- Use **`scope: shared`** with the same name (remounts `volumes/shared/{name}/`), or
- Use **`BIND`** to a fixed host path you control (outside `diskDirectory` if you want Edgelet never to own it)

Switching a mapping from private to shared does **not** copy the old UUID directory.

### Shared directory owned by the wrong user

Edgelet chowns a shared directory only when it is **new**. If the first consumer used a different `runAsUser`, later consumers must match that uid (or use an entrypoint that can write). Mixed uids are not supported.

---

## Related documentation

| Document | Topic |
|----------|--------|
| [manifest-reference.md](manifest-reference.md) | `spec.container.volumes` YAML fields including `scope` |
| [persistence.md](persistence.md) | SQLite backup; include `volumes/data/` and `volumes/shared/` |
| [container-engine.md](container-engine.md) | `diskDirectory` vs containerd paths |
| [modules/volumemount.md](modules/volumemount.md) | Secret/configmap materialization (not persistent `VOLUME` reclaim) |
| [modules/pruning.md](modules/pruning.md) | Scheduled prune: images and unused local models |
| [CONTROLLER-HANDOFF-VOLUMES.md](CONTROLLER-HANDOFF-VOLUMES.md) | Controller `volumeMappings[].scope` contract |
| [edgelet-api-v1.md](edgelet-api-v1.md) | `GET/DELETE /v1/volumes*` |
