# Edgelet SQLite persistence (operator guide)

Edgelet stores on-device state in a single SQLite database. This document covers **backup and restore**, **wipe-only upgrades**, **secrets-at-rest expectations**, and known **JSON-in-column** schema debt.

There is **no** EdgeletAPI or CLI backup command — operators use filesystem copy after stopping services.

---

## Database location

| Item | Value |
|------|--------|
| Config key | `diskDirectory` (alias `dl`) in `/etc/edgelet/config.yaml` |
| Default directory | `/var/lib/edgelet/` |
| Database file | `{diskDirectory}/edgelet.db` |
| WAL sidecars | `{diskDirectory}/edgelet.db-wal`, `{diskDirectory}/edgelet.db-shm` (when WAL journal is active) |

Resolve the path on a running node:

```bash
grep diskDirectory /etc/edgelet/config.yaml
edgelet system info -o json | jq -r '.diskDirectory'
```

On open, Edgelet creates `diskDirectory` with mode **0700** if missing. SQLite runs with **WAL** journal mode (`_journal_mode=WAL`).

**Schema version:** fresh installs apply embedded migrations `001_edgelet_schema_v1.sql`, `002_edgelet_schema_v2.sql`, `003_edgelet_schema_v3.sql`, then `004_edgelet_schema_v4.sql` and record version **4** in `schema_versions`. Nodes already on schema v1–v3 upgrade **in place** on first start of a schema-v4 binary — no wipe. **Back up `edgelet.db` (and WAL sidecars) plus `volumes/data/`, `volumes/shared/`, `models/`, and `knowledge/` before that first start** — see [Backup runbook](#backup-runbook-r85) and [Schema v4](#schema-v4-in-place-from-v3). There is no in-place upgrade from **pre–schema-v1** databases (see [Wipe-only upgrade](#wipe-only-upgrade)).

---

## What the database holds

Tables are grouped by **source prefix** (schema v4 extends v3, which extends v2, which extends v1):

| Prefix | Examples | Contents |
|--------|----------|----------|
| `controller_*` | `controller_microservices`, `controller_registries`, `controller_volume_mounts`, `controller_models`, `controller_knowledge`, `controller_runtime_classes` | Pot controller snapshot (MS list, registries, volume mounts, fleet models, fleet Knowledge, fleet RuntimeClass) |
| `agent_*` | `agent_credentials`, `agent_edgeguard_signature` | Agent identity and EdgeGuard material |
| `local_*` | `local_workloads`, `local_registries`, `local_models`, `local_knowledge`, `local_runtime_classes`, `local_service_account_tokens`, … | EdgeletAPI deploy, local registries, local models, local Knowledge, applied RuntimeClass, RBAC tokens |
| `system_*` | `system_control_plane` | Singleton ControlPlane deployment row |
| `runtime_*` | `runtime_container_refs` | CRI/Docker workload and sandbox IDs (`scope` = `controller` \| `local`) |
| (unprefixed) | `model_refs`, `knowledge_refs`, `persistent_volumes` | Explicit keep-alive refs for model and Knowledge prune; persistent `VOLUME` ownership ledger (schema v3) |

Registry tables (`local_registries`, `controller_registries`) include **`type`** (`oci` \| `hf`), **`ca_b64`**, and **`insecure`**.

Image layers and containerd state live **outside** `diskDirectory` (for example `/var/lib/edgelet-containerd/` on the embedded engine). Model artifacts live under **`{diskDirectory}/models/`**. Knowledge artifacts live under **`{diskDirectory}/knowledge/`**. Backing up `edgelet.db` does **not** back up pulled images, running container filesystems, model files, or Knowledge files.

Stateful **`VOLUME`** mappings persist files under `{diskDirectory}/volumes/data/` (private, per microservice UUID) and `{diskDirectory}/volumes/shared/` (shared, node-global name) outside SQLite. Include **both** trees in backups for stateful workloads. See [volumes.md](volumes.md). Include `{diskDirectory}/models/` when you need to restore pulled models without re-downloading — see [models.md](models.md). Include `{diskDirectory}/knowledge/` when you need to restore pulled Knowledge without re-downloading — see [knowledge.md](knowledge.md).

---

## Backup runbook (R85)

Back up when you need to preserve controller/local desired state across reinstall, disk migration, or disaster recovery on the **same** Edgelet schema family (v1, v2, v3, or v4). A v1–v3 database is upgraded in place to v4 the next time a schema-v4 binary opens it.

### Prerequisites

- Root or sudo on the edge node
- Enough disk space for a copy of `edgelet.db`, WAL sidecars, and `{diskDirectory}/volumes/data/` plus `{diskDirectory}/volumes/shared/` when backing up stateful workloads
- Maintenance window — workloads stop while services are down

### 1. Stop services

Stop Edgelet first so the daemon can checkpoint WAL and close SQLite cleanly.

```bash
sudo systemctl stop edgelet.service
```

If the node uses the **embedded** `edgelet` engine, also stop containerd:

```bash
sudo systemctl stop edgelet-containerd.service 2>/dev/null || true
```

For `containerEngine: docker` or `podman`, only `edgelet.service` is required.

Confirm nothing holds the DB open:

```bash
sudo lsof /var/lib/edgelet/edgelet.db 2>/dev/null || true
```

(Adjust the path if `diskDirectory` is non-default.)

### 2. Copy database files

Set `DISK` to your `diskDirectory`, then copy the database and any WAL sidecars:

```bash
DISK=/var/lib/edgelet
BACKUP_DIR=/root/edgelet-db-backup-$(date +%Y%m%d-%H%M%S)
sudo mkdir -p "$BACKUP_DIR"
sudo cp -a "$DISK/edgelet.db" "$BACKUP_DIR/"
[ -f "$DISK/edgelet.db-wal" ] && sudo cp -a "$DISK/edgelet.db-wal" "$BACKUP_DIR/"
[ -f "$DISK/edgelet.db-shm" ] && sudo cp -a "$DISK/edgelet.db-shm" "$BACKUP_DIR/"
sudo chmod 700 "$BACKUP_DIR"
ls -la "$BACKUP_DIR"
```

Copy persistent volume trees (stateful workloads):

```bash
if [ -d "$DISK/volumes/data" ]; then
  sudo mkdir -p "$BACKUP_DIR/volumes"
  sudo cp -a "$DISK/volumes/data" "$BACKUP_DIR/volumes/"
fi
if [ -d "$DISK/volumes/shared" ]; then
  sudo mkdir -p "$BACKUP_DIR/volumes"
  sudo cp -a "$DISK/volumes/shared" "$BACKUP_DIR/volumes/"
fi
```

Optional — copy pulled model and Knowledge artifacts (can be large):

```bash
if [ -d "$DISK/models" ]; then
  sudo cp -a "$DISK/models" "$BACKUP_DIR/"
fi
if [ -d "$DISK/knowledge" ]; then
  sudo cp -a "$DISK/knowledge" "$BACKUP_DIR/"
fi
```

After a **graceful** `systemctl stop edgelet`, Edgelet runs `PRAGMA wal_checkpoint(TRUNCATE)` on close; often only `edgelet.db` is needed. If you copy while the daemon was **not** stopped cleanly, include `-wal` and `-shm` or the backup may be inconsistent.

### 3. Archive (optional)

```bash
sudo tar -czf "$BACKUP_DIR.tar.gz" -C "$(dirname "$BACKUP_DIR")" "$(basename "$BACKUP_DIR")"
```

Store the archive off-node. Treat it as **sensitive** (see [Threat model](#threat-model-r86)).

### 4. Start services

```bash
sudo systemctl start edgelet-containerd.service 2>/dev/null || true
sudo systemctl start edgelet.service
```

Verify:

```bash
edgelet system status -o json | jq '{state, containerEngine}'
sudo journalctl -u edgelet -n 30 --no-pager
```

---

## Restore runbook (R85)

Restore onto a node running a **schema v4** binary (or an older v1–v3 database, which will migrate to v4 on start). Restoring a pre–v1 backup onto a current binary is unsupported — use [wipe-only upgrade](#wipe-only-upgrade) and let the controller/EdgeletAPI repopulate state instead.

### Steps

1. **Stop** `edgelet.service` and `edgelet-containerd.service` (if present), same as backup.
2. **Replace** files under `diskDirectory`:
   - Remove current DB files: `edgelet.db`, `edgelet.db-wal`, `edgelet.db-shm`
   - Copy backup files into place with ownership/mode consistent with the edgelet user (typically root on bare metal).
3. **Start** `edgelet-containerd.service` (if used), then `edgelet.service`.
4. On start, Edgelet runs `PRAGMA integrity_check` — if the file is corrupt, the **supervisor does not start** and logs an integrity error (R83).

```bash
DISK=/var/lib/edgelet
sudo systemctl stop edgelet.service edgelet-containerd.service 2>/dev/null || true
sudo rm -f "$DISK/edgelet.db" "$DISK/edgelet.db-wal" "$DISK/edgelet.db-shm"
sudo cp -a /path/to/backup/edgelet.db "$DISK/"
[ -f /path/to/backup/edgelet.db-wal ] && sudo cp -a /path/to/backup/edgelet.db-wal "$DISK/"
[ -f /path/to/backup/edgelet.db-shm ] && sudo cp -a /path/to/backup/edgelet.db-shm "$DISK/"
sudo chmod 700 "$DISK"
# Restore persistent VOLUME trees
# sudo rm -rf "$DISK/volumes/data" "$DISK/volumes/shared"
# sudo mkdir -p "$DISK/volumes"
# sudo cp -a /path/to/backup/volumes/data "$DISK/volumes/"
# sudo cp -a /path/to/backup/volumes/shared "$DISK/volumes/"
# Optional: restore model artifacts
# sudo rm -rf "$DISK/models"
# sudo cp -a /path/to/backup/models "$DISK/"
# Optional: restore Knowledge artifacts
# sudo rm -rf "$DISK/knowledge"
# sudo cp -a /path/to/backup/knowledge "$DISK/"
sudo systemctl start edgelet-containerd.service 2>/dev/null || true
sudo systemctl start edgelet.service
```

**Not covered:** HA, replication, or online hot backup. Edgelet does not ship Litestream or a second SQL tier.

---

## Schema v2 (in-place from v1)

Schema v2 adds registry `type` / TLS columns, model tables, catalog bind columns, expanded microservice container fields, fleet RuntimeClass snapshot, and RuntimeClass `source`. A schema-v2 binary applies migration `002_edgelet_schema_v2.sql` automatically. **No wipe** is required for v1 → v2. A later schema-v3 binary then applies `003` (see [Schema v3](#schema-v3-in-place-from-v2)).

**Before the first schema-v2 binary opens a v1 database:** stop `edgelet.service` (and `edgelet-containerd.service` when used) and copy `edgelet.db` plus any `-wal` / `-shm` sidecars off-node. The upgrade is in-place and does not delete rows, but a backup is the only rollback if the host fails mid-migration.

| Change | Detail |
|--------|--------|
| `local_registries` / `controller_registries` | Columns `type` (`oci` \| `hf`, default `oci`), `ca_b64`, `insecure`. Local built-ins: id 1 `docker.io`, id 2 `from_cache`, id 3 `https://huggingface.co` (`hf`). Hugging Face Hub is not seeded on the controller table. |
| `local_models` | Local `kind: Model` rows and pull state, plus **`source`** (`local` \| `managed`, default `local`) |
| `controller_models` | Fleet snapshot. Primary key is **`uuid`**; **`name`** is unique. `getChanges` `models` replace-all + pull |
| `controller_runtime_classes` | Fleet RuntimeClass snapshot (`name` PK + `handler`). Replace-all like other controller tables |
| `local_runtime_classes.source` | Applied class provenance: `local` \| `managed` (default `local`). Managed wins `name` while provisioned |
| `controller_microservices` | Catalog JSON (`models`) and typed container columns (`run_as_group`, `cpus`, `memory_reservation`, `memory_swap`, `shm_size`, `working_dir`, `read_only_root_filesystem`, plus JSON text for `sysctls`, `ulimits`, `devices`, `tmpfs`, `entrypoint`, `commands`) |
| `model_refs` | Catalog bind refs so prune and `model rm` do not delete in-use artifacts |

Backup `{diskDirectory}/models/` in addition to `edgelet.db` if you need pulled weights without a re-download. See [models.md](models.md).

Confirm schema version after start (optional, on node with `sqlite3`):

```bash
sqlite3 /var/lib/edgelet/edgelet.db 'SELECT MAX(version) FROM schema_versions;'
# expect: 2
```

---

## Schema v3 (in-place from v2)

Schema v3 adds the persistent-volume ownership ledger (`persistent_volumes`). A schema-v3 binary applies migration `003_edgelet_schema_v3.sql` automatically. **No wipe** is required for v2 → v3 (or v1 → v3).

**Before the first schema-v3 binary opens a v1 or v2 database:** stop `edgelet.service` (and `edgelet-containerd.service` when used) and copy `edgelet.db` plus any `-wal` / `-shm` sidecars **and** `{diskDirectory}/volumes/data/` plus `{diskDirectory}/volumes/shared/` off-node. The upgrade is in-place and does not delete volume files, but a backup is the only rollback if the host fails mid-migration.

| Change | Detail |
|--------|--------|
| `persistent_volumes` | One row per consumer of a persistent `VOLUME` claim: `(ms_uuid, volume_name)` with `scope` (`private` \| `shared`, default `private`), `kind` (`workload` \| `controlplane`), `host_path`, and timestamps. Local and controller microservices share this table. |

Existing on-disk `volumes/data/{uuid}/*` directories are recorded as **private** on first open. Shared claims appear only after a microservice is applied with `scope: shared`. See [volumes.md](volumes.md).

Confirm schema version after start (optional, on node with `sqlite3`):

```bash
sqlite3 /var/lib/edgelet/edgelet.db 'SELECT MAX(version) FROM schema_versions;'
# expect: 3
```

---

## Schema v4 (in-place from v3)

Schema v4 adds Knowledge artifact tables and the microservice Knowledge catalog column. A schema-v4 binary applies migration `004_edgelet_schema_v4.sql` automatically. **No wipe** is required for v3 → v4 (or v1/v2 → v4).

**Before the first schema-v4 binary opens a v1–v3 database:** stop `edgelet.service` (and `edgelet-containerd.service` when used) and copy `edgelet.db` plus any `-wal` / `-shm` sidecars **and** `{diskDirectory}/knowledge/` off-node (plus `models/` and volume trees if you already back those up). The upgrade is in-place and does not delete rows or on-disk Knowledge trees, but a backup is the only rollback if the host fails mid-migration.

| Change | Detail |
|--------|--------|
| `local_knowledge` | Local `kind: Knowledge` rows and pull state, plus **`source`** (`local` \| `managed`, default `local`) and optional `format` hint |
| `controller_knowledge` | Fleet snapshot. Primary key is **`uuid`**; **`name`** is unique. `getChanges` `knowledge` replace-all + pull |
| `knowledge_refs` | Catalog bind refs so prune and `knowledge rm` do not delete in-use artifacts |
| `controller_microservices.knowledge` | Catalog JSON (same shape as the `models` column: `bindPath`, `permissions`, `items[].name`) |

Backup `{diskDirectory}/knowledge/` in addition to `edgelet.db` if you need pulled retrieval artifacts without a re-download. See [knowledge.md](knowledge.md).

Confirm schema version after start (optional, on node with `sqlite3`):

```bash
sqlite3 /var/lib/edgelet/edgelet.db 'SELECT MAX(version) FROM schema_versions;'
# expect: 4
```

---

## Wipe-only upgrade

**Pre–schema-v1 databases only:** Edgelet does **not** migrate in-place from the old incremental schema (migrations 001–011 era) to v1. Operators on dev or lab nodes that already had an `edgelet.db` from pre–v1 builds must **delete** the database before the first schema-v1 (or later) binary run.

v1 → v2, v2 → v3, and v3 → v4 are **in-place** (see [Schema v2](#schema-v2-in-place-from-v1), [Schema v3](#schema-v3-in-place-from-v2), and [Schema v4](#schema-v4-in-place-from-v3)). Do not wipe solely to pick up model/registry columns, the volume ledger, or Knowledge tables.

No published production fleets require a 012→013 migrator; fresh Lima VMs and wiped DBs are the integration-test baseline.

### Procedure

```bash
sudo systemctl stop edgelet.service edgelet-containerd.service 2>/dev/null || true

DISK=/var/lib/edgelet   # or your diskDirectory
sudo rm -f "$DISK/edgelet.db" "$DISK/edgelet.db-wal" "$DISK/edgelet.db-shm"

sudo systemctl start edgelet-containerd.service 2>/dev/null || true
sudo systemctl start edgelet.service
```

After wipe:

- Controller microservices and registries are **re-pushed** from Pot on the next field-agent sync.
- Local workloads and ControlPlane require **re-deploy** via EdgeletAPI/CLI if you relied on local state.
- Legacy **JSON file → SQLite** import (`MigrateJSONToSQLite`) is **removed** — do not expect old JSON caches to repopulate the DB.

Confirm schema version after start (optional, on node with `sqlite3`):

```bash
sqlite3 /var/lib/edgelet/edgelet.db 'SELECT MAX(version) FROM schema_versions;'
# expect: 4  (schema-v4 binary after a wipe applies 001, 002, 003, then 004)
```

---

## Runtime checks (R83, R84)

| Event | Behavior |
|-------|----------|
| **Open** | `PRAGMA integrity_check` must return `ok`; otherwise startup **fails hard** |
| **Graceful stop** | `PRAGMA wal_checkpoint(TRUNCATE)` before closing the connection (`systemctl stop edgelet`) |

If integrity fails after restore or disk errors, treat the DB as damaged: restore from a known-good backup or wipe and reconcile from controller/local deploys.

Future schema version bumps (v2+) may run an optional one-time `VACUUM` after migration apply; not used on v1-only installs.

---

## Threat model (R86)

### Sensitive data in SQLite

| Data | Tables / columns (examples) |
|------|-----------------------------|
| Registry credentials | `controller_registries.password`, `local_registries.password` |
| Agent private key | `agent_credentials.private_key_b64` |
| EdgeGuard JWT | `agent_edgeguard_signature.signature_jwt` |
| Service account material | `local_service_account_tokens` (`token_sha256`, `claims_json`, `rules_by_group_json`, …) |
| Volume / MS config blobs | JSON/text columns on controller and local tables |

Edgelet does **not** encrypt these fields at the application layer in schema v1.

### Trust boundary

Protection relies on **edge node tenancy** and **host filesystem** controls:

- `diskDirectory` is created with mode **0700**.
- The SQLite file must not be world-readable; limit backup archives to admin access.
- A single `edgelet` process holds one DB (`store.GetInstance()` singleton).
- Physical or root access to the node implies read access to the DB and backups.

### Out of scope (schema v1)

- SQLCipher or field-level encryption
- TPM-sealed keys or OS full-disk encryption policy (operator choice outside Edgelet)
- Centralized secrets vault sync into SQLite

**Future (regulated verticals):** app-level encryption at rest may be added in a later plan; R86 documents the current OS-boundary model only.

---

## JSON-in-column schema debt (R87)

Several v1 tables store structured data as **JSON text columns** instead of normalized child tables. Examples:

| Table | JSON / blob columns |
|-------|------------------------|
| `controller_microservices` | `port_mappings`, `volume_mappings`, `env_vars`, `args`, `annotations`, … |
| `controller_volume_mounts` | `microservices`, `data` |
| `local_service_account_tokens` | `rules_by_group_json`, `claims_json` |
| `local_workloads` | `manifest_yaml` (YAML blob) |

**Accepted in schema v1:** behavior and Pot snapshot semantics stay unchanged; queries and migrations remain simple.

**Future work:** normalize hot paths (ports, env, RBAC rules) into relational tables with strict migrator versions — tracked as schema debt under **RFC R87** (see [persistence.md](persistence.md) table list above).

---

## Regression gates (Lima IT)

Integration tests assume a **fresh database**. Wipe the VM disk or delete `edgelet.db` (+ WAL/SHM) on the Lima guest before running the gates below. A current binary applies migrations through schema **v4**.

### Prerequisites

- `limactl` and Lima VMs (see each suite’s `run-all.sh` header; typical names: `iofog-test`, `edgelet-engine-lifecycle`).
- Repo root as working directory.

### Wipe DB inside a Lima VM (before IT)

```bash
# Replace INSTANCE with your VM name (e.g. iofog-test)
limactl shell INSTANCE -- sudo systemctl stop edgelet.service edgelet-containerd.service 2>/dev/null || true
limactl shell INSTANCE -- sudo rm -f /var/lib/edgelet/edgelet.db /var/lib/edgelet/edgelet.db-wal /var/lib/edgelet/edgelet.db-shm
```

For a completely clean slate, recreate or reset the Lima instance per [deployment.md](deployment.md) embedded-engine section, then run setup scripts once.

### Run gates (from repo root)

```bash
./test/control-plane/run-all.sh
./test/workload-continuity/run-all.sh
./test/embedded/run-all.sh
```

### Unit gate (no Lima)

```bash
go test ./internal/store/... ./internal/fieldagent/... ./internal/processmanager/... \
  ./internal/runtimeapi/... ./internal/edgeletapi/... ./internal/auth/... -short -count=1
```

---

## Related documentation

| Document | Topic |
|----------|--------|
| [installation.md](installation.md) | Install, OTA, upgrade/rollback |
| [deployment.md](deployment.md) | systemd units, `diskDirectory` layout |
| [troubleshooting.md](troubleshooting.md) | Daemon won't start (includes disk space under `/var/lib/edgelet`) |
| [control-plane.md](control-plane.md) | ControlPlane redeploy after DB wipe |
| [container-engine.md](container-engine.md) | `/var/lib/edgelet` vs `edgelet-containerd` data paths |
| [volumes.md](volumes.md) | `volumes/data/` and `volumes/shared/` lifecycle and backup scope |
| [models.md](models.md) | `{diskDirectory}/models/` layout, pull, prune |
| [knowledge.md](knowledge.md) | `{diskDirectory}/knowledge/` layout, pull, prune |
