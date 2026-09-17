package store

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/eclipse-iofog/edgelet/internal/models"
)

func TestSchemaV2_ModelTablesExist(t *testing.T) {
	db := openFreshStoreDB(t)
	for _, table := range []string{"local_models", "controller_models", "model_refs", "controller_runtime_classes"} {
		if !tableExists(t, db, table) {
			t.Fatalf("missing v2 table %q", table)
		}
	}

	rcCols := tableColumns(t, db, "controller_runtime_classes")
	assertHasColumns(t, "controller_runtime_classes", rcCols, []string{"name", "handler", "updated_at"})
	if rcCols["name"].pk != 1 {
		t.Fatal("controller_runtime_classes.name must be primary key")
	}
	localRCCols := tableColumns(t, db, "local_runtime_classes")
	assertHasColumns(t, "local_runtime_classes", localRCCols, []string{"source"})

	localCols := tableColumns(t, db, "local_models")
	assertHasColumns(t, "local_models", localCols, []string{
		"name", "source", "repo", "revision", "registry_id", "files_json", "format",
		"state", "generation", "observed_generation", "manifest_yaml",
		"manifest_path", "content_path",
	})

	ctrlModelCols := tableColumns(t, db, "controller_models")
	assertHasColumns(t, "controller_models", ctrlModelCols, []string{
		"uuid", "name", "repo", "revision", "registry_id", "files_json", "format",
	})
	if ctrlModelCols["uuid"].pk != 1 {
		t.Fatal("controller_models.uuid must be primary key")
	}
	assertAbsentColumns(t, "controller_models", ctrlModelCols, []string{"id"})

	msCols := tableColumns(t, db, "controller_microservices")
	assertHasColumns(t, "controller_microservices", msCols, []string{
		"models", "sysctls", "ulimits", "devices", "tmpfs",
		"entrypoint", "commands", "run_as_group", "read_only_root_filesystem",
		"cpus", "memory_reservation", "memory_swap", "shm_size", "working_dir",
	})
	assertAbsentColumns(t, "controller_microservices", msCols, []string{
		"models_json", "container_spec_json",
	})
	if localCols["name"].pk != 1 {
		t.Fatal("local_models.name must be primary key")
	}

	refCols := tableColumns(t, db, "model_refs")
	assertHasColumns(t, "model_refs", refCols, []string{"model_name", "kind", "ref_id"})

	regCols := tableColumns(t, db, "local_registries")
	assertHasColumns(t, "local_registries", regCols, []string{"type", "ca_b64", "insecure"})
	ctrlCols := tableColumns(t, db, "controller_registries")
	assertHasColumns(t, "controller_registries", ctrlCols, []string{"type", "ca_b64", "insecure"})
}

func TestMigration002_UpgradeFromV1Fixture(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, dbFileName)
	raw, err := sql.Open("sqlite", path+"?_journal_mode=WAL&_synchronous=NORMAL&_foreign_keys=ON")
	if err != nil {
		t.Fatalf("open fixture sqlite: %v", err)
	}

	if _, err := raw.Exec(`CREATE TABLE IF NOT EXISTS schema_versions (
		version     INTEGER PRIMARY KEY,
		description TEXT    NOT NULL,
		applied_at  INTEGER NOT NULL DEFAULT (strftime('%s','now'))
	)`); err != nil {
		t.Fatalf("bootstrap schema_versions: %v", err)
	}

	sqlBytes, err := migrationFiles.ReadFile("migrations/001_edgelet_schema_v1.sql")
	if err != nil {
		t.Fatalf("read 001 sql: %v", err)
	}
	for _, stmt := range splitStatements(string(sqlBytes)) {
		if _, err := raw.Exec(stmt); err != nil {
			t.Fatalf("apply v1 statement: %v\nSQL: %s", err, stmt)
		}
	}
	if _, err := raw.Exec(
		`INSERT INTO schema_versions (version, description) VALUES (1, '001_edgelet_schema_v1.sql')`,
	); err != nil {
		t.Fatalf("record v1: %v", err)
	}
	if _, err := raw.Exec(
		`INSERT INTO local_registries (id, url, is_public, user_name, password, user_email, updated_at)
		 VALUES (5, 'quay.io', 0, 'alice', 'secret', 'alice@example.com', 1)`,
	); err != nil {
		t.Fatalf("insert v1 local registry: %v", err)
	}
	if _, err := raw.Exec(
		`INSERT INTO controller_registries (id, url, is_public, user_name, password, user_email, updated_at)
		 VALUES (7, 'ghcr.io', 1, '', '', '', 1)`,
	); err != nil {
		t.Fatalf("insert v1 controller registry: %v", err)
	}
	if _, err := raw.Exec(
		`INSERT INTO controller_microservices (uuid, image_name) VALUES ('ms-fixture', 'nginx:latest')`,
	); err != nil {
		t.Fatalf("insert v1 controller microservice: %v", err)
	}
	if err := raw.Close(); err != nil {
		t.Fatalf("close fixture: %v", err)
	}

	db := &DB{}
	if err := db.Open(dir); err != nil {
		t.Fatalf("open upgraded store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	var maxVersion int
	if err := db.Conn().QueryRow(`SELECT COALESCE(MAX(version), 0) FROM schema_versions`).Scan(&maxVersion); err != nil {
		t.Fatalf("schema version: %v", err)
	}
	if maxVersion != 2 {
		t.Fatalf("expected schema version 2 after upgrade, got %d", maxVersion)
	}

	got, err := db.GetLocalRegistry(5)
	if err != nil {
		t.Fatalf("get upgraded local registry: %v", err)
	}
	if got.URL != "quay.io" || got.UserName != "alice" || got.Password != "secret" {
		t.Fatalf("v1 registry identity lost: %+v", got)
	}
	if got.Type != models.RegistryTypeOCI || got.CAB64 != "" || got.Insecure {
		t.Fatalf("expected type/ca/insecure defaults, got %+v", got)
	}

	ctrl, err := db.LoadControllerRegistries()
	if err != nil {
		t.Fatalf("load controller registries: %v", err)
	}
	if len(ctrl) != 1 || ctrl[0].ID != 7 || ctrl[0].Type != models.RegistryTypeOCI || ctrl[0].Insecure {
		t.Fatalf("unexpected controller registry after upgrade: %+v", ctrl)
	}

	if !tableExists(t, db, "local_models") || !tableExists(t, db, "controller_models") {
		t.Fatal("expected model tables after 002")
	}
	if !tableExists(t, db, "controller_runtime_classes") {
		t.Fatal("expected controller_runtime_classes after 002")
	}
	localRCCols := tableColumns(t, db, "local_runtime_classes")
	assertHasColumns(t, "local_runtime_classes", localRCCols, []string{"source"})

	localCols := tableColumns(t, db, "local_models")
	assertHasColumns(t, "local_models", localCols, []string{"source"})
	ctrlModelCols := tableColumns(t, db, "controller_models")
	assertHasColumns(t, "controller_models", ctrlModelCols, []string{"uuid"})
	msCols := tableColumns(t, db, "controller_microservices")
	assertHasColumns(t, "controller_microservices", msCols, []string{
		"models", "sysctls", "ulimits", "devices", "tmpfs",
		"entrypoint", "commands", "run_as_group", "read_only_root_filesystem",
		"cpus", "memory_reservation", "memory_swap", "shm_size", "working_dir",
	})
	assertAbsentColumns(t, "controller_microservices", msCols, []string{
		"models_json", "container_spec_json",
	})

	var (
		modelsJSON, sysctlsJSON, devicesJSON string
		readOnly                             int
		entrypoint                           sql.NullString
		cpus                                 sql.NullFloat64
	)
	if err := db.Conn().QueryRow(
		`SELECT models, sysctls, devices, read_only_root_filesystem, entrypoint, cpus
		 FROM controller_microservices WHERE uuid = ?`,
		"ms-fixture",
	).Scan(&modelsJSON, &sysctlsJSON, &devicesJSON, &readOnly, &entrypoint, &cpus); err != nil {
		t.Fatalf("upgraded microservice columns: %v", err)
	}
	if modelsJSON != "{}" || sysctlsJSON != "{}" || devicesJSON != "[]" || readOnly != 0 {
		t.Fatalf("expected column defaults, got models=%q sysctls=%q devices=%q ro=%d", modelsJSON, sysctlsJSON, devicesJSON, readOnly)
	}
	if entrypoint.Valid || cpus.Valid {
		t.Fatalf("expected omitted entrypoint/cpus, got entrypoint=%v cpus=%v", entrypoint, cpus)
	}

	if tableExists(t, db, "schema_v3_placeholder") {
		t.Fatal("unexpected extra schema table")
	}
}

func TestSchemaVersionStaysAt2(t *testing.T) {
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
		if version > 2 {
			t.Fatalf("schema version must stay at 2; found %s", entry.Name())
		}
	}

	db := openFreshStoreDB(t)
	var maxVersion int
	if err := db.Conn().QueryRow(`SELECT COALESCE(MAX(version), 0) FROM schema_versions`).Scan(&maxVersion); err != nil {
		t.Fatalf("schema version: %v", err)
	}
	if maxVersion != 2 {
		t.Fatalf("expected schema version 2, got %d", maxVersion)
	}
}
