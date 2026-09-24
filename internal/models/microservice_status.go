package models

import "time"

// MicroserviceStatus represents the status of a microservice
type MicroserviceStatus struct {
	Status         MicroserviceState `json:"status" yaml:"status"`
	StartTime      int64             `json:"startTime" yaml:"startTime"`
	CPUUsage       float32           `json:"cpuUsage" yaml:"cpuUsage"`
	MemoryUsage    int64             `json:"memoryUsage" yaml:"memoryUsage"`
	ContainerID    string            `json:"containerId" yaml:"containerId"`
	PodID          string            `json:"podId,omitempty" yaml:"podId,omitempty"`
	Percentage     float32           `json:"percentage" yaml:"percentage"`
	ErrorMessage   *string           `json:"errorMessage,omitempty" yaml:"errorMessage,omitempty"`
	LastError      string            `json:"lastError,omitempty" yaml:"lastError,omitempty"`
	LastErrorAt    int64             `json:"lastErrorAt,omitempty" yaml:"lastErrorAt,omitempty"`
	RestartCount   int               `json:"restartCount,omitempty" yaml:"restartCount,omitempty"`
	IPAddress      *string           `json:"ipAddress,omitempty" yaml:"ipAddress,omitempty"`
	ExecSessionIDs []string          `json:"execSessionIds,omitempty" yaml:"execSessionIds,omitempty"`
	HealthStatus   *string           `json:"healthStatus,omitempty" yaml:"healthStatus,omitempty"`
}

// NewMicroserviceStatus creates a new MicroserviceStatus with UNKNOWN state
func NewMicroserviceStatus() *MicroserviceStatus {
	return &MicroserviceStatus{
		Status:         MicroserviceStateUnknown,
		ContainerID:    "",
		ExecSessionIDs: make([]string, 0),
	}
}

// NewMicroserviceStatusWithState creates a new MicroserviceStatus with the given state
func NewMicroserviceStatusWithState(status MicroserviceState) *MicroserviceStatus {
	return &MicroserviceStatus{
		Status:         status,
		ContainerID:    "",
		ExecSessionIDs: make([]string, 0),
	}
}

// GetOperatingDuration returns the operating duration in milliseconds
func (m *MicroserviceStatus) GetOperatingDuration() int64 {
	if m.StartTime == 0 {
		return 0
	}
	return time.Now().UnixMilli() - m.StartTime
}

// AddExecSessionID adds an exec session ID
func (m *MicroserviceStatus) AddExecSessionID(execSessionID string) {
	if m.ExecSessionIDs == nil {
		m.ExecSessionIDs = make([]string, 0)
	}
	m.ExecSessionIDs = append(m.ExecSessionIDs, execSessionID)
}

// RemoveExecSessionID removes an exec session ID
func (m *MicroserviceStatus) RemoveExecSessionID(execSessionID string) {
	if m.ExecSessionIDs == nil {
		return
	}
	for i, id := range m.ExecSessionIDs {
		if id == execSessionID {
			m.ExecSessionIDs = append(m.ExecSessionIDs[:i], m.ExecSessionIDs[i+1:]...)
			return
		}
	}
}

// GetExecSessionIDs returns the exec session IDs (never nil)
func (m *MicroserviceStatus) GetExecSessionIDs() []string {
	if m.ExecSessionIDs == nil {
		return make([]string, 0)
	}
	return m.ExecSessionIDs
}

// Equals checks if two MicroserviceStatuses are equal based on status
func (m *MicroserviceStatus) Equals(other *MicroserviceStatus) bool {
	if other == nil {
		return false
	}
	return m.Status == other.Status
}
