package store

import (
	"testing"

	"github.com/eclipse-iofog/edgelet/internal/models"
)

func TestSystemControlPlaneCRUD(t *testing.T) {
	db := openFreshStoreDB(t)

	dep := &models.ControlPlaneDeployment{
		ControllerUUID: "cp-uuid-1",
		Namespace:      "default",
		Name:           "pot",
		ManifestYAML:   "kind: ControlPlane",
		Image:          "datasance/controller:latest",
		State:          "running",
		ContainerID:    "cid-cp-1",
		Generation:     2,
	}
	if err := db.UpsertSystemControlPlane(dep); err != nil {
		t.Fatalf("upsert system control plane: %v", err)
	}

	got, found, err := db.GetSystemControlPlane()
	if err != nil {
		t.Fatalf("get system control plane: %v", err)
	}
	if !found {
		t.Fatal("expected deployment to exist")
	}
	if got.ControllerUUID != "cp-uuid-1" {
		t.Fatalf("unexpected controller_uuid: %q", got.ControllerUUID)
	}
	if got.Namespace != "default" {
		t.Fatalf("unexpected namespace: %q", got.Namespace)
	}
	if got.Name != "pot" {
		t.Fatalf("unexpected name: %q", got.Name)
	}
	if got.Image != "datasance/controller:latest" {
		t.Fatalf("unexpected image: %q", got.Image)
	}
	if got.ContainerID != "cid-cp-1" {
		t.Fatalf("unexpected container_id: %q", got.ContainerID)
	}
	if got.DesiredState != "running" {
		t.Fatalf("expected desired_state running, got %q", got.DesiredState)
	}
	if got.Generation != 2 {
		t.Fatalf("expected generation=2, got %d", got.Generation)
	}

	dep.Image = "datasance/controller:v2"
	dep.Generation = 3
	if err := db.UpsertSystemControlPlane(dep); err != nil {
		t.Fatalf("upsert system control plane patch: %v", err)
	}

	got, found, err = db.GetSystemControlPlane()
	if err != nil {
		t.Fatalf("get system control plane after patch: %v", err)
	}
	if !found || got.Image != "datasance/controller:v2" || got.Generation != 3 {
		t.Fatalf("unexpected row after patch: found=%v image=%q generation=%d", found, got.Image, got.Generation)
	}

	if err := db.DeleteSystemControlPlane(); err != nil {
		t.Fatalf("delete system control plane: %v", err)
	}

	_, found, err = db.GetSystemControlPlane()
	if err != nil {
		t.Fatalf("get system control plane after delete: %v", err)
	}
	if found {
		t.Fatal("expected no deployment after delete")
	}
}

func TestSystemControlPlaneUpsertRecordsPrivateVolumes(t *testing.T) {
	db := openFreshStoreDB(t)
	dep := &models.ControlPlaneDeployment{
		ControllerUUID: "cp-uuid-volumes",
		Namespace:      "default",
		Name:           "pot",
		ManifestYAML:   "kind: ControlPlane",
	}
	if err := db.UpsertSystemControlPlane(dep); err != nil {
		t.Fatalf("upsert system control plane: %v", err)
	}
	for _, name := range []string{"iofog-controller-db", "iofog-controller-log"} {
		rec, err := db.GetPersistentVolume(dep.ControllerUUID, name)
		if err != nil {
			t.Fatalf("get persistent volume %s: %v", name, err)
		}
		if rec.Kind != PersistentVolumeKindControlPlane {
			t.Fatalf("%s kind=%q want controlplane", name, rec.Kind)
		}
		if rec.Scope != models.VolumeScopePrivate {
			t.Fatalf("%s scope=%q want private", name, rec.Scope)
		}
		want := PersistentVolumeHostPath(db.DiskDirectory(), dep.ControllerUUID, name, models.VolumeScopePrivate)
		if rec.HostPath != want {
			t.Fatalf("%s host_path=%q want %q", name, rec.HostPath, want)
		}
	}
}

func TestSystemControlPlaneSingletonConstraint(t *testing.T) {
	db := openFreshStoreDB(t)

	dep := &models.ControlPlaneDeployment{
		ControllerUUID: "cp-uuid-1",
		Name:           "pot",
		ManifestYAML:   "kind: ControlPlane",
	}
	if err := db.UpsertSystemControlPlane(dep); err != nil {
		t.Fatalf("upsert system control plane: %v", err)
	}

	_, err := db.Conn().Exec(`INSERT INTO system_control_plane (
		id, controller_uuid, namespace, name, manifest_yaml
	) VALUES (2, 'other-uuid', 'default', 'other', 'kind: ControlPlane')`)
	if err == nil {
		t.Fatal("expected CHECK constraint to reject second system_control_plane row")
	}
}

func TestSystemControlPlaneUpsertValidation(t *testing.T) {
	db := openFreshStoreDB(t)

	if err := db.UpsertSystemControlPlane(nil); err == nil {
		t.Fatal("expected error for nil deployment")
	}
	if err := db.UpsertSystemControlPlane(&models.ControlPlaneDeployment{
		Name:         "pot",
		ManifestYAML: "kind: ControlPlane",
	}); err == nil {
		t.Fatal("expected error for missing controller_uuid")
	}
}

func TestSystemControlPlaneRegisterStateFlags(t *testing.T) {
	db := openFreshStoreDB(t)

	dep := &models.ControlPlaneDeployment{
		ControllerUUID:        "cp-uuid-flags",
		Name:                  "pot",
		ManifestYAML:          "kind: ControlPlane",
		ControllerRegistered:  true,
		InitialRebuildSkipped: true,
	}
	if err := db.UpsertSystemControlPlane(dep); err != nil {
		t.Fatalf("upsert system control plane: %v", err)
	}

	got, found, err := db.GetSystemControlPlane()
	if err != nil || !found {
		t.Fatalf("get system control plane: found=%v err=%v", found, err)
	}
	if !got.ControllerRegistered {
		t.Fatal("expected controller_registered=true")
	}
	if !got.InitialRebuildSkipped {
		t.Fatal("expected initial_rebuild_skipped=true")
	}
}
