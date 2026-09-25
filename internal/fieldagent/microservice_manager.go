package fieldagent

import (
	"github.com/eclipse-iofog/edgelet/internal/models"
	"github.com/eclipse-iofog/edgelet/internal/processmanager"
	"github.com/eclipse-iofog/edgelet/internal/utils/logging"
)

// Ensure FieldAgent implements MicroserviceManagerInterface
var _ processmanager.MicroserviceManagerInterface = (*FieldAgent)(nil)

// GetLatestMicroservices returns the latest microservices from the controller
func (fa *FieldAgent) GetLatestMicroservices() []*models.Microservice {
	fa.microservicesMu.RLock()
	defer fa.microservicesMu.RUnlock()

	// Return a copy to prevent external modification
	result := make([]*models.Microservice, len(fa.latestMicroservices))
	copy(result, fa.latestMicroservices)
	return result
}

// GetCurrentMicroservices returns the current microservices (same as latest for now)
func (fa *FieldAgent) GetCurrentMicroservices() []*models.Microservice {
	fa.microservicesMu.RLock()
	defer fa.microservicesMu.RUnlock()

	// Return a copy to prevent external modification
	result := make([]*models.Microservice, len(fa.currentMicroservices))
	copy(result, fa.currentMicroservices)
	return result
}

// FindLatestMicroserviceByUUID finds a microservice by UUID
func (fa *FieldAgent) FindLatestMicroserviceByUUID(uuid string) *models.Microservice {
	fa.microservicesMu.RLock()
	defer fa.microservicesMu.RUnlock()

	for _, ms := range fa.latestMicroservices {
		if ms.MicroserviceUUID == uuid {
			return ms
		}
	}
	return nil
}

// GetRegistry returns a registry by ID
func (fa *FieldAgent) GetRegistry(id int) *models.Registry {
	fa.microservicesMu.RLock()
	defer fa.microservicesMu.RUnlock()

	for _, reg := range fa.registries {
		if reg.ID == id {
			return reg
		}
	}
	return nil
}

// SetCurrentMicroservices sets the current microservices
func (fa *FieldAgent) SetCurrentMicroservices(microservices []*models.Microservice) {
	fa.microservicesMu.Lock()
	defer fa.microservicesMu.Unlock()

	fa.currentMicroservices = make([]*models.Microservice, len(microservices))
	copy(fa.currentMicroservices, microservices)
}

// setLatestMicroservices sets the latest microservices (internal use).
// Only workloads whose desired spec changed are marked for reconcile.
func (fa *FieldAgent) setLatestMicroservices(microservices []*models.Microservice) {
	marked := fa.replaceLatestMicroservices(microservices)
	if len(marked) == 0 {
		return
	}
	fa.mu.RLock()
	pm := fa.processManager
	fa.mu.RUnlock()
	if pm == nil {
		return
	}
	for _, uuid := range marked {
		pm.MarkReconcile(uuid)
	}
}

// setRegistries sets the registries (internal use)
func (fa *FieldAgent) setRegistries(registries []*models.Registry) {
	fa.microservicesMu.Lock()
	defer fa.microservicesMu.Unlock()

	fa.registries = make([]*models.Registry, len(registries))
	copy(fa.registries, registries)
}

// Clear clears all microservice data
func (fa *FieldAgent) Clear() {
	logging.LogDebug(moduleName, "Start microservice clear")
	fa.microservicesMu.Lock()
	defer fa.microservicesMu.Unlock()

	fa.latestMicroservices = make([]*models.Microservice, 0)
	fa.latestDesired = nil
	fa.currentMicroservices = make([]*models.Microservice, 0)
	fa.registries = make([]*models.Registry, 0)
	// Note: routes and configs are not stored in FieldAgent, they're passed to callbacks

	logging.LogDebug(moduleName, "Finished microservice clear")
}
