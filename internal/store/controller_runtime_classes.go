package store

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/eclipse-iofog/edgelet/internal/models"
)

const controllerRuntimeClassSelectColumns = `name, handler`

// SaveControllerRuntimeClasses replaces all fleet RuntimeClass rows in a single
// transaction. Empty list clears the table. Identity is unique name.
func (d *DB) SaveControllerRuntimeClasses(items []*models.ControllerRuntimeClass) error {
	tx, err := d.Conn().Begin()
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	if _, err := tx.Exec("DELETE FROM controller_runtime_classes"); err != nil {
		return fmt.Errorf("failed to clear controller_runtime_classes: %w", err)
	}

	seenName := make(map[string]struct{}, len(items))
	nowSec := time.Now().Unix()
	for _, item := range items {
		if item == nil {
			continue
		}
		item.NormalizeDefaults()
		if item.Name == "" {
			return errors.New("controller runtime class name is required")
		}
		if item.Handler == "" {
			return errors.New("controller runtime class handler is required")
		}
		if _, dup := seenName[item.Name]; dup {
			return fmt.Errorf("duplicate controller runtime class name %q", item.Name)
		}
		seenName[item.Name] = struct{}{}
		if _, err := tx.Exec(
			`INSERT INTO controller_runtime_classes (name, handler, updated_at)
			 VALUES (?, ?, ?)`,
			item.Name, item.Handler, nowSec,
		); err != nil {
			return fmt.Errorf("failed to insert controller runtime class %s: %w", item.Name, err)
		}
	}
	return tx.Commit()
}

// LoadControllerRuntimeClasses retrieves all fleet RuntimeClass rows ordered by name.
func (d *DB) LoadControllerRuntimeClasses() ([]*models.ControllerRuntimeClass, error) {
	rows, err := d.Conn().Query(
		"SELECT " + controllerRuntimeClassSelectColumns + " FROM controller_runtime_classes ORDER BY name",
	)
	if err != nil {
		return nil, fmt.Errorf("failed to query controller_runtime_classes: %w", err)
	}
	defer func() {
		_ = rows.Close()
	}()

	items := make([]*models.ControllerRuntimeClass, 0)
	for rows.Next() {
		item, scanErr := scanControllerRuntimeClass(rows)
		if scanErr != nil {
			return nil, fmt.Errorf("failed to scan controller runtime class: %w", scanErr)
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

// GetControllerRuntimeClass returns the fleet-desired RuntimeClass row for a name.
func (d *DB) GetControllerRuntimeClass(name string) (*models.ControllerRuntimeClass, error) {
	row := d.Conn().QueryRow(
		"SELECT "+controllerRuntimeClassSelectColumns+" FROM controller_runtime_classes WHERE name = ?",
		strings.TrimSpace(strings.ToLower(name)),
	)
	item, err := scanControllerRuntimeClass(row)
	if err != nil {
		return nil, err
	}
	return item, nil
}

// ClearControllerRuntimeClasses removes all fleet RuntimeClass rows.
func (d *DB) ClearControllerRuntimeClasses() error {
	_, err := d.Conn().Exec("DELETE FROM controller_runtime_classes")
	return err
}

// IsManagedFleetRuntimeClass reports whether name is a controller-desired
// RuntimeClass while the node is provisioned. Unprovisioned nodes never treat
// a name as fleet-managed.
func (d *DB) IsManagedFleetRuntimeClass(name string, provisioned bool) (bool, error) {
	if !provisioned {
		return false, nil
	}
	normalized := strings.TrimSpace(strings.ToLower(name))
	if normalized == "" {
		return false, nil
	}
	var count int
	if err := d.Conn().QueryRow(
		`SELECT COUNT(*) FROM controller_runtime_classes WHERE name = ?`,
		normalized,
	).Scan(&count); err != nil {
		return false, fmt.Errorf("failed to look up controller runtime class %q: %w", normalized, err)
	}
	return count > 0, nil
}

func scanControllerRuntimeClass(s interface{ Scan(dest ...any) error }) (*models.ControllerRuntimeClass, error) {
	item := &models.ControllerRuntimeClass{}
	if err := s.Scan(&item.Name, &item.Handler); err != nil {
		return nil, err
	}
	item.NormalizeDefaults()
	return item, nil
}
