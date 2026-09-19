package fieldagent

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/eclipse-iofog/edgelet/internal/config"
	"github.com/eclipse-iofog/edgelet/internal/models"
	"github.com/eclipse-iofog/edgelet/internal/store"
	"github.com/eclipse-iofog/edgelet/internal/volumereclaim"
)

func openDeprovisionVolumeDB(t *testing.T) (string, *store.DB, *FieldAgent) {
	t.Helper()
	dir := t.TempDir()
	db := store.GetInstance()
	_ = db.Close()
	if err := db.Open(dir); err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	cfg := config.GetInstance()
	original := cfg.DiskDirectory
	cfg.DiskDirectory = dir
	t.Cleanup(func() { cfg.DiskDirectory = original })

	fa := GetInstance()
	fa.config = cfg
	return dir, db, fa
}

func writeVolumeMarker(t *testing.T, path string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte("keep"), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	return path
}

func requireMarkerKept(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("expected %s to remain: %v", path, err)
	}
}

func requireMarkerGone(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); err == nil {
		t.Fatalf("expected %s to be deleted", path)
	} else if !os.IsNotExist(err) {
		t.Fatalf("stat %s: %v", path, err)
	}
}

func upsertLocalWorkload(t *testing.T, db *store.DB, uuid string) {
	t.Helper()
	local := &models.LocalDeployedMicroservice{
		LocalUUID:        uuid,
		ApplicationName:  "edgelet",
		MicroserviceName: "app",
		SourceName:       "local-cli",
		ManifestYAML:     "kind: Microservice",
		ImageName:        "nginx:latest",
		State:            "running",
	}
	if err := db.UpsertLocalWorkload(local); err != nil {
		t.Fatalf("upsert local workload: %v", err)
	}
}

func TestDeprovisionAllScopePreservesPersistentVolumesWithoutOrphanClock(t *testing.T) {
	dir, db, fa := openDeprovisionVolumeDB(t)
	uuid := "ms-all"
	sharedName := "n"

	dataMarker := writeVolumeMarker(t, filepath.Join(dir, "volumes", "data", uuid, "x"))
	sharedMarker := writeVolumeMarker(t, filepath.Join(dir, "volumes", "shared", sharedName, "x"))

	ms := models.NewMicroservice(uuid, "nginx:latest")
	ms.VolumeMappings = []*models.VolumeMapping{
		models.NewVolumeMapping("x", "/data", "rw", models.VolumeMappingTypeVolume),
		{
			HostDestination:      sharedName,
			ContainerDestination: "/shared",
			AccessMode:           "rw",
			Type:                 models.VolumeMappingTypeVolume,
			Scope:                models.VolumeScopeShared,
		},
	}
	if err := db.SaveControllerMicroservices([]*models.Microservice{ms}); err != nil {
		t.Fatalf("save controller ms: %v", err)
	}

	fa.clearSQLiteCacheTablesOnDeprovision(false)

	requireMarkerKept(t, dataMarker)
	requireMarkerKept(t, sharedMarker)

	private, err := db.GetPersistentVolume(uuid, "x")
	if err != nil {
		t.Fatalf("get private: %v", err)
	}
	if private.UnreferencedAt != nil {
		t.Fatalf("deprovision must not start orphan clock, unreferenced_at=%v", private.UnreferencedAt)
	}
	shared, err := db.GetPersistentVolume(uuid, sharedName)
	if err != nil {
		t.Fatalf("get shared: %v", err)
	}
	if shared.UnreferencedAt != nil {
		t.Fatalf("deprovision must not start orphan clock on shared claim, unreferenced_at=%v", shared.UnreferencedAt)
	}

	orphans, err := db.ListUnreferencedWorkloadVolumes()
	if err != nil {
		t.Fatalf("list orphans: %v", err)
	}
	if len(orphans) != 0 {
		t.Fatalf("deprovision-only rows must not be orphan-prune candidates, got %d", len(orphans))
	}
}

func TestDeprovisionLocalScopePreservesPersistentVolumes(t *testing.T) {
	dir, db, fa := openDeprovisionVolumeDB(t)
	ctrlUUID := "ms-controller"
	localUUID := "ms-local"

	dataMarker := writeVolumeMarker(t, filepath.Join(dir, "volumes", "data", ctrlUUID, "x"))
	sharedMarker := writeVolumeMarker(t, filepath.Join(dir, "volumes", "shared", "n", "x"))
	localMarker := writeVolumeMarker(t, filepath.Join(dir, "volumes", "data", localUUID, "x"))

	ms := models.NewMicroservice(ctrlUUID, "nginx:latest")
	ms.VolumeMappings = []*models.VolumeMapping{
		models.NewVolumeMapping("x", "/data", "rw", models.VolumeMappingTypeVolume),
	}
	if err := db.SaveControllerMicroservices([]*models.Microservice{ms}); err != nil {
		t.Fatalf("save controller ms: %v", err)
	}
	upsertLocalWorkload(t, db, localUUID)
	if err := db.UpsertPersistentVolume(localUUID, "x", store.PersistentVolumeKindWorkload, models.VolumeScopePrivate, ""); err != nil {
		t.Fatalf("upsert local private: %v", err)
	}
	if err := db.UpsertPersistentVolume(localUUID, "n", store.PersistentVolumeKindWorkload, models.VolumeScopeShared, ""); err != nil {
		t.Fatalf("upsert local shared: %v", err)
	}

	fa.clearSQLiteCacheTablesOnDeprovision(true)

	requireMarkerKept(t, dataMarker)
	requireMarkerKept(t, sharedMarker)
	requireMarkerKept(t, localMarker)

	locals, err := db.ListLocalWorkloads()
	if err != nil {
		t.Fatalf("list local: %v", err)
	}
	if len(locals) != 1 {
		t.Fatalf("expected local workload preserved, got %d", len(locals))
	}
}

func TestDeprovisionPurgeDestroysWorkloadNotControlPlane(t *testing.T) {
	dir, db, fa := openDeprovisionVolumeDB(t)
	msUUID := "ms-purge"
	cpUUID := "cp-uuid"
	sharedName := "only-ms"

	privateMarker := writeVolumeMarker(t, filepath.Join(dir, "volumes", "data", msUUID, "data", "keep.txt"))
	sharedMarker := writeVolumeMarker(t, filepath.Join(dir, "volumes", "shared", sharedName, "keep.txt"))
	cpMarker := writeVolumeMarker(t, filepath.Join(dir, "volumes", "data", cpUUID, "iofog-controller-db", "keep.txt"))

	if err := db.UpsertPersistentVolume(msUUID, "data", store.PersistentVolumeKindWorkload, models.VolumeScopePrivate, ""); err != nil {
		t.Fatalf("upsert private: %v", err)
	}
	if err := db.UpsertPersistentVolume(msUUID, sharedName, store.PersistentVolumeKindWorkload, models.VolumeScopeShared, ""); err != nil {
		t.Fatalf("upsert shared: %v", err)
	}
	if err := db.UpsertPersistentVolume(cpUUID, "iofog-controller-db", store.PersistentVolumeKindControlPlane, models.VolumeScopePrivate, ""); err != nil {
		t.Fatalf("upsert control plane: %v", err)
	}

	fa.clearSQLiteCacheTablesOnDeprovision(false)
	fa.purgePersistentVolumesOnDeprovision(false)

	requireMarkerGone(t, privateMarker)
	requireMarkerGone(t, sharedMarker)
	requireMarkerKept(t, cpMarker)
}

func TestDeleteNodeDeprovisionDoesNotPurgeVolumes(t *testing.T) {
	var (
		called bool
		purge  bool
		scope  string
	)
	fa := &FieldAgent{
		ctx: context.Background(),
		deprovisionOptsFn: func(clearCredentials bool, gotScope string, purgeVolumes bool) error {
			called = true
			scope = gotScope
			purge = purgeVolumes
			if !clearCredentials {
				t.Fatal("delete-node must skip the controller deprovision request")
			}
			return nil
		},
	}
	if err := fa.deleteNode(); err == nil {
		t.Fatal("expected delete-node to return api client error")
	}
	if !called {
		t.Fatal("expected delete-node to deprovision")
	}
	if purge {
		t.Fatal("delete-node must not purge persistent volumes")
	}
	if scope != DeprovisionScopeAll {
		t.Fatalf("delete-node scope=%q want all", scope)
	}
}

func TestAutomaticDeprovisionPathsDoNotPurgeVolumes(t *testing.T) {
	var lastPurge bool
	fa := &FieldAgent{
		deprovisionOptsFn: func(clearCredentials bool, scope string, purgeVolumes bool) error {
			lastPurge = purgeVolumes
			return nil
		},
	}
	if err := fa.Deprovision(true); err != nil {
		t.Fatalf("Deprovision: %v", err)
	}
	if lastPurge {
		t.Fatal("Deprovision must not purge persistent volumes")
	}
	if err := fa.DeprovisionWithScope(false, DeprovisionScopeAll); err != nil {
		t.Fatalf("DeprovisionWithScope: %v", err)
	}
	if lastPurge {
		t.Fatal("DeprovisionWithScope must not purge persistent volumes")
	}
	if err := fa.invokeDeprovision(true); err != nil {
		t.Fatalf("invokeDeprovision: %v", err)
	}
	if lastPurge {
		t.Fatal("status-auth deprovision must not purge persistent volumes")
	}
}

func TestDeprovisionPreserveRemountsSamePrivateHostPath(t *testing.T) {
	dir, db, fa := openDeprovisionVolumeDB(t)
	uuid := "ms-remount"
	if err := db.UpsertPersistentVolume(uuid, "data", store.PersistentVolumeKindWorkload, models.VolumeScopePrivate, ""); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	before, err := db.GetPersistentVolume(uuid, "data")
	if err != nil {
		t.Fatalf("get before: %v", err)
	}
	want := store.PersistentVolumeHostPath(dir, uuid, "data", models.VolumeScopePrivate)
	if before.HostPath != want {
		t.Fatalf("host_path=%q want %q", before.HostPath, want)
	}

	fa.clearSQLiteCacheTablesOnDeprovision(false)

	after, err := db.GetPersistentVolume(uuid, "data")
	if err != nil {
		t.Fatalf("get after: %v", err)
	}
	if after.HostPath != before.HostPath {
		t.Fatalf("host_path changed after preserve: before=%q after=%q", before.HostPath, after.HostPath)
	}
}

func TestDeprovisionLocalScopePurgeLeavesSharedForRemainingLocalConsumer(t *testing.T) {
	dir, db, fa := openDeprovisionVolumeDB(t)
	ctrlUUID := "ms-controller"
	localUUID := "ms-local"
	name := "n"

	sharedMarker := writeVolumeMarker(t, filepath.Join(dir, "volumes", "shared", name, "keep.txt"))
	if err := db.UpsertPersistentVolume(ctrlUUID, name, store.PersistentVolumeKindWorkload, models.VolumeScopeShared, ""); err != nil {
		t.Fatalf("upsert controller shared: %v", err)
	}
	upsertLocalWorkload(t, db, localUUID)
	if err := db.UpsertPersistentVolume(localUUID, name, store.PersistentVolumeKindWorkload, models.VolumeScopeShared, ""); err != nil {
		t.Fatalf("upsert local shared: %v", err)
	}

	fa.clearSQLiteCacheTablesOnDeprovision(true)
	fa.purgePersistentVolumesOnDeprovision(true)

	requireMarkerKept(t, sharedMarker)
	if _, err := db.GetPersistentVolume(localUUID, name); err != nil {
		t.Fatalf("local shared consumer must remain: %v", err)
	}
}

func TestDeprovisionLocalPurgeLeavesSharedForRemainingControllerConsumer(t *testing.T) {
	dir, db, _ := openDeprovisionVolumeDB(t)
	ctrlUUID := "ms-controller"
	localUUID := "ms-local"
	name := "n"

	sharedMarker := writeVolumeMarker(t, filepath.Join(dir, "volumes", "shared", name, "keep.txt"))
	if err := db.UpsertPersistentVolume(ctrlUUID, name, store.PersistentVolumeKindWorkload, models.VolumeScopeShared, ""); err != nil {
		t.Fatalf("upsert controller shared: %v", err)
	}
	if err := db.UpsertPersistentVolume(localUUID, name, store.PersistentVolumeKindWorkload, models.VolumeScopeShared, ""); err != nil {
		t.Fatalf("upsert local shared: %v", err)
	}

	if err := volumereclaim.New(db, dir).PurgeWorkloads([]string{localUUID}); err != nil {
		t.Fatalf("purge local consumers: %v", err)
	}
	requireMarkerKept(t, sharedMarker)
	if _, err := db.GetPersistentVolume(ctrlUUID, name); err != nil {
		t.Fatalf("controller shared consumer must remain: %v", err)
	}
}

func TestDeprovisionAllScopePurgeRemovesWorkloadSharedClaims(t *testing.T) {
	dir, db, fa := openDeprovisionVolumeDB(t)
	localUUID := "ms-local"
	ctrlUUID := "ms-controller"
	name := "n"

	sharedMarker := writeVolumeMarker(t, filepath.Join(dir, "volumes", "shared", name, "keep.txt"))
	cpMarker := writeVolumeMarker(t, filepath.Join(dir, "volumes", "data", "cp-uuid", "iofog-controller-db", "keep.txt"))

	upsertLocalWorkload(t, db, localUUID)
	if err := db.UpsertPersistentVolume(localUUID, name, store.PersistentVolumeKindWorkload, models.VolumeScopeShared, ""); err != nil {
		t.Fatalf("upsert local shared: %v", err)
	}
	if err := db.UpsertPersistentVolume(ctrlUUID, name, store.PersistentVolumeKindWorkload, models.VolumeScopeShared, ""); err != nil {
		t.Fatalf("upsert controller shared: %v", err)
	}
	if err := db.UpsertPersistentVolume("cp-uuid", "iofog-controller-db", store.PersistentVolumeKindControlPlane, models.VolumeScopePrivate, ""); err != nil {
		t.Fatalf("upsert control plane: %v", err)
	}

	fa.clearSQLiteCacheTablesOnDeprovision(false)
	fa.purgePersistentVolumesOnDeprovision(false)

	requireMarkerGone(t, sharedMarker)
	requireMarkerKept(t, cpMarker)
}
