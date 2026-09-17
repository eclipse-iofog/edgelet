package docker

import (
	"fmt"
	"slices"
	"strings"

	"github.com/eclipse-iofog/edgelet/internal/containerapply"
	"github.com/eclipse-iofog/edgelet/internal/models"
	"github.com/moby/moby/api/types/container"
)

func applyWorkloadCreateConfig(config *container.Config, hostConfig *container.HostConfig, ms *models.Microservice, diskDir string) []string {
	if config == nil || hostConfig == nil || ms == nil {
		return nil
	}

	if !models.UsesImageDefault(ms.Entrypoint) {
		config.Entrypoint = append([]string{}, (*ms.Entrypoint)...)
	}
	if cmd := containerapply.CommandArgs(ms); cmd != nil {
		config.Cmd = cmd
	} else {
		config.Cmd = nil
	}
	if ms.WorkingDir != nil {
		if wd := strings.TrimSpace(*ms.WorkingDir); wd != "" {
			config.WorkingDir = wd
		}
	}

	if user := containerapply.DockerUser(ms.RunAsUser, ms.RunAsGroup); user != "" {
		config.User = user
	}

	hostConfig.ReadonlyRootfs = ms.ReadOnlyRootFilesystem

	if host, dest, readOnly, ok := containerapply.CatalogBind(ms, diskDir); ok {
		mode := "rw"
		if readOnly {
			mode = "ro"
		}
		hostConfig.Binds = append(hostConfig.Binds, fmt.Sprintf("%s:%s:%s", host, dest, mode))
	}

	if len(ms.Tmpfs) > 0 {
		if hostConfig.Tmpfs == nil {
			hostConfig.Tmpfs = make(map[string]string, len(ms.Tmpfs))
		}
		for _, t := range ms.Tmpfs {
			p := strings.TrimSpace(t.ContainerPath)
			if p == "" {
				continue
			}
			hostConfig.Tmpfs[p] = containerapply.DockerTmpfsOptions(t)
		}
	}

	if ms.ShmSize != nil && *ms.ShmSize > 0 {
		hostConfig.ShmSize = *ms.ShmSize
	}

	if ms.Cpus != nil && *ms.Cpus > 0 {
		hostConfig.NanoCPUs = containerapply.NanoCPUs(*ms.Cpus)
	}

	if ms.MemoryReservation != nil && *ms.MemoryReservation > 0 {
		hostConfig.MemoryReservation = *ms.MemoryReservation
	}
	if ms.MemorySwap != nil {
		hostConfig.MemorySwap = *ms.MemorySwap
	}

	if len(ms.Sysctls) > 0 {
		hostConfig.Sysctls = cloneSysctlMap(ms.Sysctls)
	}

	if len(ms.Ulimits) > 0 {
		names := make([]string, 0, len(ms.Ulimits))
		for name := range ms.Ulimits {
			names = append(names, name)
		}
		slices.Sort(names)
		hostConfig.Ulimits = make([]*container.Ulimit, 0, len(names))
		for _, name := range names {
			u := ms.Ulimits[name]
			hostConfig.Ulimits = append(hostConfig.Ulimits, &container.Ulimit{
				Name: name,
				Soft: u.Soft,
				Hard: u.Hard,
			})
		}
	}

	if len(ms.Devices) > 0 {
		hostConfig.Devices = make([]container.DeviceMapping, 0, len(ms.Devices))
		for _, d := range ms.Devices {
			perms := strings.TrimSpace(d.Permissions)
			if perms == "" {
				perms = "rwm"
			}
			hostConfig.Devices = append(hostConfig.Devices, container.DeviceMapping{
				PathOnHost:        d.HostPath,
				PathInContainer:   d.ContainerPath,
				CgroupPermissions: perms,
			})
		}
	}

	if fp, err := containerapply.Marshal(containerapply.FromMicroservice(ms)); err == nil && fp != "" && fp != "{}" {
		if config.Labels == nil {
			config.Labels = make(map[string]string)
		}
		config.Labels[containerapply.LabelFingerprint] = fp
	}

	return workloadApplyWarnings(ms)
}

func workloadApplyWarnings(ms *models.Microservice) []string {
	if ms == nil || !models.NeedsReadOnlyRootTmpfsWarning(ms.ReadOnlyRootFilesystem, ms.Tmpfs) {
		return nil
	}
	return []string{models.ReadOnlyRootWithoutTmpfsWarning}
}

func cloneSysctlMap(in map[string]string) map[string]string {
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
