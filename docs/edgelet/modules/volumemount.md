# Volume Mount Manager

The volume mount manager materializes **Controller secrets and configmaps** (and related volume types) onto the host filesystem for bind-mounting into microservice containers. It uses a Kubernetes-style `..data` symlink layout with atomic updates.

**Code:** `internal/volumemount/`

## Purpose

- Process volume mount deltas from Controller change feed
- Store secret/configmap payloads under `{diskDirectory}/volumes/`
- Maintain mount index and type cache for engine volume creation
- Cleanup per-microservice mounts on container removal
- Report active mount counts to StatusReporter

## Dependencies

| Depends on | Reason |
|------------|--------|
| `store` | `controller_volume_mounts` table |
| `config` | `diskDirectory` base path |
| `statusreporter` | Active mount metrics |

| Used by | Reason |
|---------|--------|
| `fieldagent` | `ProcessVolumeMountChanges()` from sync/changes |
| `processmanager` | `CleanupMicroserviceVolumes()` on MS delete |
| `pkg/engine/edgelet/cri` | Resolve mount paths when creating containers |
| `pkg/docker` | Volume type lookup for docker engine |

## Storage layout

Base: `{diskDirectory}/volumes/`

```
volumes/
  secrets/{uuid}/...
  configMaps/{uuid}/...
  microservices/{msUUID}/...
  serviceaccounts/...   # owned by serviceaccount manager (separate tree)
```

Persistent `VOLUME` data under `volumes/data/` and `volumes/shared/` is **not** owned by this manager and is not deleted by `Clear()`. See [../volumes.md](../volumes.md).

Internal dirs: mode **0750**. Bind-mount targets exposed to containers: **0755** dirs, **0644** files (non-root readable).

Atomic update pattern uses `..data` symlink (see `dataSymlink` constant).

## Lifecycle

### Init (singleton)

`GetInstance()` on first use:

1. `MkdirAll` volumes base
2. `loadFromStore()` — hydrate from SQLite
3. `rebuildTypeCache()`

Not a Supervisor `Module` with explicit Start — initialized lazily when first referenced.

### Change processing

`ProcessVolumeMountChanges(records)`:

- Upsert new/changed volume mounts from Controller
- Remove deleted mounts
- Update StatusReporter active mount count

Triggered from Field Agent sync path (`fieldagent/sync.go`).

### Cleanup

`CleanupMicroserviceVolumes(microserviceUUID)` when container removed — preserves shared secret/configmap data dirs where appropriate (see tests). `Clear()` (deprovision) does **not** walk `volumes/data/` or `volumes/shared/`; persistent `VOLUME` reclaim is `edgelet volume` / `--purge-volumes`. Operator guide: [../volumes.md](../volumes.md).

## Volume types

| Type | Constant |
|------|----------|
| Secret | `VolumeMountTypeSecret` |
| ConfigMap | `VolumeMountTypeConfigMap` |
| Microservice | `VolumeMountTypeMicroservice` |

SQLite `controller_volume_mounts.kind` CHECK: `SECRET` | `CONFIGMAP`.

## Configuration

Uses `config.DiskDirectory` only (no dedicated frequency keys).

## Data and persistence

| Table | Role |
|-------|------|
| `controller_volume_mounts` | UUID, version, checksum, microservice bindings, JSON data |

Filesystem holds rendered files; DB holds authoritative metadata.

## External APIs

No direct HTTP. Indirect via Controller sync and container mounts.

Status exposure: `VolumeMountManagerStatus` on system status payload.

## Observability

- Log module: `"VolumeMountManager"`
- `UpdateVolumeMountManagerStatus(activeMounts, lastUpdate)` on StatusReporter

## Failure modes

| Symptom | Typical cause |
|---------|----------------|
| MS missing secret file | Volume mount change not processed; checksum mismatch |
| Permission denied in container | Bind mount mode vs container user |
| Stale mounts | Cleanup not run after MS delete |

## Code map

| File | Role |
|------|------|
| `manager.go` | Core reconcile, atomic writes, cleanup |
| `manager_test.go` | Store + cleanup tests |

Related: [fieldagent.md](fieldagent.md), [store.md](store.md), [processmanager.md](processmanager.md), [serviceaccount.md](serviceaccount.md).
