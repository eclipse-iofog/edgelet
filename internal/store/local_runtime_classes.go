package store

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/eclipse-iofog/edgelet/internal/models"
)

const localRuntimeClassSelectColumns = `name, handler, source, created_at, updated_at`

// UpsertLocalRuntimeClass inserts or updates one RuntimeClass row.
func (d *DB) UpsertLocalRuntimeClass(rc *models.LocalRuntimeClass) error {
	if rc == nil {
		return errors.New("runtime class is nil")
	}
	rc.Normalize()
	if rc.Name == "" {
		return errors.New("runtime class name is required")
	}
	if rc.Handler == "" {
		return errors.New("runtime class handler is required")
	}
	if !models.ValidRuntimeClassSource(rc.Source) {
		return fmt.Errorf("invalid runtime class source %q", rc.Source)
	}

	_, err := d.Conn().Exec(
		`INSERT INTO local_runtime_classes (name, handler, source, updated_at)
		 VALUES (?, ?, ?, ?)
		 ON CONFLICT(name) DO UPDATE SET
		 	handler=excluded.handler,
		 	source=excluded.source,
		 	updated_at=excluded.updated_at`,
		rc.Name, rc.Handler, rc.Source, time.Now().Unix(),
	)
	if err != nil {
		return fmt.Errorf("failed to upsert local runtime class: %w", err)
	}
	return nil
}

// ListLocalRuntimeClasses returns all applied RuntimeClass rows ordered by name.
func (d *DB) ListLocalRuntimeClasses() ([]*models.LocalRuntimeClass, error) {
	rows, err := d.Conn().Query(
		"SELECT " + localRuntimeClassSelectColumns + " FROM local_runtime_classes ORDER BY name",
	)
	if err != nil {
		return nil, fmt.Errorf("failed to query local_runtime_classes: %w", err)
	}
	defer func() {
		_ = rows.Close()
	}()

	items := make([]*models.LocalRuntimeClass, 0)
	for rows.Next() {
		rc, scanErr := scanLocalRuntimeClass(rows)
		if scanErr != nil {
			return nil, fmt.Errorf("failed to scan local runtime class: %w", scanErr)
		}
		items = append(items, rc)
	}
	return items, rows.Err()
}

// GetLocalRuntimeClass returns one RuntimeClass by name.
func (d *DB) GetLocalRuntimeClass(name string) (*models.LocalRuntimeClass, error) {
	normalizedName := strings.TrimSpace(strings.ToLower(name))
	row := d.Conn().QueryRow(
		"SELECT "+localRuntimeClassSelectColumns+" FROM local_runtime_classes WHERE name = ?",
		normalizedName,
	)
	return scanLocalRuntimeClass(row)
}

// DeleteLocalRuntimeClass deletes one RuntimeClass by name.
func (d *DB) DeleteLocalRuntimeClass(name string) error {
	normalizedName := strings.TrimSpace(strings.ToLower(name))
	_, err := d.Conn().Exec(`DELETE FROM local_runtime_classes WHERE name = ?`, normalizedName)
	return err
}

// ClearLocalRuntimeClasses removes all RuntimeClass rows.
func (d *DB) ClearLocalRuntimeClasses() error {
	_, err := d.Conn().Exec(`DELETE FROM local_runtime_classes`)
	return err
}

func scanLocalRuntimeClass(s interface{ Scan(dest ...any) error }) (*models.LocalRuntimeClass, error) {
	rc := &models.LocalRuntimeClass{}
	if err := s.Scan(&rc.Name, &rc.Handler, &rc.Source, &rc.CreatedAt, &rc.UpdatedAt); err != nil {
		return nil, err
	}
	rc.Normalize()
	return rc, nil
}
