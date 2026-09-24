package runtimeapi

import (
	"database/sql"
	"errors"
	"strings"
	"testing"

	"github.com/eclipse-iofog/edgelet/internal/buildmeta"
	"github.com/eclipse-iofog/edgelet/internal/config"
	"github.com/eclipse-iofog/edgelet/internal/constants"
	"github.com/eclipse-iofog/edgelet/internal/models"
)

func newRuntimeClassFacade(t *testing.T, engine string) *Facade {
	t.Helper()
	f := NewFacade()
	cfg := config.GetInstance()
	origEngine := cfg.ContainerEngine
	cfg.ContainerEngine = engine
	f.cfg = cfg
	embedded := true
	buildmeta.SetHasEmbeddedEngineForTest(&embedded)
	if err := f.db.Open(t.TempDir()); err != nil {
		t.Fatalf("failed to open test db: %v", err)
	}
	t.Cleanup(func() {
		_ = f.db.Close()
		cfg.ContainerEngine = origEngine
		buildmeta.SetHasEmbeddedEngineForTest(nil)
	})
	return f
}

func TestApplyLocalRuntimeClassManifest_RejectsManagedNameWhileProvisioned(t *testing.T) {
	f := newRuntimeClassFacade(t, constants.EngineEdgelet)
	cfg := config.GetInstance()
	origUUID := cfg.IOFogUUID
	cfg.IOFogUUID = "agent-uuid-runtimeclass"
	t.Cleanup(func() { cfg.IOFogUUID = origUUID })

	if err := f.db.SaveControllerRuntimeClasses([]*models.ControllerRuntimeClass{
		{Name: "spin", Handler: "spin"},
	}); err != nil {
		t.Fatalf("save fleet row: %v", err)
	}

	_, err := f.ApplyLocalRuntimeClassManifest(`
apiVersion: edgelet.iofog.org/v1
kind: RuntimeClass
metadata:
  name: spin
handler: spin
`, false)
	if err == nil {
		t.Fatal("expected local apply of managed name to be rejected")
	}
	var managedErr *ErrRuntimeClassManagedName
	if !errors.As(err, &managedErr) || managedErr.Name != "spin" {
		t.Fatalf("expected managed-name error, got %T %v", err, err)
	}
	if _, getErr := f.db.GetLocalRuntimeClass("spin"); !errors.Is(getErr, sql.ErrNoRows) {
		t.Fatalf("local apply must not overwrite a managed name, got %v", getErr)
	}
}

func TestApplyControllerRuntimeClasses_CatalogHandlerDoesNotRestartDataPlane(t *testing.T) {
	f := newRuntimeClassFacade(t, constants.EngineEdgelet)
	if err := f.ApplyControllerRuntimeClasses([]*models.ControllerRuntimeClass{
		{Name: "spin", Handler: "spin"},
		{Name: "edgelet-wasmtime", Handler: "edgelet-wasmtime"},
	}); err != nil {
		t.Fatalf("fleet apply: %v", err)
	}
	// Existing apply is metadata-only; catalog handlers stay on that path.
	spin, err := f.db.GetLocalRuntimeClass("spin")
	if err != nil || spin.Source != models.RuntimeClassSourceManaged || spin.Handler != "spin" {
		t.Fatalf("expected managed spin apply without a data-plane restart, got %+v err=%v", spin, err)
	}
}

func TestApplyControllerRuntimeClasses_DockerEngineDoesNotApply(t *testing.T) {
	f := newRuntimeClassFacade(t, constants.EngineDocker)
	if err := f.ApplyControllerRuntimeClasses([]*models.ControllerRuntimeClass{
		{Name: "spin", Handler: "spin"},
	}); err != nil {
		t.Fatalf("docker fleet persist must not fail the node: %v", err)
	}
	got, err := f.db.LoadControllerRuntimeClasses()
	if err != nil || len(got) != 1 || got[0].Name != "spin" {
		t.Fatalf("expected desired row persisted, got %+v err=%v", got, err)
	}
	if _, err := f.db.GetLocalRuntimeClass("spin"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("docker engine must not apply runtime classes, got %v", err)
	}
}

func TestDeleteRuntimeClass_RejectsWhenControllerMicroserviceUsesRuntime(t *testing.T) {
	f := newRuntimeClassFacade(t, constants.EngineEdgelet)
	if _, err := f.ApplyLocalRuntimeClassManifest(`
apiVersion: edgelet.iofog.org/v1
kind: RuntimeClass
metadata:
  name: spin
handler: spin
`, false); err != nil {
		t.Fatalf("seed runtimeclass: %v", err)
	}
	runtime := "spin"
	if err := f.db.SaveControllerMicroservices([]*models.Microservice{{
		MicroserviceUUID: "ms-spin-1",
		ImageName:        "img:1",
		Runtime:          &runtime,
	}}); err != nil {
		t.Fatalf("seed controller ms: %v", err)
	}

	err := f.DeleteRuntimeClass("spin")
	if err == nil {
		t.Fatal("expected in-use delete rejection")
	}
	var inUseErr *ErrRuntimeClassInUse
	if !errors.As(err, &inUseErr) {
		t.Fatalf("expected runtime-in-use error type, got: %T %v", err, err)
	}
	if len(inUseErr.BlockingMicroserviceUuids) == 0 || inUseErr.BlockingMicroserviceUuids[0] != "ms-spin-1" {
		t.Fatalf("expected blocking controller uuid, got=%v", inUseErr.BlockingMicroserviceUuids)
	}
}

func TestApplyLocalRuntimeClassManifest_AllowsNameAfterUnprovisioned(t *testing.T) {
	f := newRuntimeClassFacade(t, constants.EngineEdgelet)
	cfg := config.GetInstance()
	origUUID := cfg.IOFogUUID
	cfg.IOFogUUID = ""
	t.Cleanup(func() { cfg.IOFogUUID = origUUID })

	if err := f.db.SaveControllerRuntimeClasses([]*models.ControllerRuntimeClass{
		{Name: "spin", Handler: "spin"},
	}); err != nil {
		t.Fatalf("save fleet row: %v", err)
	}
	item, err := f.ApplyLocalRuntimeClassManifest(`
apiVersion: edgelet.iofog.org/v1
kind: RuntimeClass
metadata:
  name: spin
handler: spin
`, false)
	if err != nil {
		t.Fatalf("unprovisioned local apply should succeed, got %v", err)
	}
	if item.Source != models.RuntimeClassSourceLocal {
		t.Fatalf("expected local source, got %q", item.Source)
	}
}

func TestErrRuntimeClassManagedNameMessage(t *testing.T) {
	err := &ErrRuntimeClassManagedName{Name: "SPIN"}
	if !strings.Contains(err.Error(), "spin") {
		t.Fatalf("expected name in error, got %q", err.Error())
	}
}
