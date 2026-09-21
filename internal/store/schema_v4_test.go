package store

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestSchemaV3FixtureUpgradesToV4(t *testing.T) {
	dir := t.TempDir()
	writeStoreFixtureThrough(t, dir, 3)

	db := openStoreAt(t, dir)

	var maxVersion int
	if err := db.Conn().QueryRow(`SELECT COALESCE(MAX(version), 0) FROM schema_versions`).Scan(&maxVersion); err != nil {
		t.Fatalf("schema version: %v", err)
	}
	if maxVersion != 4 {
		t.Fatalf("expected schema version 4 after upgrade, got %d", maxVersion)
	}

	for _, table := range []string{"local_knowledge", "controller_knowledge", "knowledge_refs"} {
		if !tableExists(t, db, table) {
			t.Fatalf("missing knowledge table %q after schema v4", table)
		}
	}

	msCols := tableColumns(t, db, "controller_microservices")
	assertHasColumns(t, "controller_microservices", msCols, []string{"knowledge"})

	localCols := tableColumns(t, db, "local_knowledge")
	assertHasColumns(t, "local_knowledge", localCols, []string{
		"name", "generation", "source", "registry_id", "repo", "revision", "files_json",
		"format", "state", "resolved_revision", "digest", "revision_floating",
		"total_bytes", "last_error", "updated_at",
	})
	if localCols["name"].pk != 1 {
		t.Fatal("local_knowledge.name must be primary key")
	}

	ctrlCols := tableColumns(t, db, "controller_knowledge")
	assertHasColumns(t, "controller_knowledge", ctrlCols, []string{"uuid", "name"})
	if ctrlCols["uuid"].pk != 1 {
		t.Fatal("controller_knowledge.uuid must be primary key")
	}

	refCols := tableColumns(t, db, "knowledge_refs")
	assertHasColumns(t, "knowledge_refs", refCols, []string{"microservice_uuid", "knowledge_name"})
}

func TestSchemaV4_KnowledgeTablesExist(t *testing.T) {
	db := openFreshStoreDB(t)
	for _, table := range []string{"local_knowledge", "controller_knowledge", "knowledge_refs"} {
		if !tableExists(t, db, table) {
			t.Fatalf("missing knowledge table %q", table)
		}
	}
	msCols := tableColumns(t, db, "controller_microservices")
	assertHasColumns(t, "controller_microservices", msCols, []string{"knowledge", "models"})
}

func TestSchemaVersionIs4(t *testing.T) {
	entries, err := migrationFiles.ReadDir("migrations")
	if err != nil {
		t.Fatalf("read migrations: %v", err)
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		version, err := parseMigrationVersion(entry.Name())
		if err != nil {
			t.Fatalf("parse %s: %v", entry.Name(), err)
		}
		if version > 4 {
			t.Fatalf("schema version must stay at 4; found %s", entry.Name())
		}
	}

	db := openFreshStoreDB(t)
	var maxVersion int
	if err := db.Conn().QueryRow(`SELECT COALESCE(MAX(version), 0) FROM schema_versions`).Scan(&maxVersion); err != nil {
		t.Fatalf("schema version: %v", err)
	}
	if maxVersion != 4 {
		t.Fatalf("expected schema version 4, got %d", maxVersion)
	}

	var description string
	if err := db.Conn().QueryRow(
		`SELECT description FROM schema_versions WHERE version = 4`,
	).Scan(&description); err != nil {
		t.Fatalf("schema v4 row: %v", err)
	}
	if description != "004_edgelet_schema_v4.sql" {
		t.Fatalf("unexpected v4 migration description: %q", description)
	}

	if filepath.Base(description) != description {
		t.Fatalf("expected filename description, got %q", description)
	}
}
