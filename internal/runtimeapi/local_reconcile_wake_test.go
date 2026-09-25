package runtimeapi

import (
	"testing"

	"github.com/eclipse-iofog/edgelet/internal/models"
	"github.com/eclipse-iofog/edgelet/internal/processmanager"
)

func TestLocalUpsertAndDeleteMarkThatWorkload(t *testing.T) {
	f := NewFacade()
	if err := f.db.Open(t.TempDir()); err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = f.db.Close() })

	pm := &processmanager.ProcessManager{}
	_ = processmanager.GetInstance()
	restore := processmanager.SetInstanceForTest(pm)
	t.Cleanup(restore)

	item := &models.LocalDeployedMicroservice{
		LocalUUID:        "local-up",
		ApplicationName:  "edgelet",
		MicroserviceName: "router",
		ManifestYAML:     "apiVersion: edgelet.iofog.org/v1\nkind: Microservice\nmetadata:\n  name: router\nspec:\n  image: nginx:latest\n",
		ImageName:        "nginx:latest",
		DesiredState:     "running",
		Generation:       1,
	}
	if err := f.UpsertLocalDeployment(item); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if !pm.ReconcilePending("local-up") {
		t.Fatal("local upsert should mark that workload")
	}

	gone := &models.LocalDeployedMicroservice{
		LocalUUID:        "local-gone",
		ApplicationName:  "edgelet",
		MicroserviceName: "old",
		ManifestYAML:     "apiVersion: edgelet.iofog.org/v1\nkind: Microservice\nmetadata:\n  name: old\nspec:\n  image: nginx:latest\n",
		ImageName:        "nginx:latest",
		DesiredState:     "running",
		Generation:       1,
	}
	if err := f.db.UpsertLocalWorkload(gone); err != nil {
		t.Fatalf("seed delete: %v", err)
	}
	if err := f.DeleteLocalDeployment("local-gone"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if !pm.ReconcilePending("local-gone") {
		t.Fatal("local delete should mark that workload")
	}
}
