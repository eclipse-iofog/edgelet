package volumereclaim

import (
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/eclipse-iofog/edgelet/internal/models"
	"github.com/eclipse-iofog/edgelet/internal/runtimeops"
	"github.com/eclipse-iofog/edgelet/internal/store"
)

type staticUsage struct {
	private []string
	shared  []string
	mounted map[string]bool
}

func (s staticUsage) ListedPrivateUUIDs() []string { return s.private }
func (s staticUsage) ListedSharedNames() []string  { return s.shared }
func (s staticUsage) PathMounted(hostPath string) bool {
	if s.mounted == nil {
		return false
	}
	return s.mounted[filepath.Clean(hostPath)]
}

func openReclaimDB(t *testing.T) (string, *store.DB) {
	t.Helper()
	dir := t.TempDir()
	db := &store.DB{}
	if err := db.Open(dir); err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return dir, db
}

func newReclaimer(t *testing.T) (string, *store.DB, *Reclaimer) {
	t.Helper()
	dir, db := openReclaimDB(t)
	return dir, db, New(db, dir)
}

func writePrivateMarker(t *testing.T, disk, uuid, name string) string {
	t.Helper()
	dir := filepath.Join(disk, "volumes", "data", uuid, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir private volume: %v", err)
	}
	marker := filepath.Join(dir, "keep.txt")
	if err := os.WriteFile(marker, []byte("keep"), 0o600); err != nil {
		t.Fatalf("write private marker: %v", err)
	}
	return marker
}

func writeSharedMarker(t *testing.T, disk, name string) string {
	t.Helper()
	dir := filepath.Join(disk, "volumes", "shared", name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir shared volume: %v", err)
	}
	marker := filepath.Join(dir, "keep.txt")
	if err := os.WriteFile(marker, []byte("keep"), 0o600); err != nil {
		t.Fatalf("write shared marker: %v", err)
	}
	return marker
}

func requireFileKept(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("expected files to remain at %s: %v", path, err)
	}
}

func requireGone(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); err == nil {
		t.Fatalf("expected %s to be deleted", path)
	} else if !os.IsNotExist(err) {
		t.Fatalf("stat %s: %v", path, err)
	}
}

func requireRowGone(t *testing.T, db *store.DB, uuid, name string) {
	t.Helper()
	_, err := db.GetPersistentVolume(uuid, name)
	if !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("expected missing ledger row %s/%s, err=%v", uuid, name, err)
	}
}

func upsertPrivate(t *testing.T, db *store.DB, uuid, name, kind string) {
	t.Helper()
	if err := db.UpsertPersistentVolume(uuid, name, kind, models.VolumeScopePrivate, ""); err != nil {
		t.Fatalf("upsert private: %v", err)
	}
}

func upsertShared(t *testing.T, db *store.DB, uuid, name string) {
	t.Helper()
	if err := db.UpsertPersistentVolume(uuid, name, store.PersistentVolumeKindWorkload, models.VolumeScopeShared, ""); err != nil {
		t.Fatalf("upsert shared: %v", err)
	}
}

func saveDesired(t *testing.T, db *store.DB, uuid string, mappings []*models.VolumeMapping) {
	t.Helper()
	ms := models.NewMicroservice(uuid, "nginx:latest")
	ms.VolumeMappings = mappings
	if err := db.SaveControllerMicroservices([]*models.Microservice{ms}); err != nil {
		t.Fatalf("save desired: %v", err)
	}
}

func captureEvents(t *testing.T) *[]runtimeops.RuntimeEvent {
	t.Helper()
	events := make([]runtimeops.RuntimeEvent, 0)
	runtimeops.SetTestSink(func(e runtimeops.RuntimeEvent) {
		events = append(events, e)
	})
	t.Cleanup(func() { runtimeops.SetTestSink(nil) })
	return &events
}

func ageUnreferenced(t *testing.T, db *store.DB, uuid, name string, at time.Time) {
	t.Helper()
	if _, err := db.Conn().Exec(
		`UPDATE persistent_volumes SET unreferenced_at = ? WHERE ms_uuid = ? AND volume_name = ?`,
		at.Unix(), uuid, name,
	); err != nil {
		t.Fatalf("age unreferenced_at: %v", err)
	}
}

func upsertLocalShared(t *testing.T, db *store.DB, uuid, name string) {
	t.Helper()
	ms := &models.LocalDeployedMicroservice{
		LocalUUID:        uuid,
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
      - hostDestination: ` + name + `
        containerDestination: /data
        type: VOLUME
        scope: shared
`,
	}
	if err := db.UpsertLocalWorkload(ms); err != nil {
		t.Fatalf("upsert local shared: %v", err)
	}
}

func TestReclaimerHasNoScheduledSweeper(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read package: %v", err)
	}
	needles := []string{
		"time.NewTicker",
		"time.NewTimer",
		"time.AfterFunc",
		"time.Tick(",
		"go func",
	}
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		body, readErr := os.ReadFile(name)
		if readErr != nil {
			t.Fatalf("read %s: %v", name, readErr)
		}
		src := string(body)
		for _, needle := range needles {
			if strings.Contains(src, needle) {
				t.Fatalf("%s must not start a scheduled volume delete (%q)", name, needle)
			}
		}
	}

	before := runtime.NumGoroutine()
	_, _, r := newReclaimer(t)
	_ = r
	after := runtime.NumGoroutine()
	if after > before+5 {
		t.Fatalf("constructing reclaim helper started goroutines: before=%d after=%d", before, after)
	}
}

func TestRemovePrivateKeepSetAbortsWithoutForce(t *testing.T) {
	dir, db, r := newReclaimer(t)
	uuid := "ms-desired"
	marker := writePrivateMarker(t, dir, uuid, "data")
	saveDesired(t, db, uuid, []*models.VolumeMapping{
		models.NewVolumeMapping("data", "/data", "rw", models.VolumeMappingTypeVolume),
	})
	events := captureEvents(t)

	err := r.RemovePrivate(uuid, "data", RemoveOptions{})
	if !errors.Is(err, ErrKeepSet) {
		t.Fatalf("err=%v want keep-set", err)
	}
	requireFileKept(t, marker)
	if _, getErr := db.GetPersistentVolume(uuid, "data"); getErr != nil {
		t.Fatalf("ledger row must remain: %v", getErr)
	}

	found := false
	for _, ev := range *events {
		if ev.Result == runtimeops.ResultFailed && ev.ReasonCode == runtimeops.ReasonVolumeKeepSet {
			found = true
			if ev.Fields["trigger"] != triggerVolumeRM {
				t.Fatalf("trigger=%v", ev.Fields["trigger"])
			}
			if ev.Fields["uuid"] != uuid {
				t.Fatalf("uuid=%v", ev.Fields["uuid"])
			}
		}
	}
	if !found {
		t.Fatal("expected keep-set abort event")
	}
}

func TestRemovePrivateKeepSetForceDeletesWhenUnmounted(t *testing.T) {
	dir, db, r := newReclaimer(t)
	uuid := "ms-force"
	marker := writePrivateMarker(t, dir, uuid, "data")
	saveDesired(t, db, uuid, []*models.VolumeMapping{
		models.NewVolumeMapping("data", "/data", "rw", models.VolumeMappingTypeVolume),
	})

	if err := r.RemovePrivate(uuid, "data", RemoveOptions{Force: true}); err != nil {
		t.Fatalf("force remove: %v", err)
	}
	requireGone(t, marker)
	requireRowGone(t, db, uuid, "data")
}

func TestPruneOrphansDryRunListsWithoutDelete(t *testing.T) {
	dir, db, r := newReclaimer(t)
	uuid := "ms-orphan"
	marker := writePrivateMarker(t, dir, uuid, "data")
	upsertPrivate(t, db, uuid, "data", store.PersistentVolumeKindWorkload)
	if err := db.MarkPersistentVolumesUnreferenced(uuid); err != nil {
		t.Fatalf("mark unreferenced: %v", err)
	}

	result, err := r.PruneOrphans(PruneOrphansOptions{})
	if err != nil {
		t.Fatalf("dry-run prune: %v", err)
	}
	if len(result.Candidates) == 0 {
		t.Fatal("expected orphan candidates")
	}
	if len(result.Deleted) != 0 {
		t.Fatalf("dry-run must not delete, deleted=%+v", result.Deleted)
	}
	requireFileKept(t, marker)
	if _, getErr := db.GetPersistentVolume(uuid, "data"); getErr != nil {
		t.Fatalf("ledger row must remain: %v", getErr)
	}
}

func TestPruneOrphansSkipsYoungUnreferenced(t *testing.T) {
	dir, db, r := newReclaimer(t)
	uuid := "ms-young"
	marker := writePrivateMarker(t, dir, uuid, "data")
	upsertPrivate(t, db, uuid, "data", store.PersistentVolumeKindWorkload)
	if err := db.MarkPersistentVolumesUnreferenced(uuid); err != nil {
		t.Fatalf("mark unreferenced: %v", err)
	}

	result, err := r.PruneOrphans(PruneOrphansOptions{Confirm: true})
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if len(result.Deleted) != 0 {
		t.Fatalf("young orphan must be skipped, deleted=%+v", result.Deleted)
	}
	skipped := false
	for _, cand := range result.Skipped {
		if cand.UUID == uuid && cand.Reason == "grace" {
			skipped = true
		}
	}
	if !skipped {
		t.Fatalf("expected grace skip, skipped=%+v", result.Skipped)
	}
	requireFileKept(t, marker)
}

func TestPruneOrphansForceDeletesWhenNotKeepSet(t *testing.T) {
	dir, db, r := newReclaimer(t)
	uuid := "ms-force-orphan"
	marker := writePrivateMarker(t, dir, uuid, "data")
	upsertPrivate(t, db, uuid, "data", store.PersistentVolumeKindWorkload)
	if err := db.MarkPersistentVolumesUnreferenced(uuid); err != nil {
		t.Fatalf("mark unreferenced: %v", err)
	}

	result, err := r.PruneOrphans(PruneOrphansOptions{Confirm: true, Force: true})
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if len(result.Deleted) != 1 {
		t.Fatalf("deleted=%+v", result.Deleted)
	}
	requireGone(t, marker)
	requireRowGone(t, db, uuid, "data")
}

func TestPruneOrphansSkipsControlPlaneEvenWithForce(t *testing.T) {
	dir, db, r := newReclaimer(t)
	uuid := "cp-uuid"
	marker := writePrivateMarker(t, dir, uuid, "iofog-controller-db")
	upsertPrivate(t, db, uuid, "iofog-controller-db", store.PersistentVolumeKindControlPlane)
	if err := db.MarkPersistentVolumesUnreferenced(uuid); err != nil {
		t.Fatalf("mark unreferenced: %v", err)
	}

	result, err := r.PruneOrphans(PruneOrphansOptions{Confirm: true, Force: true})
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	for _, cand := range result.Candidates {
		if cand.UUID == uuid {
			t.Fatalf("control-plane volume must not be an orphan candidate: %+v", cand)
		}
	}
	requireFileKept(t, marker)
}

func TestRemoveRefusesPathOutsideVolumeTrees(t *testing.T) {
	dir, db, r := newReclaimer(t)
	outsideDir := t.TempDir()
	marker := filepath.Join(outsideDir, "bind.txt")
	if err := os.WriteFile(marker, []byte("bind"), 0o600); err != nil {
		t.Fatalf("write outside: %v", err)
	}
	if err := db.UpsertPersistentVolume("ms-jail", "data", store.PersistentVolumeKindWorkload, models.VolumeScopePrivate, outsideDir); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	err := r.RemovePrivate("ms-jail", "data", RemoveOptions{Force: true})
	if !errors.Is(err, ErrPathJail) {
		t.Fatalf("err=%v want path jail", err)
	}
	requireFileKept(t, marker)
	_ = dir
}

func TestBindHostPathIsNeverDeleted(t *testing.T) {
	dir, db, r := newReclaimer(t)
	bindDir := filepath.Join(t.TempDir(), "operator-bind")
	if err := os.MkdirAll(bindDir, 0o755); err != nil {
		t.Fatalf("mkdir bind: %v", err)
	}
	bindMarker := filepath.Join(bindDir, "keep.txt")
	if err := os.WriteFile(bindMarker, []byte("bind"), 0o600); err != nil {
		t.Fatalf("write bind: %v", err)
	}

	uuid := "ms-bind-neighbor"
	volMarker := writePrivateMarker(t, dir, uuid, "data")
	upsertPrivate(t, db, uuid, "data", store.PersistentVolumeKindWorkload)
	if err := db.MarkPersistentVolumesUnreferenced(uuid); err != nil {
		t.Fatalf("mark unreferenced: %v", err)
	}

	if _, err := r.destroyJailed(bindDir, r.dataRoot()); !errors.Is(err, ErrPathJail) {
		t.Fatalf("bind destroy err=%v want path jail", err)
	}
	if _, err := r.destroyJailed(bindDir, r.sharedRoot()); !errors.Is(err, ErrPathJail) {
		t.Fatalf("bind shared destroy err=%v want path jail", err)
	}
	if err := r.RemovePrivate(uuid, "data", RemoveOptions{Force: true}); err != nil {
		t.Fatalf("remove private: %v", err)
	}
	requireGone(t, volMarker)
	requireFileKept(t, bindMarker)
}

func TestPurgeWorkloadsDestroysWorkloadKeepsControlPlane(t *testing.T) {
	dir, db, r := newReclaimer(t)

	privateA := writePrivateMarker(t, dir, "ms-a", "data")
	upsertPrivate(t, db, "ms-a", "data", store.PersistentVolumeKindWorkload)

	exclusive := writeSharedMarker(t, dir, "only-a")
	upsertShared(t, db, "ms-a", "only-a")

	sharedBoth := writeSharedMarker(t, dir, "nodered-config")
	upsertShared(t, db, "ms-a", "nodered-config")
	upsertShared(t, db, "ms-b", "nodered-config")

	cpMarker := writePrivateMarker(t, dir, "cp-uuid", "iofog-controller-db")
	upsertPrivate(t, db, "cp-uuid", "iofog-controller-db", store.PersistentVolumeKindControlPlane)

	if err := r.PurgeWorkloads([]string{"ms-a"}); err != nil {
		t.Fatalf("purge: %v", err)
	}

	requireGone(t, privateA)
	requireGone(t, exclusive)
	requireFileKept(t, sharedBoth)
	requireFileKept(t, cpMarker)
	requireRowGone(t, db, "ms-a", "data")
	requireRowGone(t, db, "ms-a", "only-a")
	if _, err := db.GetPersistentVolume("ms-b", "nodered-config"); err != nil {
		t.Fatalf("remaining shared consumer must stay: %v", err)
	}
	if _, err := db.GetPersistentVolume("cp-uuid", "iofog-controller-db"); err != nil {
		t.Fatalf("control-plane volume must stay: %v", err)
	}
}

func TestDeleteControlPlaneDestroysOnlyControlPlane(t *testing.T) {
	dir, db, r := newReclaimer(t)
	cpMarker := writePrivateMarker(t, dir, "cp-uuid", "iofog-controller-db")
	logMarker := writePrivateMarker(t, dir, "cp-uuid", "iofog-controller-log")
	upsertPrivate(t, db, "cp-uuid", "iofog-controller-db", store.PersistentVolumeKindControlPlane)
	upsertPrivate(t, db, "cp-uuid", "iofog-controller-log", store.PersistentVolumeKindControlPlane)

	workload := writePrivateMarker(t, dir, "ms-other", "data")
	upsertPrivate(t, db, "ms-other", "data", store.PersistentVolumeKindWorkload)

	if err := r.DeleteControlPlane("cp-uuid"); err != nil {
		t.Fatalf("control-plane delete: %v", err)
	}
	requireGone(t, cpMarker)
	requireGone(t, logMarker)
	requireFileKept(t, workload)
	requireRowGone(t, db, "cp-uuid", "iofog-controller-db")
	requireRowGone(t, db, "cp-uuid", "iofog-controller-log")
}

func TestRemoveSharedAbortsWhenConsumerRemains(t *testing.T) {
	dir, db, r := newReclaimer(t)
	name := "nodered-config"
	marker := writeSharedMarker(t, dir, name)
	upsertLocalShared(t, db, "local-uuid", name)
	saveDesired(t, db, "controller-uuid", []*models.VolumeMapping{
		{
			HostDestination:      name,
			ContainerDestination: "/data",
			AccessMode:           "rw",
			Type:                 models.VolumeMappingTypeVolume,
			Scope:                models.VolumeScopeShared,
		},
	})
	events := captureEvents(t)

	err := r.RemoveShared(name, RemoveOptions{})
	if !errors.Is(err, ErrKeepSet) {
		t.Fatalf("err=%v want keep-set", err)
	}
	requireFileKept(t, marker)

	if err := db.SaveControllerMicroservices(nil); err != nil {
		t.Fatalf("drop controller desired: %v", err)
	}
	err = r.RemoveShared(name, RemoveOptions{})
	if !errors.Is(err, ErrKeepSet) {
		t.Fatalf("local remaining consumer err=%v want keep-set", err)
	}
	requireFileKept(t, marker)

	found := false
	for _, ev := range *events {
		if ev.ReasonCode == runtimeops.ReasonVolumeKeepSet && ev.Fields["scope"] == models.VolumeScopeShared {
			found = true
		}
	}
	if !found {
		t.Fatal("expected shared keep-set abort event")
	}
}

func TestRemoveSharedDeletesAfterLastConsumer(t *testing.T) {
	dir, db, r := newReclaimer(t)
	name := "nodered-config"
	marker := writeSharedMarker(t, dir, name)
	upsertShared(t, db, "local-uuid", name)
	upsertShared(t, db, "controller-uuid", name)
	if err := db.MarkPersistentVolumesUnreferenced("local-uuid"); err != nil {
		t.Fatalf("unreference local: %v", err)
	}
	if err := db.MarkPersistentVolumesUnreferenced("controller-uuid"); err != nil {
		t.Fatalf("unreference controller: %v", err)
	}
	now := time.Now()
	r.setNow(func() time.Time { return now })
	ageUnreferenced(t, db, "local-uuid", name, now.Add(-25*time.Hour))
	ageUnreferenced(t, db, "controller-uuid", name, now.Add(-24*time.Hour))

	if err := r.RemoveShared(name, RemoveOptions{}); err != nil {
		t.Fatalf("remove shared: %v", err)
	}
	requireGone(t, marker)
	requireRowGone(t, db, "local-uuid", name)
	requireRowGone(t, db, "controller-uuid", name)
}

func TestForceAbortsWhenPathIsMounted(t *testing.T) {
	dir, db, r := newReclaimer(t)
	uuid := "ms-mounted"
	marker := writePrivateMarker(t, dir, uuid, "data")
	upsertPrivate(t, db, uuid, "data", store.PersistentVolumeKindWorkload)
	if err := db.MarkPersistentVolumesUnreferenced(uuid); err != nil {
		t.Fatalf("mark unreferenced: %v", err)
	}
	rec, err := db.GetPersistentVolume(uuid, "data")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	r.SetUsageProvider(staticUsage{mounted: map[string]bool{filepath.Clean(rec.HostPath): true}})

	err = r.RemovePrivate(uuid, "data", RemoveOptions{Force: true})
	if !errors.Is(err, ErrInUse) {
		t.Fatalf("private err=%v want in-use", err)
	}
	requireFileKept(t, marker)

	sharedMarker := writeSharedMarker(t, dir, "shared-data")
	upsertShared(t, db, "ms-shared", "shared-data")
	if err := db.MarkPersistentVolumesUnreferenced("ms-shared"); err != nil {
		t.Fatalf("unreference shared: %v", err)
	}
	shared, err := db.GetPersistentVolume("ms-shared", "shared-data")
	if err != nil {
		t.Fatalf("get shared: %v", err)
	}
	r.SetUsageProvider(staticUsage{mounted: map[string]bool{filepath.Clean(shared.HostPath): true}})
	err = r.RemoveShared("shared-data", RemoveOptions{Force: true})
	if !errors.Is(err, ErrInUse) {
		t.Fatalf("shared err=%v want in-use", err)
	}
	requireFileKept(t, sharedMarker)
}

func TestRemovePrivateDoesNotDeleteSharedTree(t *testing.T) {
	dir, db, r := newReclaimer(t)
	privateMarker := writePrivateMarker(t, dir, "ms-1", "config")
	sharedMarker := writeSharedMarker(t, dir, "config")
	upsertPrivate(t, db, "ms-1", "config", store.PersistentVolumeKindWorkload)
	upsertShared(t, db, "ms-2", "config")
	if err := db.MarkPersistentVolumesUnreferenced("ms-1"); err != nil {
		t.Fatalf("unreference: %v", err)
	}

	if err := r.RemovePrivate("ms-1", "config", RemoveOptions{}); err != nil {
		t.Fatalf("remove private: %v", err)
	}
	requireGone(t, privateMarker)
	requireFileKept(t, sharedMarker)
}

func TestPruneOrphansSharedUsesLastConsumerClock(t *testing.T) {
	dir, db, r := newReclaimer(t)
	name := "shared-orphan"
	marker := writeSharedMarker(t, dir, name)
	upsertShared(t, db, "ms-first", name)
	upsertShared(t, db, "ms-last", name)
	if err := db.MarkPersistentVolumesUnreferenced("ms-first"); err != nil {
		t.Fatalf("unreference first: %v", err)
	}
	if err := db.MarkPersistentVolumesUnreferenced("ms-last"); err != nil {
		t.Fatalf("unreference last: %v", err)
	}
	now := time.Date(2026, 9, 19, 15, 0, 0, 0, time.UTC)
	r.setNow(func() time.Time { return now })
	ageUnreferenced(t, db, "ms-first", name, now.Add(-48*time.Hour))
	ageUnreferenced(t, db, "ms-last", name, now.Add(-2*time.Hour))

	young, err := r.PruneOrphans(PruneOrphansOptions{Confirm: true})
	if err != nil {
		t.Fatalf("prune young last consumer: %v", err)
	}
	if len(young.Deleted) != 0 {
		t.Fatalf("last-consumer clock must skip, deleted=%+v", young.Deleted)
	}
	requireFileKept(t, marker)

	r.setNow(func() time.Time { return now.Add(23 * time.Hour) })
	aged, err := r.PruneOrphans(PruneOrphansOptions{Confirm: true})
	if err != nil {
		t.Fatalf("prune aged last consumer: %v", err)
	}
	if len(aged.Deleted) != 1 {
		t.Fatalf("expected shared orphan delete, deleted=%+v skipped=%+v", aged.Deleted, aged.Skipped)
	}
	requireGone(t, marker)
}

func TestPruneOrphansCleanupBypassesGrace(t *testing.T) {
	dir, db, r := newReclaimer(t)
	uuid := "ms-cleanup"
	marker := writePrivateMarker(t, dir, uuid, "data")
	upsertPrivate(t, db, uuid, "data", store.PersistentVolumeKindWorkload)
	if err := db.MarkPersistentVolumesUnreferenced(uuid); err != nil {
		t.Fatalf("mark unreferenced: %v", err)
	}

	result, err := r.PruneOrphans(PruneOrphansOptions{Confirm: true, CleanupUUIDs: []string{uuid}})
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if len(result.Deleted) != 1 {
		t.Fatalf("cleanup bit should bypass grace, deleted=%+v skipped=%+v", result.Deleted, result.Skipped)
	}
	requireGone(t, marker)
}
