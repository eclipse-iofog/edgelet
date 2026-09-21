package store

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/eclipse-iofog/edgelet/internal/models"
)

func writeStoreFixtureThrough(t *testing.T, dir string, maxVersion int) {
	t.Helper()
	path := filepath.Join(dir, dbFileName)
	raw, err := sql.Open("sqlite", path+"?_journal_mode=WAL&_synchronous=NORMAL&_foreign_keys=ON")
	if err != nil {
		t.Fatalf("open fixture sqlite: %v", err)
	}
	defer func() { _ = raw.Close() }()

	if _, err := raw.Exec(`CREATE TABLE IF NOT EXISTS schema_versions (
		version     INTEGER PRIMARY KEY,
		description TEXT    NOT NULL,
		applied_at  INTEGER NOT NULL DEFAULT (strftime('%s','now'))
	)`); err != nil {
		t.Fatalf("bootstrap schema_versions: %v", err)
	}

	files := []string{
		"001_edgelet_schema_v1.sql",
		"002_edgelet_schema_v2.sql",
		"003_edgelet_schema_v3.sql",
	}
	if maxVersion < 1 || maxVersion > len(files) {
		t.Fatalf("unsupported fixture version %d", maxVersion)
	}
	for i := 0; i < maxVersion; i++ {
		name := files[i]
		sqlBytes, err := migrationFiles.ReadFile("migrations/" + name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		for _, stmt := range splitStatements(string(sqlBytes)) {
			if _, err := raw.Exec(stmt); err != nil {
				t.Fatalf("apply %s: %v\nSQL: %s", name, err, stmt)
			}
		}
		if _, err := raw.Exec(
			`INSERT INTO schema_versions (version, description) VALUES (?, ?)`,
			i+1, name,
		); err != nil {
			t.Fatalf("record v%d: %v", i+1, err)
		}
	}
}

func writePrivateVolumeDir(t *testing.T, diskDir, uuid, name string) string {
	t.Helper()
	dir := filepath.Join(diskDir, "volumes", "data", uuid, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir volume data: %v", err)
	}
	marker := filepath.Join(dir, "keep.txt")
	if err := os.WriteFile(marker, []byte("keep"), 0o600); err != nil {
		t.Fatalf("write volume marker: %v", err)
	}
	return marker
}

func requireFileKept(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("expected volume files to remain at %s: %v", path, err)
	}
}

func openStoreAt(t *testing.T, dir string) *DB {
	t.Helper()
	db := &DB{}
	if err := db.Open(dir); err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestPersistentVolumeSchemaV2FixtureUpgradesToV3(t *testing.T) {
	dir := t.TempDir()
	writeStoreFixtureThrough(t, dir, 2)

	db := openStoreAt(t, dir)

	var maxVersion int
	if err := db.Conn().QueryRow(`SELECT COALESCE(MAX(version), 0) FROM schema_versions`).Scan(&maxVersion); err != nil {
		t.Fatalf("schema version: %v", err)
	}
	if maxVersion != 4 {
		t.Fatalf("expected schema version 4, got %d", maxVersion)
	}
	if !tableExists(t, db, "persistent_volumes") {
		t.Fatal("expected persistent_volumes table")
	}
	cols := tableColumns(t, db, "persistent_volumes")
	assertHasColumns(t, "persistent_volumes", cols, []string{
		"ms_uuid", "volume_name", "scope", "kind", "host_path",
		"created_at", "last_desired_at", "unreferenced_at",
	})
	if cols["ms_uuid"].pk != 1 || cols["volume_name"].pk != 2 {
		t.Fatalf("expected PK (ms_uuid, volume_name), got pk flags ms_uuid=%d volume_name=%d",
			cols["ms_uuid"].pk, cols["volume_name"].pk)
	}

	if _, err := db.Conn().Exec(
		`INSERT INTO persistent_volumes (ms_uuid, volume_name, kind) VALUES ('ms-default', 'data', 'workload')`,
	); err != nil {
		t.Fatalf("insert with scope default: %v", err)
	}
	var scope string
	if err := db.Conn().QueryRow(
		`SELECT scope FROM persistent_volumes WHERE ms_uuid = 'ms-default' AND volume_name = 'data'`,
	).Scan(&scope); err != nil {
		t.Fatalf("read default scope: %v", err)
	}
	if scope != models.VolumeScopePrivate {
		t.Fatalf("expected default scope private, got %q", scope)
	}
}

func TestPersistentVolumeSeedDesiredIsReferenced(t *testing.T) {
	dir := t.TempDir()
	db := openStoreAt(t, dir)
	uuid := "ms-desired"
	if err := db.SaveControllerMicroservices([]*models.Microservice{
		models.NewMicroservice(uuid, "nginx:latest"),
	}); err != nil {
		t.Fatalf("save desired microservice: %v", err)
	}
	writePrivateVolumeDir(t, dir, uuid, "data")
	if err := db.SeedPersistentVolumesFromDisk(dir, ""); err != nil {
		t.Fatalf("seed: %v", err)
	}

	rec, err := db.GetPersistentVolume(uuid, "data")
	if err != nil {
		t.Fatalf("get seeded volume: %v", err)
	}
	if rec.Kind != PersistentVolumeKindWorkload {
		t.Fatalf("kind=%q", rec.Kind)
	}
	if rec.Scope != models.VolumeScopePrivate {
		t.Fatalf("scope=%q", rec.Scope)
	}
	if rec.UnreferencedAt != nil {
		t.Fatalf("expected referenced consumer, unreferenced_at=%v", rec.UnreferencedAt)
	}
}

func TestPersistentVolumeSeedLeftoverKeepsFiles(t *testing.T) {
	dir := t.TempDir()
	marker := writePrivateVolumeDir(t, dir, "ms-leftover", "data")
	db := openStoreAt(t, dir)

	rec, err := db.GetPersistentVolume("ms-leftover", "data")
	if err != nil {
		t.Fatalf("get leftover volume: %v", err)
	}
	if rec.UnreferencedAt == nil {
		t.Fatal("expected leftover consumer to be unreferenced")
	}
	requireFileKept(t, marker)
}

func TestPersistentVolumeUpsertBumpsLastDesiredAt(t *testing.T) {
	db := openFreshStoreDB(t)
	if err := db.UpsertPersistentVolume("ms-1", "data", PersistentVolumeKindWorkload, models.VolumeScopePrivate, ""); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if _, err := db.Conn().Exec(
		`UPDATE persistent_volumes SET last_desired_at = 1 WHERE ms_uuid = 'ms-1' AND volume_name = 'data'`,
	); err != nil {
		t.Fatalf("age last_desired_at: %v", err)
	}
	if err := db.UpsertPersistentVolume("ms-1", "data", PersistentVolumeKindWorkload, models.VolumeScopePrivate, ""); err != nil {
		t.Fatalf("upsert again: %v", err)
	}
	rec, err := db.GetPersistentVolume("ms-1", "data")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if rec.LastDesiredAt <= 1 {
		t.Fatalf("expected last_desired_at to advance, got %d", rec.LastDesiredAt)
	}
	if rec.UnreferencedAt != nil {
		t.Fatalf("expected still referenced, unreferenced_at=%v", rec.UnreferencedAt)
	}
}

func TestPersistentVolumeMarkUnreferencedOnDesiredDeleteKeepsFiles(t *testing.T) {
	dir := t.TempDir()
	db := openStoreAt(t, dir)
	uuid := "ms-delete"
	marker := writePrivateVolumeDir(t, dir, uuid, "data")
	ms := models.NewMicroservice(uuid, "nginx:latest")
	ms.VolumeMappings = []*models.VolumeMapping{
		models.NewVolumeMapping("data", "/data", "rw", models.VolumeMappingTypeVolume),
	}
	if err := db.SaveControllerMicroservices([]*models.Microservice{ms}); err != nil {
		t.Fatalf("save: %v", err)
	}
	if err := db.SaveControllerMicroservices(nil); err != nil {
		t.Fatalf("delete from desired state: %v", err)
	}
	rec, err := db.GetPersistentVolume(uuid, "data")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if rec.UnreferencedAt == nil {
		t.Fatal("expected unreferenced_at after microservice delete")
	}
	requireFileKept(t, marker)
}

func TestPersistentVolumeDrainDoesNotMarkUnreferenced(t *testing.T) {
	dir := t.TempDir()
	db := openStoreAt(t, dir)
	uuid := "ms-drain"
	ms := models.NewMicroservice(uuid, "nginx:latest")
	ms.VolumeMappings = []*models.VolumeMapping{
		models.NewVolumeMapping("data", "/data", "rw", models.VolumeMappingTypeVolume),
	}
	if err := db.SaveControllerMicroservices([]*models.Microservice{ms}); err != nil {
		t.Fatalf("save: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if err := db.Open(dir); err != nil {
		t.Fatalf("reopen after drain: %v", err)
	}

	rec, err := db.GetPersistentVolume(uuid, "data")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if rec.UnreferencedAt != nil {
		t.Fatalf("drain must not stamp unreferenced_at, got %v", rec.UnreferencedAt)
	}
}

func TestPersistentVolumeKeepSetIncludesStoppedDesiredMicroservice(t *testing.T) {
	db := openFreshStoreDB(t)
	uuid := "ms-stopped"
	if err := db.SaveControllerMicroservices([]*models.Microservice{
		models.NewMicroservice(uuid, "nginx:latest"),
	}); err != nil {
		t.Fatalf("save: %v", err)
	}
	kept, err := db.PrivateVolumeUUIDKept(uuid, nil)
	if err != nil {
		t.Fatalf("keep-set: %v", err)
	}
	if !kept {
		t.Fatal("expected stopped desired-state microservice in keep-set with no listed container")
	}
}

func TestPersistentVolumeSeedControlPlaneIsPrivate(t *testing.T) {
	dir := t.TempDir()
	db := openStoreAt(t, dir)
	cpUUID := "cp-uuid-1"
	if err := db.UpsertSystemControlPlane(&models.ControlPlaneDeployment{
		ControllerUUID: cpUUID,
		Namespace:      "default",
		Name:           "pot",
		ManifestYAML:   "kind: ControlPlane",
		Image:          "controller:latest",
	}); err != nil {
		t.Fatalf("upsert control plane: %v", err)
	}
	writePrivateVolumeDir(t, dir, cpUUID, "iofog-controller-db")
	if err := db.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if err := db.Open(dir); err != nil {
		t.Fatalf("reopen: %v", err)
	}

	rec, err := db.GetPersistentVolume(cpUUID, "iofog-controller-db")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if rec.Kind != PersistentVolumeKindControlPlane {
		t.Fatalf("kind=%q want controlplane", rec.Kind)
	}
	if rec.Scope != models.VolumeScopePrivate {
		t.Fatalf("scope=%q want private", rec.Scope)
	}
}

func TestPersistentVolumeDeprovisionClearDoesNotStampUnreferenced(t *testing.T) {
	db := openFreshStoreDB(t)
	uuid := "ms-deprovision"
	ms := models.NewMicroservice(uuid, "nginx:latest")
	ms.VolumeMappings = []*models.VolumeMapping{
		models.NewVolumeMapping("data", "/data", "rw", models.VolumeMappingTypeVolume),
	}
	if err := db.SaveControllerMicroservices([]*models.Microservice{ms}); err != nil {
		t.Fatalf("save: %v", err)
	}
	if err := db.ClearControllerMicroservices(); err != nil {
		t.Fatalf("clear desired state: %v", err)
	}
	rec, err := db.GetPersistentVolume(uuid, "data")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if rec.UnreferencedAt != nil {
		t.Fatalf("deprovision table clear must not stamp unreferenced_at, got %v", rec.UnreferencedAt)
	}
}

func TestPersistentVolumeSharedConsumersShareHostPath(t *testing.T) {
	db := openFreshStoreDB(t)
	if err := db.UpsertPersistentVolume("local-uuid", "nodered-config", PersistentVolumeKindWorkload, models.VolumeScopeShared, ""); err != nil {
		t.Fatalf("upsert local: %v", err)
	}
	if err := db.UpsertPersistentVolume("controller-uuid", "nodered-config", PersistentVolumeKindWorkload, models.VolumeScopeShared, ""); err != nil {
		t.Fatalf("upsert controller: %v", err)
	}
	local, err := db.GetPersistentVolume("local-uuid", "nodered-config")
	if err != nil {
		t.Fatalf("get local: %v", err)
	}
	ctrl, err := db.GetPersistentVolume("controller-uuid", "nodered-config")
	if err != nil {
		t.Fatalf("get controller: %v", err)
	}
	wantSuffix := filepath.Join("volumes", "shared", "nodered-config")
	if !strings.HasSuffix(local.HostPath, wantSuffix) {
		t.Fatalf("local host_path=%q want suffix %q", local.HostPath, wantSuffix)
	}
	if local.HostPath != ctrl.HostPath {
		t.Fatalf("shared consumers must share host_path: local=%q controller=%q", local.HostPath, ctrl.HostPath)
	}
}

func TestPersistentVolumePrivateAndSharedConfigAreDifferentPaths(t *testing.T) {
	db := openFreshStoreDB(t)
	if err := db.UpsertPersistentVolume("ms-1", "config", PersistentVolumeKindWorkload, models.VolumeScopePrivate, ""); err != nil {
		t.Fatalf("upsert private: %v", err)
	}
	if err := db.UpsertPersistentVolume("ms-2", "config", PersistentVolumeKindWorkload, models.VolumeScopeShared, ""); err != nil {
		t.Fatalf("upsert shared: %v", err)
	}
	private, err := db.GetPersistentVolume("ms-1", "config")
	if err != nil {
		t.Fatalf("get private: %v", err)
	}
	shared, err := db.GetPersistentVolume("ms-2", "config")
	if err != nil {
		t.Fatalf("get shared: %v", err)
	}
	if private.HostPath == shared.HostPath {
		t.Fatalf("private and shared config must be different paths, both %q", private.HostPath)
	}
	if !strings.Contains(filepath.ToSlash(private.HostPath), "/volumes/data/ms-1/config") {
		t.Fatalf("private host_path=%q", private.HostPath)
	}
	if !strings.Contains(filepath.ToSlash(shared.HostPath), "/volumes/shared/config") {
		t.Fatalf("shared host_path=%q", shared.HostPath)
	}
}

func TestPersistentVolumeUnknownScopeStoresPrivate(t *testing.T) {
	db := openFreshStoreDB(t)
	for i, scope := range []string{"", "SHARED", "foo"} {
		name := "data"
		uuid := "ms-scope-" + string(rune('a'+i))
		if err := db.UpsertPersistentVolume(uuid, name, PersistentVolumeKindWorkload, scope, ""); err != nil {
			t.Fatalf("upsert scope %q: %v", scope, err)
		}
		rec, err := db.GetPersistentVolume(uuid, name)
		if err != nil {
			t.Fatalf("get scope %q: %v", scope, err)
		}
		if rec.Scope != models.VolumeScopePrivate {
			t.Fatalf("scope %q stored %q want private", scope, rec.Scope)
		}
	}
}

func TestPersistentVolumeBindScopeDoesNotCreateSharedClaim(t *testing.T) {
	db := openFreshStoreDB(t)
	if err := db.UpsertPersistentVolumesFromMappings("ms-bind", PersistentVolumeKindWorkload, []*models.VolumeMapping{
		{
			HostDestination:      "/var/lib/data",
			ContainerDestination: "/data",
			AccessMode:           "rw",
			Type:                 models.VolumeMappingTypeBind,
			Scope:                models.VolumeScopeShared,
		},
	}); err != nil {
		t.Fatalf("upsert mappings: %v", err)
	}
	rows, err := db.ListPersistentVolumes()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("BIND must not create a persistent volume claim, got %+v", rows)
	}
}

func TestPersistentVolumeSeedFromDataIsPrivateOnly(t *testing.T) {
	dir := t.TempDir()
	writePrivateVolumeDir(t, dir, "ms-seed", "data")
	if err := os.MkdirAll(filepath.Join(dir, "volumes", "shared", "from-disk"), 0o755); err != nil {
		t.Fatalf("mkdir shared: %v", err)
	}
	db := openStoreAt(t, dir)
	rows, err := db.ListPersistentVolumes()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected 1 seeded private row, got %+v", rows)
	}
	if rows[0].Scope != models.VolumeScopePrivate {
		t.Fatalf("seeded scope=%q", rows[0].Scope)
	}
	if rows[0].VolumeName != "data" {
		t.Fatalf("seeded name=%q", rows[0].VolumeName)
	}
}

func TestPersistentVolumeLocalManifestSharedUpsert(t *testing.T) {
	db := openFreshStoreDB(t)
	ms := &models.LocalDeployedMicroservice{
		LocalUUID:        "local-shared",
		ApplicationName:  "edgelet",
		MicroserviceName: "nodered",
		SourceName:       "local-apply",
		ImageName:        "nodered:latest",
		State:            "running",
		ManifestYAML: `apiVersion: edgelet.iofog.org/v1
kind: Microservice
metadata:
  name: nodered
spec:
  image: nodered:latest
  container:
    volumes:
      - hostDestination: nodered-config
        containerDestination: /data
        type: VOLUME
        scope: shared
`,
	}
	if err := db.UpsertLocalWorkload(ms); err != nil {
		t.Fatalf("upsert local workload: %v", err)
	}
	rec, err := db.GetPersistentVolume("local-shared", "nodered-config")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if rec.Scope != models.VolumeScopeShared {
		t.Fatalf("scope=%q", rec.Scope)
	}
	if !strings.HasSuffix(rec.HostPath, filepath.Join("volumes", "shared", "nodered-config")) {
		t.Fatalf("host_path=%q", rec.HostPath)
	}
}

func TestPersistentVolumeCleanupUUIDIsReservedOnly(t *testing.T) {
	dir := t.TempDir()
	db := openStoreAt(t, dir)
	if err := db.AddPersistentVolumeCleanupUUID("ms-cleanup"); err != nil {
		t.Fatalf("add cleanup: %v", err)
	}
	uuids, err := db.ListPersistentVolumeCleanupUUIDs()
	if err != nil {
		t.Fatalf("list cleanup: %v", err)
	}
	if len(uuids) != 1 || uuids[0] != "ms-cleanup" {
		t.Fatalf("unexpected cleanup list %v", uuids)
	}
}
