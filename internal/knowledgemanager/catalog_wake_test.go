package knowledgemanager

import (
	"errors"
	"testing"
	"time"

	"github.com/eclipse-iofog/edgelet/internal/config"
	"github.com/eclipse-iofog/edgelet/internal/models"
	"github.com/eclipse-iofog/edgelet/internal/processmanager"
	"github.com/eclipse-iofog/edgelet/internal/store"
)

func TestKnowledgeReadyAndFailedMarkBoundWorkloads(t *testing.T) {
	m, _, _ := newTestManager(t)
	cfg := config.GetInstance()
	orig := cfg.WatchdogEnabled
	cfg.WatchdogEnabled = false
	t.Cleanup(func() { cfg.WatchdogEnabled = orig })

	if err := store.GetInstance().UpsertLocalWorkload(localWithKnowledge("local-bound", "product-docs")); err != nil {
		t.Fatalf("upsert bound: %v", err)
	}
	if err := store.GetInstance().UpsertLocalWorkload(localWithKnowledge("local-other", "other-docs")); err != nil {
		t.Fatalf("upsert other: %v", err)
	}
	pm := &processmanager.ProcessManager{}
	_ = processmanager.GetInstance()
	restore := processmanager.SetInstanceForTest(pm)
	t.Cleanup(restore)

	if _, err := m.UpsertDesired(sampleKnowledge("product-docs", "org/docs", "1", 1, nil)); err != nil {
		t.Fatalf("upsert knowledge: %v", err)
	}
	if _, err := m.StartPull("product-docs"); err != nil {
		t.Fatalf("start pull: %v", err)
	}
	waitState(t, m, "product-docs", models.KnowledgeStateReady)
	waitReconcilePending(t, pm, "local-bound", "ready knowledge should mark the bound workload without waiting for a full sweep")
	if pm.ReconcilePending("local-other") {
		t.Fatal("ready knowledge marked an unrelated workload")
	}

	m.SetPullers(&stubPuller{err: errors.New("pull denied")}, &stubPuller{})
	if _, err := m.UpsertDesired(sampleKnowledge("other-docs", "org/other", "1", 1, nil)); err != nil {
		t.Fatalf("upsert failing knowledge: %v", err)
	}
	if _, err := m.StartPull("other-docs"); err != nil {
		t.Fatalf("start failing pull: %v", err)
	}
	waitState(t, m, "other-docs", models.KnowledgeStateFailed)
	waitReconcilePending(t, pm, "local-other", "failed knowledge should mark the bound workload")
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

func localWithKnowledge(uuid, name string) *models.LocalDeployedMicroservice {
	manifest := `apiVersion: edgelet.iofog.org/v1
kind: Microservice
metadata:
  name: ` + uuid + `
spec:
  image: nginx:latest
  knowledge:
    bindPath: /knowledge
    items:
      - name: ` + name + `
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
