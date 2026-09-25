package processmanager

import (
	"errors"
	"strings"

	"github.com/eclipse-iofog/edgelet/internal/models"
	"github.com/eclipse-iofog/edgelet/internal/runtimeops"
	"github.com/eclipse-iofog/edgelet/internal/statusreporter"
	"github.com/eclipse-iofog/edgelet/internal/store"
)

// volumeInUseStatusText is shown while create or start waits because a host
// process still has a persistent volume open.
const volumeInUseStatusText = "volume in use by a leftover process"

// errVolumeInUse is returned when create or start must wait for that process
// to release the volume. Callers keep the current runtime state.
var errVolumeInUse = errors.New(volumeInUseStatusText)

// listVolumePathHolders is the host scan used before creating a container.
// Tests replace it; production uses the /proc scanner on Linux.
var listVolumePathHolders = ListVolumePathHolders

// persistentVolumeHostPaths returns host directories for VOLUME mappings.
// BIND and secret/config mounts are omitted. Private volumes use
// volumes/data/{uuid}/{name}; shared volumes use volumes/shared/{name}.
func persistentVolumeHostPaths(diskDirectory string, ms *models.Microservice) []string {
	diskDirectory = strings.TrimSpace(diskDirectory)
	if diskDirectory == "" || ms == nil {
		return nil
	}
	seen := make(map[string]struct{})
	paths := make([]string, 0)
	for _, vm := range ms.VolumeMappings {
		if vm == nil || vm.Type != models.VolumeMappingTypeVolume {
			continue
		}
		name := strings.TrimSpace(vm.HostDestination)
		if name == "" {
			continue
		}
		path := store.PersistentVolumeHostPath(diskDirectory, ms.MicroserviceUUID, name, vm.EffectiveVolumeScope())
		if _, ok := seen[path]; ok {
			continue
		}
		seen[path] = struct{}{}
		paths = append(paths, path)
	}
	return paths
}

func volumePathsHeld(diskDirectory string, ms *models.Microservice) (bool, error) {
	paths := persistentVolumeHostPaths(diskDirectory, ms)
	if len(paths) == 0 {
		return false, nil
	}
	holders, err := listVolumePathHolders(paths)
	if err != nil {
		return false, err
	}
	return len(holders) > 0, nil
}

func (pm *ProcessManager) volumeHeld(ms *models.Microservice) bool {
	disk := ""
	if pm != nil {
		disk = pm.catalogDiskDirectoryValue()
	}
	held, err := volumePathsHeld(disk, ms)
	if err != nil {
		if pm != nil && pm.logger != nil && ms != nil {
			pm.logger.Warnf("volume holder scan for %s: %v", ms.MicroserviceUUID, err)
		}
		return true
	}
	return held
}

func (cm *ContainerManager) volumeHeld(ms *models.Microservice) bool {
	disk := ""
	if cm != nil {
		disk = cm.catalogDiskDirectoryValue()
	}
	held, err := volumePathsHeld(disk, ms)
	if err != nil {
		if cm != nil && cm.logger != nil && ms != nil {
			cm.logger.Warnf("volume holder scan for %s: %v", ms.MicroserviceUUID, err)
		}
		return true
	}
	return held
}

// deferVolumeCreate reports whether a new container must wait. When it returns
// true, the caller keeps the current runtime state and does not enqueue create.
// keep, when set, is written first so the reported state stays the engine state.
func (pm *ProcessManager) deferVolumeCreate(ms *models.Microservice, keep *models.MicroserviceStatus) bool {
	if pm == nil || !pm.volumeHeld(ms) {
		return false
	}
	if ms == nil {
		return true
	}
	if keep != nil {
		pm.syncRuntimeStatus(ms.MicroserviceUUID, keep)
	}
	pm.noteVolumeInUse(ms.MicroserviceUUID)
	pm.armReconcileDeadline(ms.MicroserviceUUID, reconcileTickInterval())
	return true
}

// volumeWaitActive reports whether the current error is a volume hold.
// That wait is not a crash, so the next create is not charged a restart delay.
func volumeWaitActive(status *models.MicroserviceStatus) bool {
	return status != nil && status.ErrorMessage != nil &&
		strings.TrimSpace(*status.ErrorMessage) == volumeInUseStatusText
}

func (pm *ProcessManager) clearVolumeWait(msUUID string) {
	statusreporter.GetInstance().UpdateProcessManagerStatus(func(s *models.ProcessManagerStatus) {
		s.SetMicroservicesStatusErrorMessage(msUUID, "")
	})
}

func (pm *ProcessManager) noteVolumeInUse(msUUID string) {
	msUUID = strings.TrimSpace(msUUID)
	if msUUID == "" {
		return
	}
	statusreporter.GetInstance().UpdateProcessManagerStatus(func(s *models.ProcessManagerStatus) {
		s.SetMicroserviceStatusWait(msUUID, volumeInUseStatusText)
	})
	if pm != nil && pm.ctx != nil {
		pm.emitReconcileDecision(msUUID, "WAIT", "volume_in_use", volumeInUseStatusText, runtimeops.LevelInfo, nil)
	}
}

func (pm *ProcessManager) noteLocalVolumeHold(item *models.LocalDeployedMicroservice) {
	if item == nil {
		return
	}
	pm.noteVolumeInUse(item.LocalUUID)
	item.LastError = volumeInUseStatusText
	_ = persistLocalWorkloadIfPresent(item)
}
