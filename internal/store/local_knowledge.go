package store

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/eclipse-iofog/edgelet/internal/models"
)

const localKnowledgeSelectColumns = `name, generation, source, registry_id, repo, revision, files_json, format,
		state, resolved_revision, digest, revision_floating, total_bytes, last_error`

// UpsertLocalKnowledge inserts or updates one local Knowledge row by metadata.name.
func (d *DB) UpsertLocalKnowledge(k *models.LocalKnowledge) error {
	if k == nil {
		return errors.New("knowledge is nil")
	}
	k.NormalizeDefaults()
	if strings.TrimSpace(k.Name) == "" {
		return errors.New("knowledge name is required")
	}
	if strings.TrimSpace(k.Repo) == "" {
		return errors.New("knowledge repo is required")
	}
	if k.RegistryID <= 0 {
		return errors.New("knowledge registry_id is required")
	}
	if !models.ValidKnowledgeState(k.State) {
		return fmt.Errorf("invalid knowledge state %q", k.State)
	}
	if !models.ValidKnowledgeSource(k.Source) {
		return fmt.Errorf("invalid knowledge source %q", k.Source)
	}

	_, err := d.Conn().Exec(`INSERT INTO local_knowledge (
		name, generation, source, registry_id, repo, revision, files_json, format,
		state, resolved_revision, digest, revision_floating, total_bytes, last_error, updated_at
	) VALUES (
		?,?,?,?,?,?,?,?,?,?,?,?,?,?,?
	)
	ON CONFLICT(name) DO UPDATE SET
		generation=excluded.generation,
		source=excluded.source,
		registry_id=excluded.registry_id,
		repo=excluded.repo,
		revision=excluded.revision,
		files_json=excluded.files_json,
		format=excluded.format,
		state=excluded.state,
		resolved_revision=excluded.resolved_revision,
		digest=excluded.digest,
		revision_floating=excluded.revision_floating,
		total_bytes=excluded.total_bytes,
		last_error=excluded.last_error,
		updated_at=excluded.updated_at`,
		k.Name, k.Generation, k.Source, k.RegistryID, k.Repo, k.Revision, k.FilesJSON, k.Format,
		k.State, k.ResolvedRevision, k.Digest, boolToInt(k.RevisionFloating), k.TotalBytes, k.LastError,
		time.Now().Unix(),
	)
	if err != nil {
		return fmt.Errorf("failed to upsert local knowledge: %w", err)
	}
	return nil
}

// GetLocalKnowledge retrieves one local Knowledge by metadata.name.
func (d *DB) GetLocalKnowledge(name string) (*models.LocalKnowledge, error) {
	row := d.Conn().QueryRow(
		"SELECT "+localKnowledgeSelectColumns+" FROM local_knowledge WHERE name = ?",
		strings.TrimSpace(name),
	)
	return scanLocalKnowledge(row)
}

// ListLocalKnowledge returns all local Knowledge rows ordered by name.
func (d *DB) ListLocalKnowledge() ([]*models.LocalKnowledge, error) {
	rows, err := d.Conn().Query(
		"SELECT " + localKnowledgeSelectColumns + " FROM local_knowledge ORDER BY name",
	)
	if err != nil {
		return nil, fmt.Errorf("failed to query local_knowledge: %w", err)
	}
	defer func() {
		_ = rows.Close()
	}()

	items := make([]*models.LocalKnowledge, 0)
	for rows.Next() {
		item, scanErr := scanLocalKnowledge(rows)
		if scanErr != nil {
			return nil, fmt.Errorf("failed to scan local knowledge: %w", scanErr)
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

// DeleteLocalKnowledge deletes one local Knowledge by metadata.name.
func (d *DB) DeleteLocalKnowledge(name string) error {
	_, err := d.Conn().Exec("DELETE FROM local_knowledge WHERE name = ?", strings.TrimSpace(name))
	return err
}

func scanLocalKnowledge(s interface{ Scan(dest ...any) error }) (*models.LocalKnowledge, error) {
	item := &models.LocalKnowledge{}
	var revisionFloating int
	if err := s.Scan(
		&item.Name, &item.Generation, &item.Source, &item.RegistryID, &item.Repo, &item.Revision,
		&item.FilesJSON, &item.Format, &item.State, &item.ResolvedRevision, &item.Digest,
		&revisionFloating, &item.TotalBytes, &item.LastError,
	); err != nil {
		return nil, err
	}
	item.RevisionFloating = intToBool(revisionFloating)
	item.NormalizeDefaults()
	return item, nil
}
