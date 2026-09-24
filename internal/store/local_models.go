package store

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/eclipse-iofog/edgelet/internal/models"
)

const localModelSelectColumns = `name, source, repo, revision, registry_id, files_json, format, state, last_error,
		generation, observed_generation, manifest_yaml, manifest_path, content_path,
		resolved_revision, digest, revision_floating, total_bytes,
		last_transition_at, last_reconcile_at, pulled_at`

// UpsertLocalModel inserts or updates one local model row by metadata.name.
func (d *DB) UpsertLocalModel(m *models.LocalModel) error {
	if m == nil {
		return errors.New("model is nil")
	}
	m.NormalizeDefaults()
	if strings.TrimSpace(m.Name) == "" {
		return errors.New("model name is required")
	}
	if strings.TrimSpace(m.Repo) == "" {
		return errors.New("model repo is required")
	}
	if m.RegistryID <= 0 {
		return errors.New("model registry_id is required")
	}
	if !models.ValidModelState(m.State) {
		return fmt.Errorf("invalid model state %q", m.State)
	}
	if !models.ValidModelSource(m.Source) {
		return fmt.Errorf("invalid model source %q", m.Source)
	}

	_, err := d.Conn().Exec(`INSERT INTO local_models (
		name, source, repo, revision, registry_id, files_json, format, state, last_error,
		generation, observed_generation, manifest_yaml, manifest_path, content_path,
		resolved_revision, digest, revision_floating, total_bytes,
		last_transition_at, last_reconcile_at, pulled_at, created_at, updated_at
	) VALUES (
		?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,strftime('%s','now'),?
	)
	ON CONFLICT(name) DO UPDATE SET
		source=excluded.source,
		repo=excluded.repo,
		revision=excluded.revision,
		registry_id=excluded.registry_id,
		files_json=excluded.files_json,
		format=excluded.format,
		state=excluded.state,
		last_error=excluded.last_error,
		generation=excluded.generation,
		observed_generation=excluded.observed_generation,
		manifest_yaml=excluded.manifest_yaml,
		manifest_path=excluded.manifest_path,
		content_path=excluded.content_path,
		resolved_revision=excluded.resolved_revision,
		digest=excluded.digest,
		revision_floating=excluded.revision_floating,
		total_bytes=excluded.total_bytes,
		last_transition_at=excluded.last_transition_at,
		last_reconcile_at=excluded.last_reconcile_at,
		pulled_at=excluded.pulled_at,
		updated_at=excluded.updated_at`,
		m.Name, m.Source, m.Repo, m.Revision, m.RegistryID, m.FilesJSON, m.Format, m.State, m.LastError,
		m.Generation, m.ObservedGeneration, m.ManifestYAML, m.ManifestPath, m.ContentPath,
		m.ResolvedRevision, m.Digest, boolToInt(m.RevisionFloating), m.TotalBytes,
		m.LastTransitionAt, m.LastReconcileAt, m.PulledAt, time.Now().Unix(),
	)
	if err != nil {
		return fmt.Errorf("failed to upsert local model: %w", err)
	}
	return nil
}

// GetLocalModel retrieves one local model by metadata.name.
func (d *DB) GetLocalModel(name string) (*models.LocalModel, error) {
	row := d.Conn().QueryRow(
		"SELECT "+localModelSelectColumns+" FROM local_models WHERE name = ?",
		strings.TrimSpace(name),
	)
	return scanLocalModel(row)
}

// ListLocalModels returns all local model rows ordered by name.
func (d *DB) ListLocalModels() ([]*models.LocalModel, error) {
	rows, err := d.Conn().Query(
		"SELECT " + localModelSelectColumns + " FROM local_models ORDER BY name",
	)
	if err != nil {
		return nil, fmt.Errorf("failed to query local_models: %w", err)
	}
	defer func() {
		_ = rows.Close()
	}()

	items := make([]*models.LocalModel, 0)
	for rows.Next() {
		item, scanErr := scanLocalModel(rows)
		if scanErr != nil {
			return nil, fmt.Errorf("failed to scan local model: %w", scanErr)
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

// DeleteLocalModel deletes one local model by metadata.name.
func (d *DB) DeleteLocalModel(name string) error {
	_, err := d.Conn().Exec("DELETE FROM local_models WHERE name = ?", strings.TrimSpace(name))
	return err
}

func scanLocalModel(s interface{ Scan(dest ...any) error }) (*models.LocalModel, error) {
	item := &models.LocalModel{}
	var revisionFloating int
	if err := s.Scan(
		&item.Name, &item.Source, &item.Repo, &item.Revision, &item.RegistryID, &item.FilesJSON, &item.Format,
		&item.State, &item.LastError, &item.Generation, &item.ObservedGeneration,
		&item.ManifestYAML, &item.ManifestPath, &item.ContentPath,
		&item.ResolvedRevision, &item.Digest, &revisionFloating, &item.TotalBytes,
		&item.LastTransitionAt, &item.LastReconcileAt, &item.PulledAt,
	); err != nil {
		return nil, err
	}
	item.RevisionFloating = intToBool(revisionFloating)
	item.NormalizeDefaults()
	return item, nil
}
