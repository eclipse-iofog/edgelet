-- Ownership of knowledge artifacts and microservice knowledge catalog

CREATE TABLE IF NOT EXISTS local_knowledge (
    name              TEXT    NOT NULL PRIMARY KEY,
    generation        INTEGER NOT NULL DEFAULT 1,
    source            TEXT    NOT NULL DEFAULT 'local' CHECK(source IN ('local','managed')),
    registry_id       INTEGER NOT NULL,
    repo              TEXT    NOT NULL,
    revision          TEXT    NOT NULL DEFAULT '',
    files_json        TEXT    NOT NULL DEFAULT '[]',
    format            TEXT    NOT NULL DEFAULT '',
    state             TEXT    NOT NULL DEFAULT 'Pending',
    resolved_revision TEXT    NOT NULL DEFAULT '',
    digest            TEXT    NOT NULL DEFAULT '',
    revision_floating INTEGER NOT NULL DEFAULT 0,
    total_bytes       INTEGER NOT NULL DEFAULT 0,
    last_error        TEXT    NOT NULL DEFAULT '',
    updated_at        INTEGER NOT NULL DEFAULT (strftime('%s','now'))
)

CREATE TABLE IF NOT EXISTS controller_knowledge (
    uuid              TEXT    NOT NULL PRIMARY KEY,
    name              TEXT    NOT NULL UNIQUE,
    registry_id       INTEGER NOT NULL,
    repo              TEXT    NOT NULL,
    revision          TEXT    NOT NULL DEFAULT '',
    files_json        TEXT    NOT NULL DEFAULT '[]',
    format            TEXT    NOT NULL DEFAULT '',
    state             TEXT    NOT NULL DEFAULT 'Pending',
    resolved_revision TEXT    NOT NULL DEFAULT '',
    digest            TEXT    NOT NULL DEFAULT '',
    revision_floating INTEGER NOT NULL DEFAULT 0,
    total_bytes       INTEGER NOT NULL DEFAULT 0,
    last_error        TEXT    NOT NULL DEFAULT '',
    updated_at        INTEGER NOT NULL DEFAULT (strftime('%s','now'))
)

CREATE TABLE IF NOT EXISTS knowledge_refs (
    microservice_uuid TEXT NOT NULL,
    knowledge_name    TEXT NOT NULL,
    PRIMARY KEY (microservice_uuid, knowledge_name)
)

ALTER TABLE controller_microservices ADD COLUMN knowledge TEXT NOT NULL DEFAULT '{}'
