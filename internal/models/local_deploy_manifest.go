//revive:disable:nested-structs
package models

import (
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
)

// LocalDeployManifest mirrors single-microservice deploy shape used by controller deploy flows.
type LocalDeployManifest struct {
	APIVersion string `yaml:"apiVersion" json:"apiVersion"`
	Kind       string `yaml:"kind" json:"kind"`
	Metadata   struct {
		Name      string            `yaml:"name" json:"name"`
		Namespace string            `yaml:"namespace,omitempty" json:"namespace,omitempty"`
		Labels    map[string]string `yaml:"labels,omitempty" json:"labels,omitempty"`
	} `yaml:"metadata" json:"metadata"`
	Spec struct {
		Image     string        `yaml:"image" json:"image"`
		Registry  *int          `yaml:"registry,omitempty" json:"registry,omitempty"`
		Models    *ModelCatalog `yaml:"models,omitempty" json:"models,omitempty"`
		Container struct {
			Annotations            map[string]any    `yaml:"annotations,omitempty" json:"annotations,omitempty"`
			HostNetworkMode        bool              `yaml:"hostNetworkMode" json:"hostNetworkMode"`
			IsPrivileged           bool              `yaml:"isPrivileged" json:"isPrivileged"`
			CapAdd                 []string          `yaml:"capAdd,omitempty" json:"capAdd,omitempty"`
			CapDrop                []string          `yaml:"capDrop,omitempty" json:"capDrop,omitempty"`
			IpcMode                string            `yaml:"ipcMode,omitempty" json:"ipcMode,omitempty"`
			PidMode                string            `yaml:"pidMode,omitempty" json:"pidMode,omitempty"`
			RunAsUser              string            `yaml:"runAsUser,omitempty" json:"runAsUser,omitempty"`
			RunAsGroup             string            `yaml:"runAsGroup,omitempty" json:"runAsGroup,omitempty"`
			ReadOnlyRootFilesystem bool              `yaml:"readOnlyRootFilesystem,omitempty" json:"readOnlyRootFilesystem,omitempty"`
			Platform               string            `yaml:"platform,omitempty" json:"platform,omitempty"`
			Runtime                string            `yaml:"runtime,omitempty" json:"runtime,omitempty"`
			CDIDevices             []string          `yaml:"cdiDevices,omitempty" json:"cdiDevices,omitempty"`
			Sysctls                map[string]string `yaml:"sysctls,omitempty" json:"sysctls,omitempty"`
			Ulimits                map[string]Ulimit `yaml:"ulimits,omitempty" json:"ulimits,omitempty"`
			Devices                []DeviceMapping   `yaml:"devices,omitempty" json:"devices,omitempty"`
			Tmpfs                  []TmpfsMount      `yaml:"tmpfs,omitempty" json:"tmpfs,omitempty"`
			Volumes                []struct {
				HostDestination      string `yaml:"hostDestination" json:"hostDestination"`
				ContainerDestination string `yaml:"containerDestination" json:"containerDestination"`
				AccessMode           string `yaml:"accessMode,omitempty" json:"accessMode,omitempty"`
				Type                 string `yaml:"type,omitempty" json:"type,omitempty"`
				Scope                string `yaml:"scope,omitempty" json:"scope,omitempty"`
			} `yaml:"volumes,omitempty" json:"volumes,omitempty"`
			ExtraHosts []struct {
				Name    string `yaml:"name" json:"name"`
				Address string `yaml:"address" json:"address"`
			} `yaml:"extraHosts,omitempty" json:"extraHosts,omitempty"`
			Env []struct {
				Key   string `yaml:"key" json:"key"`
				Value string `yaml:"value" json:"value"`
			} `yaml:"env,omitempty" json:"env,omitempty"`
			Ports []struct {
				Internal int    `yaml:"internal" json:"internal"`
				External int    `yaml:"external" json:"external"`
				Protocol string `yaml:"protocol,omitempty" json:"protocol,omitempty"`
			} `yaml:"ports,omitempty" json:"ports,omitempty"`
			WorkingDir        string    `yaml:"workingDir,omitempty" json:"workingDir,omitempty"`
			Entrypoint        *[]string `yaml:"entrypoint,omitempty" json:"entrypoint,omitempty"`
			Commands          *[]string `yaml:"commands,omitempty" json:"commands,omitempty"`
			CPUSetCpus        string    `yaml:"cpuSetCpus,omitempty" json:"cpuSetCpus,omitempty"`
			Cpus              *float64  `yaml:"cpus,omitempty" json:"cpus,omitempty"`
			MemoryLimit       int64     `yaml:"memoryLimit,omitempty" json:"memoryLimit,omitempty"`
			MemoryReservation *int64    `yaml:"memoryReservation,omitempty" json:"memoryReservation,omitempty"`
			MemorySwap        *int64    `yaml:"memorySwap,omitempty" json:"memorySwap,omitempty"`
			ShmSize           *int64    `yaml:"shmSize,omitempty" json:"shmSize,omitempty"`
			HealthCheck       struct {
				Test        []string `yaml:"test,omitempty" json:"test,omitempty"`
				Interval    int      `yaml:"interval,omitempty" json:"interval,omitempty"`
				Timeout     int      `yaml:"timeout,omitempty" json:"timeout,omitempty"`
				StartPeriod int      `yaml:"startPeriod,omitempty" json:"startPeriod,omitempty"`
				Retries     int      `yaml:"retries,omitempty" json:"retries,omitempty"`
			} `yaml:"healthCheck,omitempty" json:"healthCheck,omitempty"`
		} `yaml:"container,omitempty" json:"container,omitempty"`
		Schedule int            `yaml:"schedule,omitempty" json:"schedule,omitempty"`
		Config   map[string]any `yaml:"config,omitempty" json:"config,omitempty"`
	} `yaml:"spec" json:"spec"`
}

var localDeployNamePattern = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)
var localDeployVolumeNamePattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]*$`)
var windowsAbsPathPattern = regexp.MustCompile(`^[a-zA-Z]:[\\/].+`)

func (m *LocalDeployManifest) Validate() error {
	if m == nil {
		return errors.New("manifest is nil")
	}
	if strings.TrimSpace(m.APIVersion) == "" {
		return errors.New("apiVersion is required")
	}
	if strings.TrimSpace(m.Kind) == "" {
		return errors.New("kind is required")
	}
	if strings.TrimSpace(m.Kind) != "Microservice" {
		return errors.New("kind must be Microservice")
	}
	switch strings.TrimSpace(m.APIVersion) {
	case "edgelet.iofog.org/v1":
	default:
		return errors.New("apiVersion must be edgelet.iofog.org/v1")
	}
	if strings.TrimSpace(m.Metadata.Name) == "" {
		return errors.New("metadata.name is required")
	}
	name := strings.TrimSpace(m.Metadata.Name)
	if len(name) > 63 {
		return errors.New("metadata.name must be <= 63 characters and follow DNS-1123 label format")
	}
	if !localDeployNamePattern.MatchString(name) {
		return errors.New("metadata.name must match DNS-1123 label format: lowercase alphanumeric or '-', start/end alphanumeric")
	}
	if strings.TrimSpace(m.Spec.Image) == "" {
		return errors.New("spec.image is required")
	}
	for i := range m.Spec.Container.Volumes {
		volume := &m.Spec.Container.Volumes[i]
		effectiveType := strings.ToUpper(strings.TrimSpace(volume.Type))
		if effectiveType == "" {
			effectiveType = string(VolumeMappingTypeBind)
		}
		volume.Type = effectiveType

		switch VolumeMappingType(effectiveType) {
		case VolumeMappingTypeBind:
			if !isValidHostPath(volume.HostDestination) {
				return fmt.Errorf("spec.container.volumes[%d].hostDestination must be an absolute host path for type BIND", i)
			}
			volume.Scope = VolumeScopePrivate
		case VolumeMappingTypeVolume:
			if !localDeployVolumeNamePattern.MatchString(strings.TrimSpace(volume.HostDestination)) {
				return fmt.Errorf("spec.container.volumes[%d].hostDestination includes invalid characters for a local volume name, only \"[a-zA-Z0-9][a-zA-Z0-9_.-]*\" are allowed. If you intended to pass a host directory, use type: bind", i)
			}
			volume.Scope = CanonicalVolumeScope(volume.Scope)
		case VolumeMappingTypeVolumeMount:
			return fmt.Errorf("spec.container.volumes[%d].type VOLUME_MOUNT is not supported for local manifests", i)
		default:
			return fmt.Errorf("spec.container.volumes[%d].type must be BIND or VOLUME", i)
		}
	}
	volumeDests := make([]string, 0, len(m.Spec.Container.Volumes))
	for _, volume := range m.Spec.Container.Volumes {
		if dest := strings.TrimSpace(volume.ContainerDestination); dest != "" {
			volumeDests = append(volumeDests, dest)
		}
	}
	if err := ValidateModelCatalog(m.Spec.Models, volumeDests, collectTmpfsPaths(m.Spec.Container.Tmpfs)); err != nil {
		return err
	}
	return validateManifestContainerFields(m)
}

// ValidateWithModelSources runs Validate then checks catalog item names against a source lookup.
func (m *LocalDeployManifest) ValidateWithModelSources(lookup ModelSourceLookup) error {
	if err := m.Validate(); err != nil {
		return err
	}
	return ValidateCatalogModelSources(m.Spec.Models, ModelSourceLocal, lookup)
}

// ValidateCatalogApply runs Validate then rejects unknown or Failed local catalog names.
func (m *LocalDeployManifest) ValidateCatalogApply(lookup ModelStatusLookup) error {
	if err := m.Validate(); err != nil {
		return err
	}
	return ValidateCatalogApply(m.Spec.Models, ModelSourceLocal, lookup)
}

func isValidHostPath(path string) bool {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return false
	}
	return filepath.IsAbs(trimmed) || windowsAbsPathPattern.MatchString(trimmed)
}

// ManifestImage returns the resolved container image from spec.image.
func (m *LocalDeployManifest) ManifestImage() string {
	if m == nil {
		return ""
	}
	return strings.TrimSpace(m.Spec.Image)
}
