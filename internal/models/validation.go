package models

import (
	"errors"
	"fmt"
)

// ValidationError represents a validation error with field information
type ValidationError struct {
	Field   string
	Message string
}

func (e *ValidationError) Error() string {
	return fmt.Sprintf("validation error in field '%s': %s", e.Field, e.Message)
}

// ValidatePortMapping validates a PortMapping
func ValidatePortMapping(p *PortMapping) error {
	if p == nil {
		return errors.New("port mapping cannot be nil")
	}
	if p.Outside <= 0 || p.Outside > 65535 {
		return &ValidationError{Field: "outside", Message: "port must be between 1 and 65535"}
	}
	if p.Inside <= 0 || p.Inside > 65535 {
		return &ValidationError{Field: "inside", Message: "port must be between 1 and 65535"}
	}
	return nil
}

// ValidateEnvVar validates an EnvVar
func ValidateEnvVar(e *EnvVar) error {
	if e == nil {
		return errors.New("env var cannot be nil")
	}
	if e.Key == "" {
		return &ValidationError{Field: "key", Message: "key is required"}
	}
	return nil
}

// ValidateVolumeMapping validates a VolumeMapping
func ValidateVolumeMapping(v *VolumeMapping) error {
	if v == nil {
		return errors.New("volume mapping cannot be nil")
	}
	if v.HostDestination == "" {
		return &ValidationError{Field: "hostDestination", Message: "host destination is required"}
	}
	if v.ContainerDestination == "" {
		return &ValidationError{Field: "containerDestination", Message: "container destination is required"}
	}
	if v.Type != VolumeMappingTypeVolume && v.Type != VolumeMappingTypeBind && v.Type != VolumeMappingTypeVolumeMount {
		return &ValidationError{Field: "type", Message: "invalid volume mapping type"}
	}
	return nil
}

// ValidateRoute validates a Route
func ValidateRoute(r *Route) error {
	if r == nil {
		return errors.New("route cannot be nil")
	}
	// Routes can be empty, so no validation needed
	return nil
}

// ValidateRegistry validates a Registry
func ValidateRegistry(r *Registry) error {
	if r == nil {
		return errors.New("registry cannot be nil")
	}
	if r.URL == "" {
		return &ValidationError{Field: "url", Message: "registry URL is required"}
	}
	if r.ID < 0 {
		return &ValidationError{Field: "id", Message: "registry ID must be non-negative"}
	}
	r.NormalizeDefaults()
	if !ValidRegistryType(r.Type) {
		return &ValidationError{Field: "type", Message: "registry type must be oci or hf"}
	}
	return nil
}

// ValidateMicroserviceStatus validates a MicroserviceStatus
func ValidateMicroserviceStatus(ms *MicroserviceStatus) error {
	if ms == nil {
		return errors.New("microservice status cannot be nil")
	}
	// Status enum is validated by type system
	return nil
}

// ValidateFieldAgentStatus validates a FieldAgentStatus
func ValidateFieldAgentStatus(fa *FieldAgentStatus) error {
	if fa == nil {
		return errors.New("field agent status cannot be nil")
	}
	// ControllerStatus enum is validated by type system
	return nil
}

// ValidateStatusReporterStatus validates a StatusReporterStatus
func ValidateStatusReporterStatus(sr *StatusReporterStatus) error {
	if sr == nil {
		return errors.New("status reporter status cannot be nil")
	}
	if sr.SystemTime < 0 {
		return &ValidationError{Field: "systemTime", Message: "system time must be non-negative"}
	}
	if sr.LastUpdate < 0 {
		return &ValidationError{Field: "lastUpdate", Message: "last update must be non-negative"}
	}
	return nil
}

// ValidateExecMessage validates an ExecMessage
func ValidateExecMessage(em *ExecMessage) error {
	if em == nil {
		return errors.New("exec message cannot be nil")
	}
	if em.Type > ExecMessageTypeControl {
		return &ValidationError{Field: "type", Message: "invalid exec message type"}
	}
	if em.MicroserviceUUID == "" {
		return &ValidationError{Field: "microserviceUuid", Message: "microservice UUID is required"}
	}
	return nil
}

// ValidateLogMessage validates a LogMessage
func ValidateLogMessage(lm *LogMessage) error {
	if lm == nil {
		return errors.New("log message cannot be nil")
	}
	if lm.Type < LogMessageTypeLogLine || lm.Type > LogMessageTypeLogError {
		return &ValidationError{Field: "type", Message: "invalid log message type"}
	}
	if lm.SessionID == "" {
		return &ValidationError{Field: "sessionId", Message: "session ID is required"}
	}
	return nil
}

// ValidateYamlConfig validates a YamlConfig
func ValidateYamlConfig(yc *YamlConfig) error {
	if yc == nil {
		return errors.New("yaml config cannot be nil")
	}
	if yc.CurrentProfile != "" && yc.Profiles != nil {
		if _, exists := yc.Profiles[yc.CurrentProfile]; !exists {
			return &ValidationError{Field: "currentProfile", Message: fmt.Sprintf("current profile '%s' does not exist in profiles", yc.CurrentProfile)}
		}
	}
	return nil
}
