package store

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/eclipse-iofog/edgelet/internal/models"
)

const controllerModelSelectColumns = `uuid, name, repo, revision, registry_id, files_json, format`

// SaveControllerModels replaces all controller model rows in a single transaction.
// Identity is uuid (primary key) plus unique name.
func (d *DB) SaveControllerModels(items []*models.ControllerModel) error {
	tx, err := d.Conn().Begin()
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	if _, err := tx.Exec("DELETE FROM controller_models"); err != nil {
		return fmt.Errorf("failed to clear controller_models: %w", err)
	}

	seenUUID := make(map[string]struct{}, len(items))
	seenName := make(map[string]struct{}, len(items))
	for _, item := range items {
		if item == nil {
			continue
		}
		item.NormalizeDefaults()
		if item.UUID == "" {
			return errors.New("controller model uuid is required")
		}
		if item.Name == "" {
			return errors.New("controller model name is required")
		}
		if _, dup := seenUUID[item.UUID]; dup {
			return fmt.Errorf("duplicate controller model uuid %q", item.UUID)
		}
		if _, dup := seenName[item.Name]; dup {
			return fmt.Errorf("duplicate controller model name %q", item.Name)
		}
		seenUUID[item.UUID] = struct{}{}
		seenName[item.Name] = struct{}{}
		if _, err := tx.Exec(
			`INSERT INTO controller_models (uuid, name, repo, revision, registry_id, files_json, format, updated_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			item.UUID, item.Name, item.Repo, item.Revision, item.RegistryID, item.FilesJSON, item.Format,
			time.Now().Unix(),
		); err != nil {
			return fmt.Errorf("failed to insert controller model %s: %w", item.UUID, err)
		}
	}
	return tx.Commit()
}

// LoadControllerModels retrieves all controller model rows ordered by name.
func (d *DB) LoadControllerModels() ([]*models.ControllerModel, error) {
	rows, err := d.Conn().Query(
		"SELECT " + controllerModelSelectColumns + " FROM controller_models ORDER BY name",
	)
	if err != nil {
		return nil, fmt.Errorf("failed to query controller_models: %w", err)
	}
	defer func() {
		_ = rows.Close()
	}()

	items := make([]*models.ControllerModel, 0)
	for rows.Next() {
		item, scanErr := scanControllerModel(rows)
		if scanErr != nil {
			return nil, fmt.Errorf("failed to scan controller model: %w", scanErr)
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

// GetControllerModelByName returns the fleet-desired model row for a unique name.
func (d *DB) GetControllerModelByName(name string) (*models.ControllerModel, error) {
	row := d.Conn().QueryRow(
		"SELECT "+controllerModelSelectColumns+" FROM controller_models WHERE name = ?",
		strings.TrimSpace(name),
	)
	item, err := scanControllerModel(row)
	if err != nil {
		return nil, err
	}
	return item, nil
}

// ClearControllerModels removes all controller model rows.
func (d *DB) ClearControllerModels() error {
	_, err := d.Conn().Exec("DELETE FROM controller_models")
	return err
}

func scanControllerModel(s interface{ Scan(dest ...any) error }) (*models.ControllerModel, error) {
	item := &models.ControllerModel{}
	if err := s.Scan(
		&item.UUID, &item.Name, &item.Repo, &item.Revision, &item.RegistryID, &item.FilesJSON, &item.Format,
	); err != nil {
		return nil, err
	}
	item.NormalizeDefaults()
	return item, nil
}
