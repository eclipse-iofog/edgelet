package store

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/eclipse-iofog/edgelet/internal/controlplane"
	"github.com/eclipse-iofog/edgelet/internal/models"
)

const controlPlaneSingletonID = 1

// UpsertSystemControlPlane stores the singleton system control plane row.
func (d *DB) UpsertSystemControlPlane(dep *models.ControlPlaneDeployment) error {
	if dep == nil {
		return errors.New("control plane deployment is nil")
	}
	if strings.TrimSpace(dep.ControllerUUID) == "" {
		return errors.New("controller_uuid is required")
	}
	if strings.TrimSpace(dep.ManifestYAML) == "" {
		return errors.New("manifest_yaml is required")
	}
	if strings.TrimSpace(dep.Name) == "" {
		return errors.New("name is required")
	}
	if d.Conn() == nil {
		return errors.New("database is closed")
	}

	dep.NormalizeDefaults()

	controllerRegistered := boolToSQLiteInt(dep.ControllerRegistered)
	initialRebuildSkipped := boolToSQLiteInt(dep.InitialRebuildSkipped)

	_, err := d.Conn().Exec(`INSERT INTO system_control_plane (
		id, controller_uuid, namespace, name, manifest_yaml, image, container_id,
		state, desired_state, runtime_state, last_error, restart_count,
		last_transition_at, last_reconcile_at, last_start_attempt_at, failure_count,
		deleted_at, generation, observed_generation, controller_registered,
		initial_rebuild_skipped, created_at, updated_at
	) VALUES (
		?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,strftime('%s','now'),?
	)
	ON CONFLICT(id) DO UPDATE SET
		controller_uuid=excluded.controller_uuid,
		namespace=excluded.namespace,
		name=excluded.name,
		manifest_yaml=excluded.manifest_yaml,
		image=excluded.image,
		container_id=excluded.container_id,
		state=excluded.state,
		desired_state=excluded.desired_state,
		runtime_state=excluded.runtime_state,
		last_error=excluded.last_error,
		restart_count=excluded.restart_count,
		last_transition_at=excluded.last_transition_at,
		last_reconcile_at=excluded.last_reconcile_at,
		last_start_attempt_at=excluded.last_start_attempt_at,
		failure_count=excluded.failure_count,
		deleted_at=excluded.deleted_at,
		generation=excluded.generation,
		observed_generation=excluded.observed_generation,
		controller_registered=excluded.controller_registered,
		initial_rebuild_skipped=excluded.initial_rebuild_skipped,
		updated_at=excluded.updated_at`,
		controlPlaneSingletonID,
		dep.ControllerUUID, dep.Namespace, dep.Name, dep.ManifestYAML, dep.Image, dep.ContainerID,
		dep.State, dep.DesiredState, dep.RuntimeState, dep.LastError, dep.RestartCount,
		dep.LastTransitionAt, dep.LastReconcileAt, dep.LastStartAttemptAt, dep.FailureCount,
		dep.DeletedAt, dep.Generation, dep.ObservedGeneration, controllerRegistered,
		initialRebuildSkipped, time.Now().Unix(),
	)
	if err != nil {
		return fmt.Errorf("failed to upsert system control plane: %w", err)
	}
	return d.upsertControlPlanePersistentVolumes(dep.ControllerUUID)
}

func (d *DB) upsertControlPlanePersistentVolumes(uuid string) error {
	for _, name := range []string{controlplane.VolumeDBName, controlplane.VolumeLogName} {
		if err := d.UpsertPersistentVolume(uuid, name, PersistentVolumeKindControlPlane, models.VolumeScopePrivate, ""); err != nil {
			return err
		}
	}
	return nil
}

// GetSystemControlPlane reads the singleton system control plane row.
// Returns found=false when no deployment is stored.
func (d *DB) GetSystemControlPlane() (*models.ControlPlaneDeployment, bool, error) {
	if d.Conn() == nil {
		return nil, false, errors.New("database is closed")
	}

	item := &models.ControlPlaneDeployment{}
	var controllerRegistered, initialRebuildSkipped int
	err := d.Conn().QueryRow(`SELECT
		controller_uuid, namespace, name, manifest_yaml, image, container_id,
		state, desired_state, runtime_state, last_error, restart_count,
		last_transition_at, last_reconcile_at, last_start_attempt_at, failure_count,
		deleted_at, generation, observed_generation, controller_registered, initial_rebuild_skipped
		FROM system_control_plane WHERE id = ?`, controlPlaneSingletonID).Scan(
		&item.ControllerUUID, &item.Namespace, &item.Name, &item.ManifestYAML, &item.Image, &item.ContainerID,
		&item.State, &item.DesiredState, &item.RuntimeState, &item.LastError, &item.RestartCount,
		&item.LastTransitionAt, &item.LastReconcileAt, &item.LastStartAttemptAt, &item.FailureCount,
		&item.DeletedAt, &item.Generation, &item.ObservedGeneration,
		&controllerRegistered, &initialRebuildSkipped,
	)
	if err == sql.ErrNoRows {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("failed to get system control plane: %w", err)
	}
	item.ControllerRegistered = controllerRegistered != 0
	item.InitialRebuildSkipped = initialRebuildSkipped != 0
	item.NormalizeDefaults()
	return item, true, nil
}

func boolToSQLiteInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

// DeleteSystemControlPlane removes the singleton system control plane row.
func (d *DB) DeleteSystemControlPlane() error {
	if d.Conn() == nil {
		return errors.New("database is closed")
	}
	if _, err := d.Conn().Exec(`DELETE FROM system_control_plane WHERE id = ?`, controlPlaneSingletonID); err != nil {
		return fmt.Errorf("failed to delete system control plane: %w", err)
	}
	return nil
}
