package models

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
)

// Helper functions for formatting
func formatFloat32(f float32) string {
	return fmt.Sprintf("%.2f", f)
}

func formatInt64(i int64) string {
	return fmt.Sprintf("%d", i)
}

// ProcessManagerStatus represents the Process Manager status
type ProcessManagerStatus struct {
	mu                        sync.RWMutex
	RunningMicroservicesCount int                            `json:"runningMicroservicesCount" yaml:"runningMicroservicesCount"` // Number of running microservices
	MicroservicesStatus       map[string]*MicroserviceStatus `json:"microservicesStatus" yaml:"microservicesStatus"`             // Status of each microservice
	RegistriesStatus          map[int]string                 `json:"registriesStatus" yaml:"registriesStatus"`                   // Status of each registry (registry ID -> status)
}

// NewProcessManagerStatus creates a new ProcessManagerStatus
func NewProcessManagerStatus() *ProcessManagerStatus {
	return &ProcessManagerStatus{
		MicroservicesStatus: make(map[string]*MicroserviceStatus),
		RegistriesStatus:    make(map[int]string),
	}
}

// SetRunningMicroservicesCount sets the running microservices count and returns the status for chaining
func (p *ProcessManagerStatus) SetRunningMicroservicesCount(count int) *ProcessManagerStatus {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.RunningMicroservicesCount = count
	return p
}

// SetMicroservicesStatus sets the status of a microservice and returns the status for chaining
func (p *ProcessManagerStatus) SetMicroservicesStatus(microserviceUUID string, status *MicroserviceStatus) *ProcessManagerStatus {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.MicroservicesStatus[microserviceUUID] = status
	return p
}

// GetMicroserviceStatus gets the status of a microservice, creating it if it doesn't exist
func (p *ProcessManagerStatus) GetMicroserviceStatus(microserviceUUID string) *MicroserviceStatus {
	p.mu.Lock()
	defer p.mu.Unlock()
	if status, exists := p.MicroservicesStatus[microserviceUUID]; exists {
		return status
	}
	status := NewMicroserviceStatus()
	p.MicroservicesStatus[microserviceUUID] = status
	return status
}

// SetMicroservicesState sets the state of a microservice and returns the status for chaining
func (p *ProcessManagerStatus) SetMicroservicesState(microserviceUUID string, state MicroserviceState) *ProcessManagerStatus {
	p.mu.Lock()
	defer p.mu.Unlock()
	status := p.MicroservicesStatus[microserviceUUID]
	if status == nil {
		status = NewMicroserviceStatus()
		p.MicroservicesStatus[microserviceUUID] = status
	}
	status.Status = state
	return p
}

// SetMicroservicesStatePercentage sets the state percentage of a microservice and returns the status for chaining
func (p *ProcessManagerStatus) SetMicroservicesStatePercentage(microserviceUUID string, percentage float32) *ProcessManagerStatus {
	p.mu.Lock()
	defer p.mu.Unlock()
	status := p.MicroservicesStatus[microserviceUUID]
	if status == nil {
		status = NewMicroserviceStatus()
		p.MicroservicesStatus[microserviceUUID] = status
	}
	status.Percentage = percentage
	return p
}

// SetMicroservicesStatusErrorMessage sets the error message for a microservice and returns the status for chaining
func (p *ProcessManagerStatus) SetMicroservicesStatusErrorMessage(microserviceUUID string, message string) *ProcessManagerStatus {
	p.mu.Lock()
	defer p.mu.Unlock()
	status := p.MicroservicesStatus[microserviceUUID]
	if status == nil {
		status = NewMicroserviceStatus()
		p.MicroservicesStatus[microserviceUUID] = status
	}
	// Keep tri-state semantics:
	// nil pointer => no update/unspecified, non-nil empty string => explicit clear.
	status.ErrorMessage = &message
	return p
}

// SetMicroservicesHealthStatus sets the health status for a microservice (e.g. "healthy", "unhealthy").
func (p *ProcessManagerStatus) SetMicroservicesHealthStatus(microserviceUUID string, healthStatus *string) *ProcessManagerStatus {
	p.mu.Lock()
	defer p.mu.Unlock()
	status := p.MicroservicesStatus[microserviceUUID]
	if status == nil {
		status = NewMicroserviceStatus()
		p.MicroservicesStatus[microserviceUUID] = status
	}
	status.HealthStatus = healthStatus
	return p
}

// RemoveNotRunningMicroserviceStatus removes microservices that are not running
func (p *ProcessManagerStatus) RemoveNotRunningMicroserviceStatus() {
	p.mu.Lock()
	defer p.mu.Unlock()
	for uuid, status := range p.MicroservicesStatus {
		if status.Status == MicroserviceStateUnknown || status.Status == MicroserviceStateDeleted {
			delete(p.MicroservicesStatus, uuid)
		}
	}
}

// PruneMicroserviceStatus removes entries matching predicate.
func (p *ProcessManagerStatus) PruneMicroserviceStatus(predicate func(uuid string, status *MicroserviceStatus) bool) {
	if predicate == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	for uuid, status := range p.MicroservicesStatus {
		if predicate(uuid, status) {
			delete(p.MicroservicesStatus, uuid)
		}
	}
}

// ClearMicroserviceStatuses clears all tracked microservice statuses and resets count.
func (p *ProcessManagerStatus) ClearMicroserviceStatuses() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.MicroservicesStatus = make(map[string]*MicroserviceStatus)
	p.RunningMicroservicesCount = 0
}

// GetJSONMicroservicesStatus returns the microservices status as a JSON string
func (p *ProcessManagerStatus) GetJSONMicroservicesStatus() string {
	p.mu.RLock()
	defer p.mu.RUnlock()

	type MicroserviceStatusJSON struct {
		ID                string   `json:"id"`
		Status            string   `json:"status"`
		Percentage        float32  `json:"percentage"`
		ContainerID       string   `json:"containerId,omitempty"`
		PodID             string   `json:"podId,omitempty"`
		StartTime         int64    `json:"startTime,omitempty"`
		OperatingDuration int64    `json:"operatingDuration,omitempty"`
		CPUUsage          string   `json:"cpuUsage,omitempty"`
		MemoryUsage       string   `json:"memoryUsage,omitempty"`
		IPAddress         string   `json:"ipAddress,omitempty"`
		HealthStatus      string   `json:"healthStatus,omitempty"`
		ExecSessionIDs    []string `json:"execSessionIds,omitempty"`
		ErrorMessage      *string  `json:"errorMessage,omitempty"`
	}

	statuses := make([]MicroserviceStatusJSON, 0, len(p.MicroservicesStatus))
	for uuid, status := range p.MicroservicesStatus {
		msStatus := MicroserviceStatusJSON{
			ID:         uuid,
			Status:     string(status.Status),
			Percentage: status.Percentage,
		}

		if status.ContainerID != "" {
			msStatus.ContainerID = status.ContainerID
			if podID := strings.TrimSpace(status.PodID); podID != "" {
				msStatus.PodID = podID
			}
			msStatus.StartTime = status.StartTime
			msStatus.OperatingDuration = status.GetOperatingDuration()
			if status.CPUUsage > 0 {
				msStatus.CPUUsage = formatFloat32(status.CPUUsage)
			}
			if status.MemoryUsage > 0 {
				msStatus.MemoryUsage = formatInt64(status.MemoryUsage)
			}
			if status.IPAddress != nil {
				msStatus.IPAddress = *status.IPAddress
			}
			if status.HealthStatus != nil {
				msStatus.HealthStatus = *status.HealthStatus
			}
			msStatus.ExecSessionIDs = status.GetExecSessionIDs()
		}

		if status.ErrorMessage != nil {
			msStatus.ErrorMessage = status.ErrorMessage
		}

		statuses = append(statuses, msStatus)
	}

	jsonData, err := json.Marshal(statuses)
	if err != nil {
		return "[]"
	}
	return string(jsonData)
}

// GetJSONRegistriesStatus returns the registries status as a JSON string
func (p *ProcessManagerStatus) GetJSONRegistriesStatus() string {
	p.mu.RLock()
	defer p.mu.RUnlock()

	type RegistryStatusJSON struct {
		ID         int    `json:"id"`
		LinkStatus string `json:"linkStatus"`
	}

	statuses := make([]RegistryStatusJSON, 0, len(p.RegistriesStatus))
	for id, status := range p.RegistriesStatus {
		statuses = append(statuses, RegistryStatusJSON{
			ID:         id,
			LinkStatus: status,
		})
	}

	jsonData, err := json.Marshal(statuses)
	if err != nil {
		return "[]"
	}
	return string(jsonData)
}

// SetRegistriesStatus sets the status of a registry
func (p *ProcessManagerStatus) SetRegistriesStatus(registryID int, status string) *ProcessManagerStatus {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.RegistriesStatus[registryID] = status
	return p
}

// GetRegistriesStatus returns the registries status map
func (p *ProcessManagerStatus) GetRegistriesStatus() map[int]string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	result := make(map[int]string)
	for k, v := range p.RegistriesStatus {
		result[k] = v
	}
	return result
}
