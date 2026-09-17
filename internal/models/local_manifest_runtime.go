package models

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/eclipse-iofog/edgelet/internal/workloadmeta"
)

// BuildMicroserviceFromLocalManifest converts a local deploy manifest into
// a runtime microservice model used by the container engine.
func BuildMicroserviceFromLocalManifest(doc *LocalDeployManifest, deploymentID, image string) *Microservice {
	ms := NewMicroservice(deploymentID, image)
	ms.MicroserviceName = doc.Metadata.Name
	ms.ApplicationName = workloadmeta.LocalDeployApplicationName
	ms.Labels = cloneManifestLabels(doc.Metadata.Labels)
	ms.RegistryID = 2
	if doc.Spec.Registry != nil && *doc.Spec.Registry > 0 {
		ms.RegistryID = *doc.Spec.Registry
	}
	ms.HostNetworkMode = doc.Spec.Container.HostNetworkMode
	ms.IsPrivileged = doc.Spec.Container.IsPrivileged
	ms.ReadOnlyRootFilesystem = doc.Spec.Container.ReadOnlyRootFilesystem
	ms.Entrypoint = CloneStringSlicePtr(doc.Spec.Container.Entrypoint)
	ms.Commands = CloneStringSlicePtr(doc.Spec.Container.Commands)
	if !UsesImageDefault(doc.Spec.Container.Commands) {
		ms.Args = append(ms.Args, (*doc.Spec.Container.Commands)...)
	}
	ms.Models = doc.Spec.Models.Clone()
	if len(doc.Spec.Container.Sysctls) > 0 {
		ms.Sysctls = make(map[string]string, len(doc.Spec.Container.Sysctls))
		for k, v := range doc.Spec.Container.Sysctls {
			ms.Sysctls[k] = v
		}
	}
	if len(doc.Spec.Container.Ulimits) > 0 {
		ms.Ulimits = make(map[string]Ulimit, len(doc.Spec.Container.Ulimits))
		for k, v := range doc.Spec.Container.Ulimits {
			ms.Ulimits[k] = v
		}
	}
	if len(doc.Spec.Container.Devices) > 0 {
		ms.Devices = append([]DeviceMapping(nil), doc.Spec.Container.Devices...)
	}
	if len(doc.Spec.Container.Tmpfs) > 0 {
		ms.Tmpfs = append([]TmpfsMount(nil), doc.Spec.Container.Tmpfs...)
	}
	ms.CapAdd = append(ms.CapAdd, doc.Spec.Container.CapAdd...)
	ms.CapDrop = append(ms.CapDrop, doc.Spec.Container.CapDrop...)
	ms.CdiDevs = append(ms.CdiDevs, doc.Spec.Container.CDIDevices...)
	ms.Schedule = doc.Spec.Schedule
	if strings.TrimSpace(doc.Spec.Container.RunAsUser) != "" {
		runAs := strings.TrimSpace(doc.Spec.Container.RunAsUser)
		ms.RunAsUser = &runAs
	}
	if strings.TrimSpace(doc.Spec.Container.RunAsGroup) != "" {
		group := strings.TrimSpace(doc.Spec.Container.RunAsGroup)
		ms.RunAsGroup = &group
	}
	if strings.TrimSpace(doc.Spec.Container.WorkingDir) != "" {
		wd := strings.TrimSpace(doc.Spec.Container.WorkingDir)
		ms.WorkingDir = &wd
	}
	if strings.TrimSpace(doc.Spec.Container.Runtime) != "" {
		runtime := strings.TrimSpace(doc.Spec.Container.Runtime)
		ms.Runtime = &runtime
	}
	if strings.TrimSpace(doc.Spec.Container.Platform) != "" {
		platform := strings.TrimSpace(doc.Spec.Container.Platform)
		ms.Platform = &platform
	}
	if strings.TrimSpace(doc.Spec.Container.IpcMode) != "" {
		ipcMode := strings.TrimSpace(doc.Spec.Container.IpcMode)
		ms.IpcMode = &ipcMode
	}
	if strings.TrimSpace(doc.Spec.Container.PidMode) != "" {
		pidMode := strings.TrimSpace(doc.Spec.Container.PidMode)
		ms.PidMode = &pidMode
	}
	if strings.TrimSpace(doc.Spec.Container.CPUSetCpus) != "" {
		cpuSet := strings.TrimSpace(doc.Spec.Container.CPUSetCpus)
		ms.CPUSetCpus = &cpuSet
	}
	if doc.Spec.Container.MemoryLimit > 0 {
		limitMiB := doc.Spec.Container.MemoryLimit
		ms.SetMemoryLimitMB(&limitMiB)
	}
	if doc.Spec.Container.Cpus != nil {
		cpus := *doc.Spec.Container.Cpus
		ms.Cpus = &cpus
	}
	ms.SetMemoryReservationMB(doc.Spec.Container.MemoryReservation)
	ms.SetMemorySwapMB(doc.Spec.Container.MemorySwap)
	ms.SetShmSizeMB(doc.Spec.Container.ShmSize)
	for _, envVar := range doc.Spec.Container.Env {
		ms.EnvVars = append(ms.EnvVars, &EnvVar{Key: envVar.Key, Value: envVar.Value})
	}
	for _, volume := range doc.Spec.Container.Volumes {
		volumeType := strings.ToUpper(strings.TrimSpace(volume.Type))
		if volumeType == "" {
			volumeType = string(VolumeMappingTypeBind)
		}
		ms.VolumeMappings = append(ms.VolumeMappings, &VolumeMapping{
			HostDestination:      volume.HostDestination,
			ContainerDestination: volume.ContainerDestination,
			AccessMode:           volume.AccessMode,
			Type:                 VolumeMappingType(volumeType),
		})
	}
	for _, port := range doc.Spec.Container.Ports {
		ms.PortMappings = append(ms.PortMappings, &PortMapping{
			Inside:  port.Internal,
			Outside: port.External,
			UDP:     strings.EqualFold(strings.TrimSpace(port.Protocol), "udp"),
		})
	}
	for _, host := range doc.Spec.Container.ExtraHosts {
		if strings.TrimSpace(host.Name) == "" || strings.TrimSpace(host.Address) == "" {
			continue
		}
		ms.ExtraHosts = append(ms.ExtraHosts, strings.TrimSpace(host.Name)+":"+strings.TrimSpace(host.Address))
	}
	ms.Healthcheck = healthcheckFromManifest(localHealthCheckSpec{
		Test:        doc.Spec.Container.HealthCheck.Test,
		Interval:    doc.Spec.Container.HealthCheck.Interval,
		Timeout:     doc.Spec.Container.HealthCheck.Timeout,
		StartPeriod: doc.Spec.Container.HealthCheck.StartPeriod,
		Retries:     doc.Spec.Container.HealthCheck.Retries,
	})
	ms.Annotations = annotationsJSON(doc.Spec.Container.Annotations)
	return ms
}

// LocalDeployNeedsRecreate reports whether a local re-apply must replace the
// running container. Catalog item add/remove does not recreate when bindPath
// and catalog permissions are unchanged.
func LocalDeployNeedsRecreate(prev, next *Microservice) bool {
	if prev == nil || next == nil {
		return prev != next
	}
	if CatalogMountNeedsRecreate(prev.Models, next.Models) {
		return true
	}
	a, err := localDeployRecreateSnapshot(prev)
	if err != nil {
		return true
	}
	b, err := localDeployRecreateSnapshot(next)
	if err != nil {
		return true
	}
	return a != b
}

type localHealthCheckSpec struct {
	Test        []string
	Interval    int
	Timeout     int
	StartPeriod int
	Retries     int
}

func healthcheckFromManifest(hc localHealthCheckSpec) *Healthcheck {
	if len(hc.Test) == 0 && hc.Interval == 0 && hc.Timeout == 0 && hc.StartPeriod == 0 && hc.Retries == 0 {
		return nil
	}
	out := &Healthcheck{Test: append([]string(nil), hc.Test...)}
	if hc.Interval != 0 {
		v := int64(hc.Interval)
		out.Interval = &v
	}
	if hc.Timeout != 0 {
		v := int64(hc.Timeout)
		out.Timeout = &v
	}
	if hc.StartPeriod != 0 {
		v := int64(hc.StartPeriod)
		out.StartPeriod = &v
	}
	if hc.Retries != 0 {
		v := hc.Retries
		out.Retries = &v
	}
	return out
}

func annotationsJSON(in map[string]any) *string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		key := strings.TrimSpace(k)
		if key == "" {
			continue
		}
		out[key] = annotationString(v)
	}
	if len(out) == 0 {
		return nil
	}
	raw, err := json.Marshal(out)
	if err != nil {
		return nil
	}
	s := string(raw)
	return &s
}

func annotationString(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case nil:
		return ""
	default:
		raw, err := json.Marshal(t)
		if err != nil {
			return fmt.Sprint(t)
		}
		var s string
		if err := json.Unmarshal(raw, &s); err == nil {
			return s
		}
		return string(raw)
	}
}

type localRecreateSnapshot struct {
	ImageName              string            `json:"imageName"`
	RegistryID             int               `json:"registryId"`
	HostNetworkMode        bool              `json:"hostNetworkMode"`
	IsPrivileged           bool              `json:"isPrivileged"`
	ReadOnlyRootFilesystem bool              `json:"readOnlyRootFilesystem"`
	Entrypoint             []string          `json:"entrypoint,omitempty"`
	Commands               []string          `json:"commands,omitempty"`
	Sysctls                map[string]string `json:"sysctls,omitempty"`
	Ulimits                map[string]Ulimit `json:"ulimits,omitempty"`
	Devices                []DeviceMapping   `json:"devices,omitempty"`
	Tmpfs                  []TmpfsMount      `json:"tmpfs,omitempty"`
	CapAdd                 []string          `json:"capAdd,omitempty"`
	CapDrop                []string          `json:"capDrop,omitempty"`
	CdiDevs                []string          `json:"cdiDevs,omitempty"`
	ExtraHosts             []string          `json:"extraHosts,omitempty"`
	Schedule               int               `json:"schedule,omitempty"`
	RunAsUser              string            `json:"runAsUser,omitempty"`
	RunAsGroup             string            `json:"runAsGroup,omitempty"`
	WorkingDir             string            `json:"workingDir,omitempty"`
	Runtime                string            `json:"runtime,omitempty"`
	Platform               string            `json:"platform,omitempty"`
	IpcMode                string            `json:"ipcMode,omitempty"`
	PidMode                string            `json:"pidMode,omitempty"`
	CPUSetCpus             string            `json:"cpuSetCpus,omitempty"`
	Cpus                   *float64          `json:"cpus,omitempty"`
	MemoryLimit            *int64            `json:"memoryLimit,omitempty"`
	MemoryReservation      *int64            `json:"memoryReservation,omitempty"`
	MemorySwap             *int64            `json:"memorySwap,omitempty"`
	ShmSize                *int64            `json:"shmSize,omitempty"`
	EnvVars                []*EnvVar         `json:"envVars,omitempty"`
	VolumeMappings         []*VolumeMapping  `json:"volumeMappings,omitempty"`
	PortMappings           []*PortMapping    `json:"portMappings,omitempty"`
	Labels                 map[string]string `json:"labels,omitempty"`
	Annotations            *string           `json:"annotations,omitempty"`
	Healthcheck            *Healthcheck      `json:"healthcheck,omitempty"`
	CatalogBindPath        string            `json:"catalogBindPath,omitempty"`
	CatalogPermissions     string            `json:"catalogPermissions,omitempty"`
}

func localDeployRecreateSnapshot(ms *Microservice) (string, error) {
	if ms == nil {
		return "", nil
	}
	snap := localRecreateSnapshot{
		ImageName:              ms.ImageName,
		RegistryID:             ms.RegistryID,
		HostNetworkMode:        ms.HostNetworkMode,
		IsPrivileged:           ms.IsPrivileged,
		ReadOnlyRootFilesystem: ms.ReadOnlyRootFilesystem,
		Sysctls:                cloneStringMapSnapshot(ms.Sysctls),
		Ulimits:                cloneUlimitMapSnapshot(ms.Ulimits),
		Devices:                append([]DeviceMapping(nil), ms.Devices...),
		Tmpfs:                  append([]TmpfsMount(nil), ms.Tmpfs...),
		CapAdd:                 append([]string(nil), ms.CapAdd...),
		CapDrop:                append([]string(nil), ms.CapDrop...),
		CdiDevs:                append([]string(nil), ms.CdiDevs...),
		ExtraHosts:             append([]string(nil), ms.ExtraHosts...),
		Schedule:               ms.Schedule,
		Cpus:                   cloneFloat64Snapshot(ms.Cpus),
		MemoryLimit:            cloneInt64Snapshot(ms.MemoryLimit),
		MemoryReservation:      cloneInt64Snapshot(ms.MemoryReservation),
		MemorySwap:             cloneInt64Snapshot(ms.MemorySwap),
		ShmSize:                cloneInt64Snapshot(ms.ShmSize),
		EnvVars:                append([]*EnvVar(nil), ms.EnvVars...),
		VolumeMappings:         append([]*VolumeMapping(nil), ms.VolumeMappings...),
		PortMappings:           append([]*PortMapping(nil), ms.PortMappings...),
		Labels:                 cloneStringMapSnapshot(ms.Labels),
		Annotations:            ms.Annotations,
		Healthcheck:            ms.Healthcheck,
	}
	if !UsesImageDefault(ms.Entrypoint) {
		snap.Entrypoint = append([]string{}, (*ms.Entrypoint)...)
	}
	if !UsesImageDefault(ms.Commands) {
		snap.Commands = append([]string{}, (*ms.Commands)...)
	}
	snap.RunAsUser = derefTrim(ms.RunAsUser)
	snap.RunAsGroup = derefTrim(ms.RunAsGroup)
	snap.WorkingDir = derefTrim(ms.WorkingDir)
	snap.Runtime = derefTrim(ms.Runtime)
	snap.Platform = derefTrim(ms.Platform)
	snap.IpcMode = derefTrim(ms.IpcMode)
	snap.PidMode = derefTrim(ms.PidMode)
	snap.CPUSetCpus = derefTrim(ms.CPUSetCpus)
	if ms.Models.HasItems() {
		cloned := ms.Models.Clone()
		cloned.NormalizeDefaults()
		snap.CatalogBindPath = cloned.BindPath
		snap.CatalogPermissions = cloned.Permissions
	}
	raw, err := json.Marshal(snap)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

func derefTrim(v *string) string {
	if v == nil {
		return ""
	}
	return strings.TrimSpace(*v)
}

func cloneStringMapSnapshot(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func cloneUlimitMapSnapshot(in map[string]Ulimit) map[string]Ulimit {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]Ulimit, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func cloneInt64Snapshot(v *int64) *int64 {
	if v == nil {
		return nil
	}
	n := *v
	return &n
}

func cloneFloat64Snapshot(v *float64) *float64 {
	if v == nil {
		return nil
	}
	n := *v
	return &n
}

func cloneManifestLabels(in map[string]string) map[string]string {
	if len(in) == 0 {
		return map[string]string{}
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		key := strings.TrimSpace(k)
		if key == "" {
			continue
		}
		out[key] = strings.TrimSpace(v)
	}
	return out
}
