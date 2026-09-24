package store

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/eclipse-iofog/edgelet/internal/models"
)

// UpsertLocalWorkload upserts a local workload record.
func (d *DB) UpsertLocalWorkload(ms *models.LocalDeployedMicroservice) error {
	if ms == nil {
		return errors.New("local deployed microservice is nil")
	}
	if ms.LocalUUID == "" {
		return errors.New("local_uuid is required")
	}
	if ms.ManifestYAML == "" {
		return errors.New("manifest_yaml is required")
	}
	ms.NormalizeDefaults()

	_, err := d.Conn().Exec(`INSERT INTO local_workloads (
		local_uuid, application_name, microservice_name, source_name, manifest_yaml, image_name,
		state, container_id, desired_state, runtime_state, last_error, restart_count, last_transition_at, last_reconcile_at, last_start_attempt_at, failure_count,
		deleted_at, generation, observed_generation, created_at, updated_at
	) VALUES (
		?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,strftime('%s','now'),?
	)
	ON CONFLICT(local_uuid) DO UPDATE SET
		application_name=excluded.application_name,
		microservice_name=excluded.microservice_name,
		source_name=excluded.source_name,
		manifest_yaml=excluded.manifest_yaml,
		image_name=excluded.image_name,
		state=excluded.state,
		container_id=excluded.container_id,
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
		updated_at=excluded.updated_at`,
		ms.LocalUUID, ms.ApplicationName, ms.MicroserviceName, ms.SourceName, ms.ManifestYAML, ms.ImageName,
		ms.State, ms.ContainerID, ms.DesiredState, ms.RuntimeState, ms.LastError, ms.RestartCount, ms.LastTransitionAt,
		ms.LastReconcileAt, ms.LastStartAttemptAt, ms.FailureCount, ms.DeletedAt, ms.Generation, ms.ObservedGeneration, time.Now().Unix(),
	)
	if err != nil {
		return fmt.Errorf("failed to upsert local workload: %w", err)
	}
	if err := d.UpsertPersistentVolumesFromMappings(
		ms.LocalUUID,
		PersistentVolumeKindWorkload,
		volumeMappingsFromManifestYAML(ms.ManifestYAML),
	); err != nil {
		return fmt.Errorf("failed to record persistent volumes for %s: %w", ms.LocalUUID, err)
	}
	return nil
}

// ListLocalWorkloads returns all local workload records.
func (d *DB) ListLocalWorkloads() ([]*models.LocalDeployedMicroservice, error) {
	if d.Conn() == nil {
		return nil, errors.New("database is closed")
	}
	rows, err := d.Conn().Query(`SELECT
		local_uuid, application_name, microservice_name, source_name, manifest_yaml, image_name, state, container_id,
		desired_state, runtime_state, last_error, restart_count, last_transition_at, last_reconcile_at, last_start_attempt_at, failure_count,
		deleted_at, generation, observed_generation
		FROM local_workloads ORDER BY created_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("failed to query local_workloads: %w", err)
	}
	defer func() {
		_ = rows.Close()
	}()

	var result []*models.LocalDeployedMicroservice
	for rows.Next() {
		item := &models.LocalDeployedMicroservice{}
		if scanErr := rows.Scan(
			&item.LocalUUID, &item.ApplicationName, &item.MicroserviceName, &item.SourceName, &item.ManifestYAML,
			&item.ImageName, &item.State, &item.ContainerID, &item.DesiredState, &item.RuntimeState, &item.LastError,
			&item.RestartCount, &item.LastTransitionAt, &item.LastReconcileAt, &item.LastStartAttemptAt, &item.FailureCount,
			&item.DeletedAt, &item.Generation, &item.ObservedGeneration,
		); scanErr != nil {
			return nil, fmt.Errorf("failed to scan local_workloads row: %w", scanErr)
		}
		item.NormalizeDefaults()
		result = append(result, item)
	}
	if result == nil {
		result = make([]*models.LocalDeployedMicroservice, 0)
	}
	return result, rows.Err()
}

// GetLocalWorkload retrieves one local workload record by id.
func (d *DB) GetLocalWorkload(id string) (*models.LocalDeployedMicroservice, error) {
	row := d.Conn().QueryRow(`SELECT
		local_uuid, application_name, microservice_name, source_name, manifest_yaml, image_name, state, container_id,
		desired_state, runtime_state, last_error, restart_count, last_transition_at, last_reconcile_at, last_start_attempt_at, failure_count,
		deleted_at, generation, observed_generation
		FROM local_workloads WHERE local_uuid = ?`, id)
	item := &models.LocalDeployedMicroservice{}
	if err := row.Scan(
		&item.LocalUUID, &item.ApplicationName, &item.MicroserviceName, &item.SourceName, &item.ManifestYAML,
		&item.ImageName, &item.State, &item.ContainerID, &item.DesiredState, &item.RuntimeState, &item.LastError,
		&item.RestartCount, &item.LastTransitionAt, &item.LastReconcileAt, &item.LastStartAttemptAt, &item.FailureCount,
		&item.DeletedAt, &item.Generation, &item.ObservedGeneration,
	); err != nil {
		return nil, err
	}
	item.NormalizeDefaults()
	return item, nil
}

// FindLocalWorkloadsByAppAndName finds local workloads by app/name.
func (d *DB) FindLocalWorkloadsByAppAndName(application, name string) ([]*models.LocalDeployedMicroservice, error) {
	app := strings.TrimSpace(application)
	msName := strings.TrimSpace(name)
	if app == "" || msName == "" {
		return make([]*models.LocalDeployedMicroservice, 0), nil
	}
	if d.Conn() == nil {
		return nil, errors.New("database is closed")
	}
	rows, err := d.Conn().Query(`SELECT
		local_uuid, application_name, microservice_name, source_name, manifest_yaml, image_name, state, container_id,
		desired_state, runtime_state, last_error, restart_count, last_transition_at, last_reconcile_at, last_start_attempt_at, failure_count,
		deleted_at, generation, observed_generation
		FROM local_workloads
		WHERE application_name = ? COLLATE NOCASE
		  AND microservice_name = ? COLLATE NOCASE
		ORDER BY updated_at DESC, created_at DESC`, app, msName)
	if err != nil {
		return nil, fmt.Errorf("failed to query local_workloads by app/name: %w", err)
	}
	defer func() {
		_ = rows.Close()
	}()

	items := make([]*models.LocalDeployedMicroservice, 0)
	for rows.Next() {
		item := &models.LocalDeployedMicroservice{}
		if scanErr := rows.Scan(
			&item.LocalUUID, &item.ApplicationName, &item.MicroserviceName, &item.SourceName, &item.ManifestYAML,
			&item.ImageName, &item.State, &item.ContainerID, &item.DesiredState, &item.RuntimeState, &item.LastError,
			&item.RestartCount, &item.LastTransitionAt, &item.LastReconcileAt, &item.LastStartAttemptAt, &item.FailureCount,
			&item.DeletedAt, &item.Generation, &item.ObservedGeneration,
		); scanErr != nil {
			return nil, fmt.Errorf("failed to scan local_workloads by app/name row: %w", scanErr)
		}
		item.NormalizeDefaults()
		items = append(items, item)
	}
	return items, rows.Err()
}

// DeleteLocalWorkload removes a local workload record by id.
func (d *DB) DeleteLocalWorkload(id string) error {
	if _, err := d.Conn().Exec(`DELETE FROM local_workloads WHERE local_uuid = ?`, id); err != nil {
		return err
	}
	return d.MarkPersistentVolumesUnreferenced(id)
}

// ClearLocalWorkloads removes all local workload records.
func (d *DB) ClearLocalWorkloads() error {
	_, err := d.Conn().Exec(`DELETE FROM local_workloads`)
	return err
}
