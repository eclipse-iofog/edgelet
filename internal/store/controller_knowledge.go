package store

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/eclipse-iofog/edgelet/internal/models"
)

const controllerKnowledgeSelectColumns = `uuid, name, registry_id, repo, revision, files_json, format,
		state, resolved_revision, digest, revision_floating, total_bytes, last_error`

// SaveControllerKnowledge replaces all controller Knowledge rows in a single transaction.
// Identity is uuid (primary key) plus unique name.
func (d *DB) SaveControllerKnowledge(items []*models.ControllerKnowledge) error {
	tx, err := d.Conn().Begin()
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	if _, err := tx.Exec("DELETE FROM controller_knowledge"); err != nil {
		return fmt.Errorf("failed to clear controller_knowledge: %w", err)
	}

	seenUUID := make(map[string]struct{}, len(items))
	seenName := make(map[string]struct{}, len(items))
	for _, item := range items {
		if item == nil {
			continue
		}
		item.NormalizeDefaults()
		if item.UUID == "" {
			return errors.New("controller knowledge uuid is required")
		}
		if item.Name == "" {
			return errors.New("controller knowledge name is required")
		}
		if _, dup := seenUUID[item.UUID]; dup {
			return fmt.Errorf("duplicate controller knowledge uuid %q", item.UUID)
		}
		if _, dup := seenName[item.Name]; dup {
			return fmt.Errorf("duplicate controller knowledge name %q", item.Name)
		}
		seenUUID[item.UUID] = struct{}{}
		seenName[item.Name] = struct{}{}
		if _, err := tx.Exec(
			`INSERT INTO controller_knowledge (
				uuid, name, registry_id, repo, revision, files_json, format,
				state, resolved_revision, digest, revision_floating, total_bytes, last_error, updated_at
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			item.UUID, item.Name, item.RegistryID, item.Repo, item.Revision, item.FilesJSON, item.Format,
			item.State, item.ResolvedRevision, item.Digest, boolToInt(item.RevisionFloating),
			item.TotalBytes, item.LastError, time.Now().Unix(),
		); err != nil {
			return fmt.Errorf("failed to insert controller knowledge %s: %w", item.UUID, err)
		}
	}
	return tx.Commit()
}

// UpsertControllerKnowledge inserts or updates one controller Knowledge row by uuid.
// Name must stay unique across controller Knowledge rows.
func (d *DB) UpsertControllerKnowledge(item *models.ControllerKnowledge) error {
	if item == nil {
		return errors.New("knowledge is nil")
	}
	item.NormalizeDefaults()
	if item.UUID == "" {
		return errors.New("controller knowledge uuid is required")
	}
	if item.Name == "" {
		return errors.New("controller knowledge name is required")
	}
	if item.RegistryID <= 0 {
		return errors.New("controller knowledge registry_id is required")
	}

	_, err := d.Conn().Exec(`INSERT INTO controller_knowledge (
		uuid, name, registry_id, repo, revision, files_json, format,
		state, resolved_revision, digest, revision_floating, total_bytes, last_error, updated_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	ON CONFLICT(uuid) DO UPDATE SET
		name=excluded.name,
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
		item.UUID, item.Name, item.RegistryID, item.Repo, item.Revision, item.FilesJSON, item.Format,
		item.State, item.ResolvedRevision, item.Digest, boolToInt(item.RevisionFloating),
		item.TotalBytes, item.LastError, time.Now().Unix(),
	)
	if err != nil {
		return fmt.Errorf("failed to upsert controller knowledge: %w", err)
	}
	return nil
}

// LoadControllerKnowledge retrieves all controller Knowledge rows ordered by name.
func (d *DB) LoadControllerKnowledge() ([]*models.ControllerKnowledge, error) {
	rows, err := d.Conn().Query(
		"SELECT " + controllerKnowledgeSelectColumns + " FROM controller_knowledge ORDER BY name",
	)
	if err != nil {
		return nil, fmt.Errorf("failed to query controller_knowledge: %w", err)
	}
	defer func() {
		_ = rows.Close()
	}()

	items := make([]*models.ControllerKnowledge, 0)
	for rows.Next() {
		item, scanErr := scanControllerKnowledge(rows)
		if scanErr != nil {
			return nil, fmt.Errorf("failed to scan controller knowledge: %w", scanErr)
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

// GetControllerKnowledgeByUUID returns one fleet-desired Knowledge row by uuid.
func (d *DB) GetControllerKnowledgeByUUID(uuid string) (*models.ControllerKnowledge, error) {
	row := d.Conn().QueryRow(
		"SELECT "+controllerKnowledgeSelectColumns+" FROM controller_knowledge WHERE uuid = ?",
		strings.TrimSpace(uuid),
	)
	return scanControllerKnowledge(row)
}

// GetControllerKnowledgeByName returns the fleet-desired Knowledge row for a unique name.
func (d *DB) GetControllerKnowledgeByName(name string) (*models.ControllerKnowledge, error) {
	row := d.Conn().QueryRow(
		"SELECT "+controllerKnowledgeSelectColumns+" FROM controller_knowledge WHERE name = ?",
		strings.TrimSpace(name),
	)
	return scanControllerKnowledge(row)
}

// ClearControllerKnowledge removes all controller Knowledge rows.
func (d *DB) ClearControllerKnowledge() error {
	_, err := d.Conn().Exec("DELETE FROM controller_knowledge")
	return err
}

func scanControllerKnowledge(s interface{ Scan(dest ...any) error }) (*models.ControllerKnowledge, error) {
	item := &models.ControllerKnowledge{}
	var revisionFloating int
	if err := s.Scan(
		&item.UUID, &item.Name, &item.RegistryID, &item.Repo, &item.Revision, &item.FilesJSON, &item.Format,
		&item.State, &item.ResolvedRevision, &item.Digest, &revisionFloating, &item.TotalBytes, &item.LastError,
	); err != nil {
		return nil, err
	}
	item.RevisionFloating = intToBool(revisionFloating)
	item.NormalizeDefaults()
	return item, nil
}
