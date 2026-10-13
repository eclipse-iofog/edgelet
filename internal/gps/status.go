package gps

import "sync"

// HealthStatus represents the GPS health status
type HealthStatus string

const (
	HealthStatusHealthy     HealthStatus = "HEALTHY"
	HealthStatusNoFix       HealthStatus = "NO_FIX"
	HealthStatusDeviceError HealthStatus = "DEVICE_ERROR"
	HealthStatusIPError     HealthStatus = "IP_ERROR"
	HealthStatusOff         HealthStatus = "OFF"
)

// Status represents the GPS status
type Status struct {
	mu           sync.RWMutex
	healthStatus HealthStatus
}

// NewStatus creates a new GPS status
func NewStatus() *Status {
	return &Status{
		healthStatus: HealthStatusOff,
	}
}

// GetHealthStatus returns the health status
func (s *Status) GetHealthStatus() HealthStatus {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.healthStatus
}

// SetHealthStatus sets the health status
func (s *Status) SetHealthStatus(status HealthStatus) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.healthStatus = status
}
