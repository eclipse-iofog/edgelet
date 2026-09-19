-- Ownership of persistent volume directories

CREATE TABLE IF NOT EXISTS persistent_volumes (
    ms_uuid          TEXT    NOT NULL,
    volume_name      TEXT    NOT NULL,
    scope            TEXT    NOT NULL DEFAULT 'private' CHECK(scope IN ('private','shared')),
    kind             TEXT    NOT NULL CHECK(kind IN ('workload','controlplane')),
    host_path        TEXT    NOT NULL DEFAULT '',
    created_at       INTEGER NOT NULL DEFAULT (strftime('%s','now')),
    last_desired_at  INTEGER NOT NULL DEFAULT (strftime('%s','now')),
    unreferenced_at  INTEGER,
    PRIMARY KEY (ms_uuid, volume_name)
)

CREATE INDEX IF NOT EXISTS idx_persistent_volumes_unreferenced ON persistent_volumes(unreferenced_at)
