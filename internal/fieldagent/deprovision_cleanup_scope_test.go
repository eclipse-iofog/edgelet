package fieldagent

import (
	"errors"
	"testing"

	"github.com/eclipse-iofog/edgelet/internal/buildmeta"
	"github.com/eclipse-iofog/edgelet/internal/constants"
	"github.com/eclipse-iofog/edgelet/internal/models"
	"github.com/eclipse-iofog/edgelet/internal/store"
)

func TestClearSQLiteCacheTablesOnDeprovision_AllClearsLocalRows(t *testing.T) {
	db := store.GetInstance()
	_ = db.Close()
	if err := db.Open(t.TempDir()); err != nil {
		t.Fatalf("failed to open sqlite DB: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	local := &models.LocalDeployedMicroservice{
		LocalUUID:        "local-1",
		ApplicationName:  "edgelet",
		MicroserviceName: "router",
		SourceName:       "local-cli",
		ManifestYAML:     "kind: Microservice",
		ImageName:        "nginx:latest",
		State:            "running",
	}
	if err := db.UpsertLocalWorkload(local); err != nil {
		t.Fatalf("failed to insert local deployment: %v", err)
	}
	if err := db.UpsertRuntimeContainerRef("local-1", store.RuntimeScopeLocal, "cid-1", "sandbox-1"); err != nil {
		t.Fatalf("failed to insert local container state: %v", err)
	}

	GetInstance().clearSQLiteCacheTablesOnDeprovision(false)

	locals, err := db.ListLocalWorkloads()
	if err != nil {
		t.Fatalf("failed listing locals: %v", err)
	}
	if len(locals) != 0 {
		t.Fatalf("expected local deployments cleared, got %d", len(locals))
	}
	state, err := db.GetRuntimeContainerRef("local-1", store.RuntimeScopeLocal)
	if err != nil {
		t.Fatalf("failed reading local container state: %v", err)
	}
	if state != nil {
		t.Fatalf("expected local container state cleared, got %#v", state)
	}
}

func TestClearSQLiteCacheTablesOnDeprovision_LocalScopePreservesLocalRows(t *testing.T) {
	db := store.GetInstance()
	_ = db.Close()
	if err := db.Open(t.TempDir()); err != nil {
		t.Fatalf("failed to open sqlite DB: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	local := &models.LocalDeployedMicroservice{
		LocalUUID:        "local-2",
		ApplicationName:  "edgelet",
		MicroserviceName: "nodered",
		SourceName:       "local-cli",
		ManifestYAML:     "kind: Microservice",
		ImageName:        "nodered/node-red:latest",
		State:            "running",
	}
	if err := db.UpsertLocalWorkload(local); err != nil {
		t.Fatalf("failed to insert local deployment: %v", err)
	}
	if err := db.UpsertRuntimeContainerRef("local-2", store.RuntimeScopeLocal, "cid-2", "sandbox-2"); err != nil {
		t.Fatalf("failed to insert local container state: %v", err)
	}

	GetInstance().clearSQLiteCacheTablesOnDeprovision(true)

	locals, err := db.ListLocalWorkloads()
	if err != nil {
		t.Fatalf("failed listing locals: %v", err)
	}
	if len(locals) != 1 {
		t.Fatalf("expected local deployment preserved, got %d", len(locals))
	}
	state, err := db.GetRuntimeContainerRef("local-2", store.RuntimeScopeLocal)
	if err != nil {
		t.Fatalf("failed reading local container state: %v", err)
	}
	if state == nil {
		t.Fatal("expected local container state preserved")
	}
}

func TestClearVolumeMountsOnDeprovision_LocalScopeSkipsClear(t *testing.T) {
	allCalled := false
	localCalled := false
	GetInstance().clearVolumeMountsOnDeprovision(true, func() error {
		allCalled = true
		return nil
	}, func() error {
		localCalled = true
		return nil
	})
	if allCalled {
		t.Fatal("expected all-scope clear function not to be called for preserveLocal=true")
	}
	if !localCalled {
		t.Fatal("expected local-scope clear function to be called for preserveLocal=true")
	}
}

func TestClearVolumeMountsOnDeprovision_AllScopeInvokesClear(t *testing.T) {
	allCalled := false
	localCalled := false
	GetInstance().clearVolumeMountsOnDeprovision(false, func() error {
		allCalled = true
		return nil
	}, func() error {
		localCalled = true
		return nil
	})
	if !allCalled {
		t.Fatal("expected all-scope clear function to be called for preserveLocal=false")
	}
	if localCalled {
		t.Fatal("expected local-scope clear function not to be called for preserveLocal=false")
	}
}

func TestClearVolumeMountsOnDeprovision_AllScopeRecoversPanic(t *testing.T) {
	GetInstance().clearVolumeMountsOnDeprovision(false, func() error {
		panic("boom")
	}, func() error {
		return nil
	})
}

func TestClearVolumeMountsOnDeprovision_AllScopeHandlesError(t *testing.T) {
	called := false
	GetInstance().clearVolumeMountsOnDeprovision(false, func() error {
		called = true
		return errors.New("clear failed")
	}, func() error {
		return nil
	})
	if !called {
		t.Fatal("expected clear function to be called")
	}
}

func TestClearLiteRuntimeArtifactsOnDeprovision_LocalScopeSkipsPrune(t *testing.T) {
	embedded := true
	buildmeta.SetHasEmbeddedEngineForTest(&embedded)
	t.Cleanup(func() { buildmeta.SetHasEmbeddedEngineForTest(nil) })
	GetInstance().config.ContainerEngine = constants.EngineDocker

	containersCalled := false
	GetInstance().clearLiteRuntimeArtifactsOnDeprovision(true, func() error {
		containersCalled = true
		return nil
	})
	if containersCalled {
		t.Fatal("expected prune steps to be skipped for preserveLocal=true")
	}
}

func TestClearLiteRuntimeArtifactsOnDeprovision_EmbeddedEdgeletEngineSkipsPrune(t *testing.T) {
	embedded := true
	buildmeta.SetHasEmbeddedEngineForTest(&embedded)
	t.Cleanup(func() { buildmeta.SetHasEmbeddedEngineForTest(nil) })
	GetInstance().config.ContainerEngine = constants.EngineEdgelet

	containersCalled := false
	GetInstance().clearLiteRuntimeArtifactsOnDeprovision(false, func() error {
		containersCalled = true
		return nil
	})
	if containersCalled {
		t.Fatal("expected prune steps to be skipped for embedded edgelet engine")
	}
}

func TestClearLiteRuntimeArtifactsOnDeprovision_ExternalEngineOnLinuxStillPrunes(t *testing.T) {
	embedded := true
	buildmeta.SetHasEmbeddedEngineForTest(&embedded)
	t.Cleanup(func() { buildmeta.SetHasEmbeddedEngineForTest(nil) })
	GetInstance().config.ContainerEngine = constants.EngineDocker

	containersCalled := false
	GetInstance().clearLiteRuntimeArtifactsOnDeprovision(false, func() error {
		containersCalled = true
		return nil
	})
	if !containersCalled {
		t.Fatal("expected container prune for docker engine on embedded-capable platform")
	}
}

func TestClearLiteRuntimeArtifactsOnDeprovision_OnlyDockerPodmanEngines(t *testing.T) {
	embedded := true
	buildmeta.SetHasEmbeddedEngineForTest(&embedded)
	t.Cleanup(func() { buildmeta.SetHasEmbeddedEngineForTest(nil) })

	GetInstance().config.ContainerEngine = constants.EngineEdgelet
	called := false
	GetInstance().clearLiteRuntimeArtifactsOnDeprovision(false, func() error {
		called = true
		return nil
	})
	if called {
		t.Fatal("expected prune steps skipped for non-docker/podman engine")
	}
}

func TestClearLiteRuntimeArtifactsOnDeprovision_DoesNotPruneVolumes(t *testing.T) {
	embedded := true
	buildmeta.SetHasEmbeddedEngineForTest(&embedded)
	t.Cleanup(func() { buildmeta.SetHasEmbeddedEngineForTest(nil) })
	GetInstance().config.ContainerEngine = constants.EnginePodman

	order := make([]string, 0, 1)
	GetInstance().clearLiteRuntimeArtifactsOnDeprovision(false, func() error {
		order = append(order, "containers")
		return errors.New("container prune failed")
	})
	if len(order) != 1 || order[0] != "containers" {
		t.Fatalf("expected only container prune on deprovision, got %v", order)
	}
}
