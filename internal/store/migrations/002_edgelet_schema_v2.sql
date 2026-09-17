-- Edgelet schema v2: registry type/TLS, model tables, catalog bind, container columns (in-place from v1)

ALTER TABLE local_registries ADD COLUMN type TEXT NOT NULL DEFAULT 'oci' CHECK(type IN ('oci','hf'))

ALTER TABLE local_registries ADD COLUMN ca_b64 TEXT NOT NULL DEFAULT ''

ALTER TABLE local_registries ADD COLUMN insecure INTEGER NOT NULL DEFAULT 0

ALTER TABLE controller_registries ADD COLUMN type TEXT NOT NULL DEFAULT 'oci' CHECK(type IN ('oci','hf'))

ALTER TABLE controller_registries ADD COLUMN ca_b64 TEXT NOT NULL DEFAULT ''

ALTER TABLE controller_registries ADD COLUMN insecure INTEGER NOT NULL DEFAULT 0

CREATE TABLE IF NOT EXISTS local_models (
    name                TEXT    PRIMARY KEY,
    source              TEXT    NOT NULL DEFAULT 'local' CHECK(source IN ('local','managed')),
    repo                TEXT    NOT NULL DEFAULT '',
    revision            TEXT    NOT NULL DEFAULT '',
    registry_id         INTEGER NOT NULL,
    files_json          TEXT    NOT NULL DEFAULT '[]',
    format              TEXT    NOT NULL DEFAULT '',
    state               TEXT    NOT NULL DEFAULT 'Pending' CHECK(state IN ('Pending','Pulling','Ready','Failed')),
    last_error          TEXT    NOT NULL DEFAULT '',
    generation          INTEGER NOT NULL DEFAULT 1,
    observed_generation INTEGER NOT NULL DEFAULT 0,
    manifest_yaml       TEXT    NOT NULL DEFAULT '',
    manifest_path       TEXT    NOT NULL DEFAULT '',
    content_path        TEXT    NOT NULL DEFAULT '',
    resolved_revision   TEXT    NOT NULL DEFAULT '',
    digest              TEXT    NOT NULL DEFAULT '',
    revision_floating   INTEGER NOT NULL DEFAULT 0,
    total_bytes         INTEGER NOT NULL DEFAULT 0,
    last_transition_at  INTEGER NOT NULL DEFAULT (strftime('%s','now')),
    last_reconcile_at   INTEGER NOT NULL DEFAULT 0,
    pulled_at           INTEGER NOT NULL DEFAULT 0,
    created_at          INTEGER NOT NULL DEFAULT (strftime('%s','now')),
    updated_at          INTEGER NOT NULL DEFAULT (strftime('%s','now'))
)

CREATE INDEX IF NOT EXISTS idx_local_models_state ON local_models(state)

CREATE INDEX IF NOT EXISTS idx_local_models_registry ON local_models(registry_id)

CREATE TABLE IF NOT EXISTS controller_models (
    uuid        TEXT    PRIMARY KEY,
    name        TEXT    NOT NULL DEFAULT '',
    repo        TEXT    NOT NULL DEFAULT '',
    revision    TEXT    NOT NULL DEFAULT '',
    registry_id INTEGER NOT NULL DEFAULT 0,
    files_json  TEXT    NOT NULL DEFAULT '[]',
    format      TEXT    NOT NULL DEFAULT '',
    updated_at  INTEGER NOT NULL DEFAULT (strftime('%s','now'))
)

CREATE UNIQUE INDEX IF NOT EXISTS idx_controller_models_name ON controller_models(name)

ALTER TABLE controller_microservices ADD COLUMN models TEXT NOT NULL DEFAULT '{}'

ALTER TABLE controller_microservices ADD COLUMN sysctls TEXT NOT NULL DEFAULT '{}'

ALTER TABLE controller_microservices ADD COLUMN ulimits TEXT NOT NULL DEFAULT '{}'

ALTER TABLE controller_microservices ADD COLUMN devices TEXT NOT NULL DEFAULT '[]'

ALTER TABLE controller_microservices ADD COLUMN tmpfs TEXT NOT NULL DEFAULT '[]'

ALTER TABLE controller_microservices ADD COLUMN entrypoint TEXT

ALTER TABLE controller_microservices ADD COLUMN commands TEXT

ALTER TABLE controller_microservices ADD COLUMN run_as_group TEXT

ALTER TABLE controller_microservices ADD COLUMN read_only_root_filesystem INTEGER NOT NULL DEFAULT 0

ALTER TABLE controller_microservices ADD COLUMN cpus REAL

ALTER TABLE controller_microservices ADD COLUMN memory_reservation INTEGER

ALTER TABLE controller_microservices ADD COLUMN memory_swap INTEGER

ALTER TABLE controller_microservices ADD COLUMN shm_size INTEGER

ALTER TABLE controller_microservices ADD COLUMN working_dir TEXT

CREATE TABLE IF NOT EXISTS model_refs (
    model_name TEXT NOT NULL,
    kind       TEXT NOT NULL DEFAULT '',
    ref_id     TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (model_name, kind, ref_id)
)

CREATE INDEX IF NOT EXISTS idx_model_refs_name ON model_refs(model_name)

-- fleet RuntimeClass snapshot from the controller
CREATE TABLE IF NOT EXISTS controller_runtime_classes (
    name       TEXT    PRIMARY KEY,
    handler    TEXT    NOT NULL,
    updated_at INTEGER NOT NULL DEFAULT (strftime('%s','now'))
)

-- applied RuntimeClass provenance: local operator vs controller fleet
ALTER TABLE local_runtime_classes ADD COLUMN source TEXT NOT NULL DEFAULT 'local'
