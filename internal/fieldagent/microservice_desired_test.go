package fieldagent

import (
	"testing"

	"github.com/eclipse-iofog/edgelet/internal/containerapply"
	"github.com/eclipse-iofog/edgelet/internal/models"
	"github.com/eclipse-iofog/edgelet/internal/processmanager"
)

func TestMicroserviceListImageChangeMarksOnlyThatWorkload(t *testing.T) {
	fa, pm := newDesiredFieldAgent(t)
	original := []*models.Microservice{
		models.NewMicroservice("ms-a", "nginx:latest"),
		models.NewMicroservice("ms-b", "redis:latest"),
	}
	fa.replaceLatestMicroservices(original)

	changed := models.NewMicroservice("ms-a", "nginx:other")
	fa.setLatestMicroservices([]*models.Microservice{changed, models.NewMicroservice("ms-b", "redis:latest")})
	if !pm.ReconcilePending("ms-a") {
		t.Fatal("image change should mark that workload")
	}
	if pm.ReconcilePending("ms-b") {
		t.Fatal("unchanged workload should stay clean")
	}
}

func TestMicroserviceListIdenticalSpecsDoNotMark(t *testing.T) {
	fa, pm := newDesiredFieldAgent(t)
	first := []*models.Microservice{
		models.NewMicroservice("ms-a", "nginx:latest"),
		models.NewMicroservice("ms-b", "redis:latest"),
	}
	fa.replaceLatestMicroservices(first)
	fa.setLatestMicroservices([]*models.Microservice{
		models.NewMicroservice("ms-a", "nginx:latest"),
		models.NewMicroservice("ms-b", "redis:latest"),
	})
	if pm.ReconcilePending("ms-a") || pm.ReconcilePending("ms-b") {
		t.Fatal("reloading the same desired spec should not mark")
	}
}

func TestChangesWithoutMicroserviceListDoesNotMark(t *testing.T) {
	fa, pm := newDesiredFieldAgent(t)
	fa.state.SetInitialization(false)
	fa.replaceLatestMicroservices([]*models.Microservice{
		models.NewMicroservice("ms-a", "nginx:latest"),
	})
	fa.processChanges(map[string]any{
		"registries": false,
		"config":     false,
		"version":    false,
		"prune":      false,
	})
	if pm.ReconcilePending("ms-a") {
		t.Fatal("a changes cycle without a list replace should not mark")
	}
}

func TestCatalogItemChangeMarksWithoutFingerprintChange(t *testing.T) {
	fa, pm := newDesiredFieldAgent(t)
	base := models.NewMicroservice("ms-a", "nginx:latest")
	base.Models = &models.ModelCatalog{
		BindPath:    "/models",
		Permissions: models.ModelCatalogPermRO,
		Items:       []models.ModelCatalogItem{{Name: "gemma3"}},
	}
	fa.replaceLatestMicroservices([]*models.Microservice{base})

	next := models.NewMicroservice("ms-a", "nginx:latest")
	next.Models = &models.ModelCatalog{
		BindPath:    "/models",
		Permissions: models.ModelCatalogPermRO,
		Items:       []models.ModelCatalogItem{{Name: "gemma3"}, {Name: "other"}},
	}
	baseFP, err := containerapply.Marshal(containerapply.FromMicroservice(base))
	if err != nil {
		t.Fatalf("fingerprint: %v", err)
	}
	nextFP, err := containerapply.Marshal(containerapply.FromMicroservice(next))
	if err != nil {
		t.Fatalf("fingerprint: %v", err)
	}
	if baseFP != nextFP {
		t.Fatalf("item membership changed the apply fingerprint:\n%s\n%s", baseFP, nextFP)
	}
	fa.setLatestMicroservices([]*models.Microservice{next})
	if !pm.ReconcilePending("ms-a") {
		t.Fatal("catalog membership change should mark the workload")
	}
}

func newDesiredFieldAgent(t *testing.T) (*FieldAgent, *processmanager.ProcessManager) {
	t.Helper()
	pm := &processmanager.ProcessManager{}
	fa := &FieldAgent{
		state:          NewState(),
		processManager: pm,
	}
	return fa, pm
}
