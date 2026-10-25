package store

import (
	"database/sql"
	"path/filepath"
	"slices"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/eclipse-iofog/edgelet/internal/models"
)

func TestFreshStoreSchemaVersionIs5(t *testing.T) {
	db := openFreshStoreDB(t)

	var maxVersion int
	if err := db.Conn().QueryRow(`SELECT COALESCE(MAX(version), 0) FROM schema_versions`).Scan(&maxVersion); err != nil {
		t.Fatalf("schema version: %v", err)
	}
	if maxVersion != 5 {
		t.Fatalf("expected schema version 5, got %d", maxVersion)
	}
	cols := tableColumns(t, db, "controller_microservices")
	assertHasColumns(t, "controller_microservices", cols, []string{"supplemental_groups"})
}

func TestSchemaV4DatabaseGainsSupplementalGroups(t *testing.T) {
	dir := t.TempDir()
	writeStoreFixtureThrough(t, dir, 4)

	path := filepath.Join(dir, dbFileName)
	rawDB, err := sql.Open("sqlite", path+"?_journal_mode=WAL&_synchronous=NORMAL&_foreign_keys=ON")
	if err != nil {
		t.Fatalf("open v4 fixture: %v", err)
	}
	if _, err := rawDB.Exec(
		`INSERT INTO controller_microservices (uuid, image_name) VALUES ('ms-v4', 'nginx:latest')`,
	); err != nil {
		t.Fatalf("insert v4 microservice: %v", err)
	}
	if err := rawDB.Close(); err != nil {
		t.Fatalf("close v4 fixture: %v", err)
	}

	db := openStoreAt(t, dir)

	var maxVersion int
	if err := db.Conn().QueryRow(`SELECT COALESCE(MAX(version), 0) FROM schema_versions`).Scan(&maxVersion); err != nil {
		t.Fatalf("schema version: %v", err)
	}
	if maxVersion != 5 {
		t.Fatalf("expected schema version 5 after upgrade, got %d", maxVersion)
	}

	var groupsJSON string
	if err := db.Conn().QueryRow(
		`SELECT supplemental_groups FROM controller_microservices WHERE uuid = ?`,
		"ms-v4",
	).Scan(&groupsJSON); err != nil {
		t.Fatalf("supplemental_groups column: %v", err)
	}
	if groupsJSON != "[]" {
		t.Fatalf("expected default [], got %q", groupsJSON)
	}

	loaded, err := db.LoadControllerMicroservices()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(loaded) != 1 || len(loaded[0].SupplementalGroups) != 0 {
		t.Fatalf("loaded groups=%v", loaded)
	}
}

func TestControllerMicroserviceSupplementalGroupsRoundTrip(t *testing.T) {
	db := openFreshStoreDB(t)

	withGroups := models.NewMicroservice("ms-groups", "nginx:latest")
	withGroups.SupplementalGroups = []int64{993, 44}
	withoutGroups := models.NewMicroservice("ms-empty", "nginx:latest")
	if err := db.SaveControllerMicroservices([]*models.Microservice{withGroups, withoutGroups}); err != nil {
		t.Fatalf("save: %v", err)
	}

	loaded, err := db.LoadControllerMicroservices()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	byUUID := map[string]*models.Microservice{}
	for _, ms := range loaded {
		byUUID[ms.MicroserviceUUID] = ms
	}
	got := byUUID["ms-groups"]
	if got == nil || !slices.Equal(got.SupplementalGroups, []int64{44, 993}) {
		t.Fatalf("round-trip groups=%v", got)
	}
	empty := byUUID["ms-empty"]
	if empty == nil || len(empty.SupplementalGroups) != 0 {
		t.Fatalf("empty groups=%v", empty)
	}

	var stored, emptyStored string
	if err := db.Conn().QueryRow(
		`SELECT supplemental_groups FROM controller_microservices WHERE uuid = ?`,
		"ms-groups",
	).Scan(&stored); err != nil {
		t.Fatalf("stored groups: %v", err)
	}
	if stored != "[44,993]" {
		t.Fatalf("stored JSON=%q", stored)
	}
	if err := db.Conn().QueryRow(
		`SELECT supplemental_groups FROM controller_microservices WHERE uuid = ?`,
		"ms-empty",
	).Scan(&emptyStored); err != nil {
		t.Fatalf("empty stored groups: %v", err)
	}
	if emptyStored != "[]" {
		t.Fatalf("empty stored JSON=%q", emptyStored)
	}

	if _, err := db.Conn().Exec(
		`INSERT INTO controller_microservices (uuid, image_name) VALUES ('ms-default', 'alpine:3')`,
	); err != nil {
		t.Fatalf("insert default row: %v", err)
	}
	var defaultStored string
	if err := db.Conn().QueryRow(
		`SELECT supplemental_groups FROM controller_microservices WHERE uuid = ?`,
		"ms-default",
	).Scan(&defaultStored); err != nil {
		t.Fatalf("default column: %v", err)
	}
	if defaultStored != "[]" {
		t.Fatalf("default column=%q", defaultStored)
	}
}
