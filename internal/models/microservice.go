package models

import (
	"sync"
)

// DeleteLock is a global lock for microservice deletion operations
var DeleteLock = &sync.Mutex{}

// Microservice represents a microservice/container configuration
type Microservice struct {
	// Required fields
	MicroserviceUUID string `json:"microserviceUuid" yaml:"microserviceUuid"`
	ImageName        string `json:"imageName" yaml:"imageName"`

	// Optional fields
	PortMappings           []*PortMapping    `json:"portMappings,omitempty" yaml:"portMappings,omitempty"`
	Config                 *string           `json:"config,omitempty" yaml:"config,omitempty"`
	RunAsUser              *string           `json:"runAsUser,omitempty" yaml:"runAsUser,omitempty"`
	RunAsGroup             *string           `json:"runAsGroup,omitempty" yaml:"runAsGroup,omitempty"`
	Platform               *string           `json:"platform,omitempty" yaml:"platform,omitempty"`
	Runtime                *string           `json:"runtime,omitempty" yaml:"runtime,omitempty"`
	ContainerID            string            `json:"containerId" yaml:"containerId"`
	RegistryID             int               `json:"registryId" yaml:"registryId"`
	ContainerIPAddress     *string           `json:"containerIpAddress,omitempty" yaml:"containerIpAddress,omitempty"`
	Rebuild                bool              `json:"rebuild" yaml:"rebuild"`
	HostNetworkMode        bool              `json:"hostNetworkMode" yaml:"hostNetworkMode"`
	IsPrivileged           bool              `json:"isPrivileged" yaml:"isPrivileged"`
	LogSize                int64             `json:"logSize" yaml:"logSize"`
	VolumeMappings         []*VolumeMapping  `json:"volumeMappings,omitempty" yaml:"volumeMappings,omitempty"`
	IsUpdating             bool              `json:"isUpdating" yaml:"isUpdating"`
	EnvVars                []*EnvVar         `json:"envVars,omitempty" yaml:"envVars,omitempty"`
	Args                   []string          `json:"args,omitempty" yaml:"args,omitempty"`
	Entrypoint             *[]string         `json:"entrypoint,omitempty" yaml:"entrypoint,omitempty"`
	Commands               *[]string         `json:"commands,omitempty" yaml:"commands,omitempty"`
	CdiDevs                []string          `json:"cdiDevs,omitempty" yaml:"cdiDevs,omitempty"`
	Annotations            *string           `json:"annotations,omitempty" yaml:"annotations,omitempty"`
	CapAdd                 []string          `json:"capAdd,omitempty" yaml:"capAdd,omitempty"`
	CapDrop                []string          `json:"capDrop,omitempty" yaml:"capDrop,omitempty"`
	ExtraHosts             []string          `json:"extraHosts,omitempty" yaml:"extraHosts,omitempty"`
	IsRouter               bool              `json:"isRouter" yaml:"isRouter"`
	IsController           bool              `json:"isController" yaml:"isController"`
	IsSystem               bool              `json:"isSystem" yaml:"isSystem"`
	PidMode                *string           `json:"pidMode,omitempty" yaml:"pidMode,omitempty"`
	IpcMode                *string           `json:"ipcMode,omitempty" yaml:"ipcMode,omitempty"`
	MicroserviceName       string            `json:"name" yaml:"name"`
	ApplicationName        string            `json:"application" yaml:"application"`
	Labels                 map[string]string `json:"labels,omitempty" yaml:"labels,omitempty"`
	IsNats                 bool              `json:"isNats" yaml:"isNats"`
	Schedule               int               `json:"schedule" yaml:"schedule"`
	CPUSetCpus             *string           `json:"cpuSetCpus,omitempty" yaml:"cpuSetCpus,omitempty"`
	Cpus                   *float64          `json:"cpus,omitempty" yaml:"cpus,omitempty"`
	MemoryLimit            *int64            `json:"memoryLimit,omitempty" yaml:"memoryLimit,omitempty"`             // stored bytes; wire MiB (managed/local YAML)
	MemoryReservation      *int64            `json:"memoryReservation,omitempty" yaml:"memoryReservation,omitempty"` // stored bytes; wire MiB
	MemorySwap             *int64            `json:"memorySwap,omitempty" yaml:"memorySwap,omitempty"`               // -1 unlimited; else stored bytes
	ShmSize                *int64            `json:"shmSize,omitempty" yaml:"shmSize,omitempty"`                     // stored bytes; wire MiB
	ReadOnlyRootFilesystem bool              `json:"readOnlyRootFilesystem,omitempty" yaml:"readOnlyRootFilesystem,omitempty"`
	Sysctls                map[string]string `json:"sysctls,omitempty" yaml:"sysctls,omitempty"`
	Ulimits                map[string]Ulimit `json:"ulimits,omitempty" yaml:"ulimits,omitempty"`
	Devices                []DeviceMapping   `json:"devices,omitempty" yaml:"devices,omitempty"`
	Tmpfs                  []TmpfsMount      `json:"tmpfs,omitempty" yaml:"tmpfs,omitempty"`
	WorkingDir             *string           `json:"workingDir,omitempty" yaml:"workingDir,omitempty"`
	Models                 *ModelCatalog     `json:"models,omitempty" yaml:"models,omitempty"`
	Knowledge              *KnowledgeCatalog `json:"knowledge,omitempty" yaml:"knowledge,omitempty"`
	ServiceAccount         *ServiceAccount   `json:"serviceAccount,omitempty" yaml:"serviceAccount,omitempty"`

	// Internal state fields
	Delete            bool         `json:"delete" yaml:"delete"`
	DeleteWithCleanup bool         `json:"deleteWithCleanup" yaml:"deleteWithCleanup"`
	IsStuckInRestart  bool         `json:"isStuckInRestart" yaml:"isStuckInRestart"`
	Healthcheck       *Healthcheck `json:"healthcheck,omitempty" yaml:"healthcheck,omitempty"`

	// Mutex for thread-safe access to IsUpdating
	mu sync.RWMutex
}

// NewMicroservice creates a new Microservice with required fields
func NewMicroservice(microserviceUUID, imageName string) *Microservice {
	return &Microservice{
		MicroserviceUUID: microserviceUUID,
		ImageName:        imageName,
		ContainerID:      "",
		PortMappings:     make([]*PortMapping, 0),
		VolumeMappings:   make([]*VolumeMapping, 0),
		EnvVars:          make([]*EnvVar, 0),
		Args:             make([]string, 0),
		CdiDevs:          make([]string, 0),
		CapAdd:           make([]string, 0),
		CapDrop:          make([]string, 0),
		ExtraHosts:       make([]string, 0),
		Labels:           make(map[string]string),
	}
}

// IsUpdating returns whether the microservice is currently updating (thread-safe)
func (m *Microservice) GetIsUpdating() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.IsUpdating
}

// SetIsUpdating sets the updating state (thread-safe)
func (m *Microservice) SetIsUpdating(updating bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.IsUpdating = updating
}

// GetMemoryLimitMB returns the memory limit in MiB (from stored bytes).
func (m *Microservice) GetMemoryLimitMB() *int64 {
	if m.MemoryLimit == nil {
		return nil
	}
	mb := *m.MemoryLimit / (1024 * 1024)
	return &mb
}

// SetMemoryLimitMB sets the memory limit from MiB (managed/local wire) to stored bytes.
func (m *Microservice) SetMemoryLimitMB(memoryLimitMB *int64) {
	if memoryLimitMB == nil {
		m.MemoryLimit = nil
		return
	}
	bytes := *memoryLimitMB * 1024 * 1024
	m.MemoryLimit = &bytes
}

// GetMemoryReservationMB returns the memory reservation in MiB (from stored bytes).
func (m *Microservice) GetMemoryReservationMB() *int64 {
	return bytesToMiB(m.MemoryReservation)
}

// SetMemoryReservationMB sets the memory reservation from MiB to stored bytes.
func (m *Microservice) SetMemoryReservationMB(mb *int64) {
	m.MemoryReservation = mibToBytes(mb)
}

// GetShmSizeMB returns /dev/shm size in MiB (from stored bytes).
func (m *Microservice) GetShmSizeMB() *int64 {
	return bytesToMiB(m.ShmSize)
}

// SetShmSizeMB sets /dev/shm size from MiB to stored bytes.
func (m *Microservice) SetShmSizeMB(mb *int64) {
	m.ShmSize = mibToBytes(mb)
}

// GetMemorySwapMB returns memory+swap total in MiB, or -1 for unlimited.
func (m *Microservice) GetMemorySwapMB() *int64 {
	if m.MemorySwap == nil {
		return nil
	}
	if *m.MemorySwap == -1 {
		v := int64(-1)
		return &v
	}
	return bytesToMiB(m.MemorySwap)
}

// SetMemorySwapMB sets memory+swap from MiB (-1 stays unlimited).
func (m *Microservice) SetMemorySwapMB(mb *int64) {
	if mb == nil {
		m.MemorySwap = nil
		return
	}
	if *mb == -1 {
		v := int64(-1)
		m.MemorySwap = &v
		return
	}
	m.MemorySwap = mibToBytes(mb)
}

func bytesToMiB(bytes *int64) *int64 {
	if bytes == nil {
		return nil
	}
	mb := *bytes / (1024 * 1024)
	return &mb
}

func mibToBytes(mb *int64) *int64 {
	if mb == nil {
		return nil
	}
	bytes := *mb * 1024 * 1024
	return &bytes
}

// Equals checks if two Microservices are equal based on UUID
func (m *Microservice) Equals(other *Microservice) bool {
	if other == nil {
		return false
	}
	return m.MicroserviceUUID == other.MicroserviceUUID
}

// Validate validates the microservice configuration
func (m *Microservice) Validate() error {
	if m.MicroserviceUUID == "" {
		return &ValidationError{Field: "microserviceUuid", Message: "microserviceUuid is required"}
	}
	if m.ImageName == "" {
		return &ValidationError{Field: "imageName", Message: "imageName is required"}
	}
	if err := ValidateWorkloadCatalogs(m.Models, m.Knowledge, collectVolumeDestsFromMappings(m.VolumeMappings), collectTmpfsPaths(m.Tmpfs)); err != nil {
		return err
	}
	return validateMicroserviceContainerFields(m)
}

// ValidateWithModelSources runs Validate then checks catalog item names against a source lookup.
func (m *Microservice) ValidateWithModelSources(requiredSource string, lookup ModelSourceLookup) error {
	if err := m.Validate(); err != nil {
		return err
	}
	return ValidateCatalogModelSources(m.Models, requiredSource, lookup)
}

// ValidateCatalogApply runs Validate then rejects unknown or Failed catalog names for requiredSource.
func (m *Microservice) ValidateCatalogApply(requiredSource string, lookup ModelStatusLookup) error {
	if err := m.Validate(); err != nil {
		return err
	}
	return ValidateCatalogApply(m.Models, requiredSource, lookup)
}

// ValidateKnowledgeCatalogApply runs Validate then rejects unknown or Failed Knowledge names.
func (m *Microservice) ValidateKnowledgeCatalogApply(requiredSource string, lookup KnowledgeStatusLookup) error {
	if err := m.Validate(); err != nil {
		return err
	}
	return ValidateKnowledgeCatalogApply(m.Knowledge, requiredSource, lookup)
}
