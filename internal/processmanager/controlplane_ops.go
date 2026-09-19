package processmanager

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/eclipse-iofog/edgelet/internal/config"
	"github.com/eclipse-iofog/edgelet/internal/controlplane"
	"github.com/eclipse-iofog/edgelet/internal/models"
	"github.com/eclipse-iofog/edgelet/internal/store"
	"github.com/eclipse-iofog/edgelet/internal/volumereclaim"
	"github.com/eclipse-iofog/edgelet/pkg/engine"
)

var ErrControlPlaneNotFound = errors.New("control plane deployment not found")

// SyncApplyControlPlaneDeployment persists and synchronously launches or recreates the controller container.
func (pm *ProcessManager) SyncApplyControlPlaneDeployment(item *models.ControlPlaneDeployment, progress LocalDeployProgressCallback) error {
	if item == nil {
		return errors.New("control plane deployment is nil")
	}
	if pm.containerManager == nil {
		return errors.New("process manager is not initialized")
	}

	item.NormalizeDefaults()
	nowSec := time.Now().Unix()
	item.DesiredState = "running"
	item.DeletedAt = nil
	item.LastStartAttemptAt = nowSec
	item.LastTransitionAt = nowSec

	container, contErr := pm.containerForControlPlane(item.ControllerUUID, item.ContainerID)
	if contErr != nil {
		return contErr
	}

	if container != nil {
		if err := pm.recreateControlPlaneDeploymentWithProgress(item, false, nowSec, progress); err != nil {
			return err
		}
	} else {
		pm.launchControlPlaneWithProgress(item, nowSec, progress)
	}

	got, found, err := store.GetInstance().GetSystemControlPlane()
	if err != nil {
		return err
	}
	if !found || got == nil {
		return errors.New("control plane deployment missing after apply")
	}
	if strings.EqualFold(strings.TrimSpace(got.RuntimeState), "failed") {
		if msg := strings.TrimSpace(got.LastError); msg != "" {
			return fmt.Errorf("%s", msg)
		}
		return errors.New("control plane launch failed")
	}
	return nil
}

// RestartControlPlaneDeployment bounces the singleton control plane container without
// deleting the system_control_plane row or re-registering with Pot.
func (pm *ProcessManager) RestartControlPlaneDeployment(item *models.ControlPlaneDeployment, pullImage bool) error {
	if item == nil {
		return errors.New("control plane deployment is nil")
	}
	if pm.containerManager == nil {
		return errors.New("process manager is not initialized")
	}

	nowSec := time.Now().Unix()

	container, contErr := pm.containerForControlPlane(item.ControllerUUID, item.ContainerID)
	if contErr != nil {
		return contErr
	}

	if container == nil {
		pm.launchControlPlaneWithHook(item, nowSec)
		got, found, err := store.GetInstance().GetSystemControlPlane()
		if err != nil {
			return err
		}
		if !found || got == nil {
			return errors.New("control plane deployment missing after launch")
		}
		if strings.EqualFold(strings.TrimSpace(got.RuntimeState), "failed") {
			if msg := strings.TrimSpace(got.LastError); msg != "" {
				return fmt.Errorf("%s", msg)
			}
			return errors.New("control plane launch failed")
		}
		return nil
	}

	if pullImage || !engine.SupportsInPlaceRestart(pm.engineName) {
		return pm.recreateControlPlaneDeployment(item, pullImage, nowSec)
	}

	if err := pm.StopMicroservice(item.ControllerUUID); err != nil {
		item.LastError = err.Error()
		item.RuntimeState = "failed"
		item.State = item.RuntimeState
		item.LastTransitionAt = nowSec
		_ = store.GetInstance().UpsertSystemControlPlane(item)
		return err
	}
	if err := pm.startLocalMicroservice(item.ControllerUUID); err != nil {
		item.LastError = err.Error()
		item.RuntimeState = "failed"
		item.State = item.RuntimeState
		item.LastTransitionAt = nowSec
		_ = store.GetInstance().UpsertSystemControlPlane(item)
		return err
	}

	container, contErr = pm.containerForControlPlane(item.ControllerUUID, item.ContainerID)
	if contErr != nil {
		return contErr
	}
	if container != nil {
		item.ContainerID = container.ID
	}

	item.RuntimeState = "running"
	item.State = item.RuntimeState
	item.LastTransitionAt = nowSec
	if err := store.GetInstance().UpsertSystemControlPlane(item); err != nil {
		return err
	}
	pm.syncControlPlaneDNS(item, true)

	var status *models.MicroserviceStatus
	if container != nil {
		status, _ = pm.getLocalContainerStatus(container.ID, item.ControllerUUID)
	}
	pm.syncControlPlaneProcessManagerStatus(item, container, status)
	return nil
}

// DeleteControlPlane stops the controller container, removes volumes, and deletes the singleton row.
func (pm *ProcessManager) DeleteControlPlane() error {
	item, found, err := store.GetInstance().GetSystemControlPlane()
	if err != nil {
		return err
	}
	if !found || item == nil {
		return ErrControlPlaneNotFound
	}

	container, contErr := pm.containerForControlPlane(item.ControllerUUID, item.ContainerID)
	if contErr != nil {
		return contErr
	}
	if container != nil && pm.containerManager != nil {
		if err := pm.removeLocalContainerByID(container.ID); err != nil {
			return fmt.Errorf("failed to remove control plane container: %w", err)
		}
	}

	pm.removeControlPlaneDNS(item.ControllerUUID)

	pm.removeControlPlaneVolumes(item.ControllerUUID)

	return store.GetInstance().DeleteSystemControlPlane()
}

func (pm *ProcessManager) removeControlPlaneVolumes(uuid string) {
	uuid = strings.TrimSpace(uuid)
	if uuid == "" {
		return
	}
	db := store.GetInstance()
	disk := ""
	if db != nil {
		disk = strings.TrimSpace(db.DiskDirectory())
	}
	if disk == "" {
		if cfg := config.GetInstance(); cfg != nil {
			disk = strings.TrimSpace(cfg.DiskDirectory)
		}
	}
	if disk == "" {
		return
	}
	r := volumereclaim.New(db, disk)
	if err := r.DeleteControlPlane(uuid); err != nil {
		pm.logger.Warnf("control plane volume cleanup failed uuid=%s err=%v", uuid, err)
		return
	}
	for _, name := range []string{controlplane.VolumeDBName, controlplane.VolumeLogName} {
		path := store.PersistentVolumeHostPath(disk, uuid, name, models.VolumeScopePrivate)
		if err := os.RemoveAll(path); err != nil {
			pm.logger.Warnf("control plane volume path cleanup failed path=%s err=%v", path, err)
		}
	}
}
