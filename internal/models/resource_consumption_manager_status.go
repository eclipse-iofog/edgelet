package models

// ResourceConsumptionManagerStatus represents the Resource Consumption Manager status.
// CPUUsage and MemoryUsage are edgelet stack totals (control plane + embedded runtime when available).
type ResourceConsumptionManagerStatus struct {
	AgentCPUPercent        float64 `json:"agentCpuPercent" yaml:"agentCpuPercent"`
	AgentMemoryMiB         float64 `json:"agentMemoryMiB" yaml:"agentMemoryMiB"`
	RuntimeCPUPercent      float64 `json:"runtimeCpuPercent" yaml:"runtimeCpuPercent"`
	RuntimeMemoryMiB       float64 `json:"runtimeMemoryMiB" yaml:"runtimeMemoryMiB"`
	RuntimeAvailable       bool    `json:"runtimeAvailable" yaml:"runtimeAvailable"`
	RuntimeDegraded        bool    `json:"runtimeDegraded" yaml:"runtimeDegraded"`
	RuntimeTracked         bool    `json:"runtimeTracked" yaml:"runtimeTracked"`
	RuntimePIDCount        int     `json:"runtimePidCount" yaml:"runtimePidCount"`
	EdgeletTotalCPUPercent float64 `json:"edgeletTotalCpuPercent" yaml:"edgeletTotalCpuPercent"`
	EdgeletTotalMemoryMiB  float64 `json:"edgeletTotalMemoryMiB" yaml:"edgeletTotalMemoryMiB"`

	MemoryUsage         float64 `json:"memoryUsage" yaml:"memoryUsage"`                 // Edgelet stack memory in MiB (alias of EdgeletTotalMemoryMiB)
	DiskUsage           float64 `json:"diskUsage" yaml:"diskUsage"`                     // Disk usage in GiB
	CPUUsage            float64 `json:"cpuUsage" yaml:"cpuUsage"`                       // Edgelet stack CPU percent (alias of EdgeletTotalCPUPercent)
	MemoryViolation     bool    `json:"memoryViolation" yaml:"memoryViolation"`         // Whether memory limit is violated
	DiskViolation       bool    `json:"diskViolation" yaml:"diskViolation"`             // Whether disk limit is violated
	CPUViolation        bool    `json:"cpuViolation" yaml:"cpuViolation"`               // Whether CPU limit is violated
	AvailableMemory     int64   `json:"availableMemory" yaml:"availableMemory"`         // System available memory in bytes
	SystemTotalMemory   int64   `json:"systemTotalMemory" yaml:"systemTotalMemory"`     // Host RAM capacity in bytes
	SystemCpus          int     `json:"systemCpus" yaml:"systemCpus"`                   // Logical CPU count
	SystemOs            string  `json:"systemOs" yaml:"systemOs"`                       // Host OS family (linux, darwin, windows, …)
	SystemOsVersion     string  `json:"systemOsVersion" yaml:"systemOsVersion"`         // Distro / release display string
	SystemKernelVersion string  `json:"systemKernelVersion" yaml:"systemKernelVersion"` // Linux kernel version; "" on non-linux
	TotalCPU            float64 `json:"totalCpu" yaml:"totalCpu"`                       // Host CPU usage percentage
	AvailableDisk       int64   `json:"availableDisk" yaml:"availableDisk"`             // Free space on diskDirectory filesystem in bytes
	TotalDiskSpace      int64   `json:"totalDiskSpace" yaml:"totalDiskSpace"`           // Total diskDirectory filesystem size in bytes
}

// NewResourceConsumptionManagerStatus creates a new ResourceConsumptionManagerStatus
func NewResourceConsumptionManagerStatus() *ResourceConsumptionManagerStatus {
	return &ResourceConsumptionManagerStatus{
		RuntimeAvailable: true,
	}
}

// SetMemoryUsage sets the memory usage and returns the status for chaining
func (r *ResourceConsumptionManagerStatus) SetMemoryUsage(memoryUsage float64) *ResourceConsumptionManagerStatus {
	r.MemoryUsage = memoryUsage
	return r
}

// SetDiskUsage sets the disk usage and returns the status for chaining
func (r *ResourceConsumptionManagerStatus) SetDiskUsage(diskUsage float64) *ResourceConsumptionManagerStatus {
	r.DiskUsage = diskUsage
	return r
}

// SetCPUUsage sets the CPU usage and returns the status for chaining
func (r *ResourceConsumptionManagerStatus) SetCPUUsage(cpuUsage float64) *ResourceConsumptionManagerStatus {
	r.CPUUsage = cpuUsage
	return r
}

// SetMemoryViolation sets the memory violation flag and returns the status for chaining
func (r *ResourceConsumptionManagerStatus) SetMemoryViolation(violation bool) *ResourceConsumptionManagerStatus {
	r.MemoryViolation = violation
	return r
}

// SetDiskViolation sets the disk violation flag and returns the status for chaining
func (r *ResourceConsumptionManagerStatus) SetDiskViolation(violation bool) *ResourceConsumptionManagerStatus {
	r.DiskViolation = violation
	return r
}

// SetCPUViolation sets the CPU violation flag and returns the status for chaining
func (r *ResourceConsumptionManagerStatus) SetCPUViolation(violation bool) *ResourceConsumptionManagerStatus {
	r.CPUViolation = violation
	return r
}

// SetAvailableMemory sets the available memory and returns the status for chaining
func (r *ResourceConsumptionManagerStatus) SetAvailableMemory(availableMemory int64) *ResourceConsumptionManagerStatus {
	r.AvailableMemory = availableMemory
	return r
}

// SetTotalCPU sets the total CPU usage and returns the status for chaining
func (r *ResourceConsumptionManagerStatus) SetTotalCPU(totalCPU float64) *ResourceConsumptionManagerStatus {
	r.TotalCPU = totalCPU
	return r
}

// SetAvailableDisk sets the available disk space and returns the status for chaining
func (r *ResourceConsumptionManagerStatus) SetAvailableDisk(availableDisk int64) *ResourceConsumptionManagerStatus {
	r.AvailableDisk = availableDisk
	return r
}

// SetTotalDiskSpace sets the total disk space and returns the status for chaining
func (r *ResourceConsumptionManagerStatus) SetTotalDiskSpace(totalDiskSpace int64) *ResourceConsumptionManagerStatus {
	r.TotalDiskSpace = totalDiskSpace
	return r
}
