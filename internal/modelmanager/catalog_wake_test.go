package modelmanager

import (
	"errors"
	"testing"
	"time"

	"github.com/eclipse-iofog/edgelet/internal/config"
	"github.com/eclipse-iofog/edgelet/internal/models"
	"github.com/eclipse-iofog/edgelet/internal/processmanager"
	"github.com/eclipse-iofog/edgelet/internal/store"
)

func TestModelReadyAndFailedMarkBoundWorkloads(t *testing.T) {
	m, _, _ := newTestManager(t)
	cfg := config.GetInstance()
	orig := cfg.WatchdogEnabled
	cfg.WatchdogEnabled = false
	t.Cleanup(func() { cfg.WatchdogEnabled = orig })

	if err := store.GetInstance().UpsertLocalWorkload(localWithModel("local-bound", "gemma3")); err != nil {
		t.Fatalf("upsert bound: %v", err)
	}
	if err := store.GetInstance().UpsertLocalWorkload(localWithModel("local-other", "other-model")); err != nil {
		t.Fatalf("upsert other: %v", err)
	}
	pm := &processmanager.ProcessManager{}
	_ = processmanager.GetInstance()
	restore := processmanager.SetInstanceForTest(pm)
	t.Cleanup(restore)

	if _, err := m.UpsertDesired(sampleModel("gemma3", "ai/gemma3", "1", 1, nil)); err != nil {
		t.Fatalf("upsert model: %v", err)
	}
	if _, err := m.StartPull("gemma3"); err != nil {
		t.Fatalf("start pull: %v", err)
	}
	waitState(t, m, "gemma3", models.ModelStateReady)
	waitReconcilePending(t, pm, "local-bound", "ready model should mark the bound workload without waiting for a full sweep")
	if pm.ReconcilePending("local-other") {
		t.Fatal("ready model marked an unrelated workload")
	}

	m.SetPullers(&stubPuller{err: errors.New("pull denied")}, &stubPuller{})
	if _, err := m.UpsertDesired(sampleModel("other-model", "ai/other", "1", 1, nil)); err != nil {
		t.Fatalf("upsert failing model: %v", err)
	}
	if _, err := m.StartPull("other-model"); err != nil {
		t.Fatalf("start failing pull: %v", err)
	}
	waitState(t, m, "other-model", models.ModelStateFailed)
	waitReconcilePending(t, pm, "local-other", "failed model should mark the bound workload")
}

func waitReconcilePending(t *testing.T, pm *processmanager.ProcessManager, uuid, msg string) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if pm.ReconcilePending(uuid) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal(msg)
}

func localWithModel(uuid, modelName string) *models.LocalDeployedMicroservice {
	manifest := `apiVersion: edgelet.iofog.org/v1
kind: Microservice
metadata:
  name: ` + uuid + `
spec:
  image: nginx:latest
  models:
    bindPath: /models
    items:
      - name: ` + modelName + `
`
	return &models.LocalDeployedMicroservice{
		LocalUUID:        uuid,
		ApplicationName:  "edgelet",
		MicroserviceName: uuid,
		ManifestYAML:     manifest,
		ImageName:        "nginx:latest",
		DesiredState:     "running",
		Generation:       1,
	}
}
