package processmanager

import (
	"errors"
	"fmt"

	"github.com/eclipse-iofog/edgelet/internal/models"
	"github.com/eclipse-iofog/edgelet/internal/store"
)

// resolveRegistryForMicroservice returns the registry for ms.RegistryID.
// Managed (Pot) workloads resolve via MicroserviceManagerInterface (controller snapshot).
// Local workloads fall back to local_registries when the controller snapshot has no match.
func resolveRegistryForMicroservice(msm MicroserviceManagerInterface, ms *models.Microservice) (*models.Registry, error) {
	if ms == nil {
		return nil, errors.New("microservice is nil")
	}
	if ms.RegistryID <= 0 {
		return nil, fmt.Errorf("registry is not valid %d", ms.RegistryID)
	}
	var resolved *models.Registry
	if msm != nil {
		if reg := msm.GetRegistry(ms.RegistryID); reg != nil {
			resolved = reg
		}
	}
	if resolved == nil {
		if reg, err := store.GetInstance().GetLocalRegistry(ms.RegistryID); err == nil && reg != nil {
			resolved = reg
		}
	}
	if resolved == nil {
		return nil, fmt.Errorf("registry is not valid %d", ms.RegistryID)
	}
	if err := models.RequireOCIForImagePull(resolved); err != nil {
		return nil, err
	}
	return resolved, nil
}

func registryURLFromRegistry(registry *models.Registry) string {
	if registry == nil {
		return ""
	}
	return registry.URL
}
