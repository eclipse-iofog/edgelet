package store

import (
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/eclipse-iofog/edgelet/internal/models"
)

// SaveControllerRegistries replaces all controller registry rows in a single transaction.
func (d *DB) SaveControllerRegistries(registries []*models.Registry) error {
	tx, err := d.Conn().Begin()
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	if _, err := tx.Exec("DELETE FROM controller_registries"); err != nil {
		return fmt.Errorf("failed to clear controller_registries: %w", err)
	}

	for _, reg := range registries {
		if err := insertRegistry(tx, reg); err != nil {
			return fmt.Errorf("failed to insert registry %d: %w", reg.ID, err)
		}
	}

	return tx.Commit()
}

// LoadControllerRegistries retrieves all controller registries ordered by id.
func (d *DB) LoadControllerRegistries() ([]*models.Registry, error) {
	rows, err := d.Conn().Query(
		"SELECT " + registrySelectColumns + " FROM controller_registries ORDER BY id",
	)
	if err != nil {
		return nil, fmt.Errorf("failed to query controller_registries: %w", err)
	}
	defer func() {
		_ = rows.Close()
	}()

	var result []*models.Registry
	for rows.Next() {
		reg, err := scanRegistry(rows)
		if err != nil {
			return nil, fmt.Errorf("failed to scan registry: %w", err)
		}
		result = append(result, reg)
	}
	if result == nil {
		result = make([]*models.Registry, 0)
	}
	return result, rows.Err()
}

// ClearControllerRegistries removes all controller registry rows (used on deprovision).
func (d *DB) ClearControllerRegistries() error {
	_, err := d.Conn().Exec("DELETE FROM controller_registries")
	return err
}

func insertRegistry(tx *sql.Tx, reg *models.Registry) error {
	if reg != nil {
		reg.NormalizeDefaults()
	}
	_, err := tx.Exec(
		`INSERT OR REPLACE INTO controller_registries (id, url, is_public, user_name, password, user_email, type, ca_b64, insecure, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		reg.ID, reg.URL, boolToInt(reg.IsPublic), reg.UserName, reg.Password, reg.UserEmail,
		reg.Type, reg.CAB64, boolToInt(reg.Insecure),
		time.Now().Unix(),
	)
	return err
}

func scanRegistry(rows *sql.Rows) (*models.Registry, error) {
	return scanRegistryRow(rows)
}

func scanRegistryRow(s interface{ Scan(dest ...any) error }) (*models.Registry, error) {
	reg := &models.Registry{}
	var isPublic, insecure int
	if err := s.Scan(
		&reg.ID, &reg.URL, &isPublic, &reg.UserName, &reg.Password, &reg.UserEmail,
		&reg.Type, &reg.CAB64, &insecure,
	); err != nil {
		return nil, err
	}
	reg.IsPublic = intToBool(isPublic)
	reg.Insecure = intToBool(insecure)
	reg.NormalizeDefaults()
	return reg, nil
}

// GetControllerRegistry gets one controller registry by ID.
func (d *DB) GetControllerRegistry(id int) (*models.Registry, error) {
	row := d.Conn().QueryRow(
		"SELECT "+registrySelectColumns+" FROM controller_registries WHERE id = ?",
		id,
	)
	return scanRegistryRow(row)
}

// LookupRegistry returns a local registry row, falling back to a controller registry.
func (d *DB) LookupRegistry(id int) (*models.Registry, error) {
	reg, err := d.GetLocalRegistry(id)
	if err == nil && reg != nil {
		return reg, nil
	}
	ctrl, ctrlErr := d.GetControllerRegistry(id)
	if ctrlErr == nil && ctrl != nil {
		return ctrl, nil
	}
	if err != nil {
		return nil, err
	}
	return nil, fmt.Errorf("registry %d not found", id)
}

// UpsertControllerRegistry inserts or updates one controller registry row.
func (d *DB) UpsertControllerRegistry(reg *models.Registry) error {
	if reg == nil {
		return errors.New("registry is nil")
	}
	reg.NormalizeDefaults()
	_, err := d.Conn().Exec(
		`INSERT OR REPLACE INTO controller_registries (id, url, is_public, user_name, password, user_email, type, ca_b64, insecure, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		reg.ID, reg.URL, boolToInt(reg.IsPublic), reg.UserName, reg.Password, reg.UserEmail,
		reg.Type, reg.CAB64, boolToInt(reg.Insecure),
		time.Now().Unix(),
	)
	return err
}

// EnsureDefaultControllerRegistries ensures docker.io and from_cache defaults exist.
// Hugging Face Hub is a local-only built-in and is not seeded here.
func (d *DB) EnsureDefaultControllerRegistries() error {
	for _, reg := range models.BuiltInControllerRegistries() {
		if err := d.UpsertControllerRegistry(reg); err != nil {
			return err
		}
	}
	return nil
}
