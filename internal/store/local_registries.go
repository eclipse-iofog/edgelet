package store

import (
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/eclipse-iofog/edgelet/internal/models"
)

const registrySelectColumns = "id, url, is_public, user_name, password, user_email, type, ca_b64, insecure"

// UpsertLocalRegistry inserts or updates one local registry row.
func (d *DB) UpsertLocalRegistry(reg *models.Registry) error {
	if reg == nil {
		return errors.New("registry is nil")
	}
	reg.NormalizeDefaults()
	if err := d.CheckLocalRegistryCollision(reg); err != nil {
		return err
	}
	_, err := d.Conn().Exec(
		`INSERT OR REPLACE INTO local_registries (id, url, is_public, user_name, password, user_email, type, ca_b64, insecure, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		reg.ID, reg.URL, boolToInt(reg.IsPublic), reg.UserName, reg.Password, reg.UserEmail,
		reg.Type, reg.CAB64, boolToInt(reg.Insecure),
		time.Now().Unix(),
	)
	return err
}

// CheckLocalRegistryCollision returns a validate error when id exists with a different (type, url).
func (d *DB) CheckLocalRegistryCollision(reg *models.Registry) error {
	if reg == nil {
		return errors.New("registry is nil")
	}
	existing, err := d.GetLocalRegistry(reg.ID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	return models.RegistryIdentityCollision(existing, reg)
}

// LoadLocalRegistries retrieves all local registries ordered by id.
func (d *DB) LoadLocalRegistries() ([]*models.Registry, error) {
	rows, err := d.Conn().Query(
		"SELECT " + registrySelectColumns + " FROM local_registries ORDER BY id",
	)
	if err != nil {
		return nil, fmt.Errorf("failed to query local_registries: %w", err)
	}
	defer func() {
		_ = rows.Close()
	}()

	var result []*models.Registry
	for rows.Next() {
		reg, err := scanRegistry(rows)
		if err != nil {
			return nil, fmt.Errorf("failed to scan local registry: %w", err)
		}
		result = append(result, reg)
	}
	if result == nil {
		result = make([]*models.Registry, 0)
	}
	return result, rows.Err()
}

// EnsureDefaultLocalRegistries ensures built-in public registries exist
// (docker.io, from_cache, and Hugging Face Hub).
func (d *DB) EnsureDefaultLocalRegistries() error {
	for _, reg := range models.BuiltInLocalRegistries() {
		if err := d.UpsertLocalRegistry(reg); err != nil {
			return err
		}
	}
	return nil
}

// DeleteLocalRegistry deletes a local registry by ID.
func (d *DB) DeleteLocalRegistry(id int) error {
	_, err := d.Conn().Exec("DELETE FROM local_registries WHERE id = ?", id)
	return err
}

// GetLocalRegistry gets one local registry by ID.
func (d *DB) GetLocalRegistry(id int) (*models.Registry, error) {
	row := d.Conn().QueryRow(
		"SELECT "+registrySelectColumns+" FROM local_registries WHERE id = ?",
		id,
	)
	return scanRegistryRow(row)
}

// NextLocalRegistryID allocates the next local registry id after built-in rows.
func (d *DB) NextLocalRegistryID() (int, error) {
	floor := models.HighestBuiltInLocalRegistryID()
	var maxID int
	if err := d.Conn().QueryRow("SELECT COALESCE(MAX(id), ?) FROM local_registries", floor).Scan(&maxID); err != nil {
		return 0, fmt.Errorf("failed to allocate local registry id: %w", err)
	}
	if maxID < floor {
		maxID = floor
	}
	return maxID + 1, nil
}
