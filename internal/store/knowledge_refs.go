package store

import (
	"errors"
	"fmt"
	"strings"
)

// ReplaceKnowledgeRefs replaces knowledge catalog binds for one microservice UUID.
func (d *DB) ReplaceKnowledgeRefs(msUUID string, names []string) error {
	msUUID = strings.TrimSpace(msUUID)
	if msUUID == "" {
		return errors.New("microservice uuid is required")
	}
	tx, err := d.Conn().Begin()
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	if _, err := tx.Exec("DELETE FROM knowledge_refs WHERE microservice_uuid = ?", msUUID); err != nil {
		return fmt.Errorf("failed to clear knowledge refs: %w", err)
	}

	seen := make(map[string]struct{}, len(names))
	for _, name := range names {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		if _, dup := seen[name]; dup {
			continue
		}
		seen[name] = struct{}{}
		if _, err := tx.Exec(
			`INSERT INTO knowledge_refs (microservice_uuid, knowledge_name) VALUES (?, ?)`,
			msUUID, name,
		); err != nil {
			return fmt.Errorf("failed to insert knowledge ref %s: %w", name, err)
		}
	}
	return tx.Commit()
}

// InsertKnowledgeRefs records knowledge catalog binds for one microservice UUID.
func (d *DB) InsertKnowledgeRefs(msUUID string, names []string) error {
	msUUID = strings.TrimSpace(msUUID)
	if msUUID == "" {
		return errors.New("microservice uuid is required")
	}
	tx, err := d.Conn().Begin()
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	seen := make(map[string]struct{}, len(names))
	for _, name := range names {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		if _, dup := seen[name]; dup {
			continue
		}
		seen[name] = struct{}{}
		if _, err := tx.Exec(
			`INSERT OR IGNORE INTO knowledge_refs (microservice_uuid, knowledge_name) VALUES (?, ?)`,
			msUUID, name,
		); err != nil {
			return fmt.Errorf("failed to insert knowledge ref %s: %w", name, err)
		}
	}
	return tx.Commit()
}

// ListKnowledgeRefs returns knowledge names bound to one microservice UUID.
func (d *DB) ListKnowledgeRefs(msUUID string) ([]string, error) {
	rows, err := d.Conn().Query(
		`SELECT knowledge_name FROM knowledge_refs WHERE microservice_uuid = ? ORDER BY knowledge_name`,
		strings.TrimSpace(msUUID),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to list knowledge refs: %w", err)
	}
	defer func() {
		_ = rows.Close()
	}()

	names := make([]string, 0)
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, fmt.Errorf("failed to scan knowledge ref: %w", err)
		}
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		names = append(names, name)
	}
	return names, rows.Err()
}

// ClearKnowledgeRefs removes knowledge catalog binds for one microservice UUID.
func (d *DB) ClearKnowledgeRefs(msUUID string) error {
	_, err := d.Conn().Exec(
		"DELETE FROM knowledge_refs WHERE microservice_uuid = ?",
		strings.TrimSpace(msUUID),
	)
	if err != nil {
		return fmt.Errorf("failed to clear knowledge refs: %w", err)
	}
	return nil
}

// CountKnowledgeRefs returns how many catalog binds exist for one Knowledge name.
func (d *DB) CountKnowledgeRefs(name string) (int, error) {
	var n int
	err := d.Conn().QueryRow(
		"SELECT COUNT(*) FROM knowledge_refs WHERE knowledge_name = ?",
		strings.TrimSpace(name),
	).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("failed to count knowledge refs: %w", err)
	}
	return n, nil
}

// DeleteKnowledgeRefs removes catalog binds for one Knowledge name.
func (d *DB) DeleteKnowledgeRefs(name string) error {
	_, err := d.Conn().Exec("DELETE FROM knowledge_refs WHERE knowledge_name = ?", strings.TrimSpace(name))
	return err
}

// ListPruneKeepKnowledgeNames returns names dangling prune must keep:
// fleet-desired controller Knowledge names plus catalog binds when includeWorkloadRefs is true.
func (d *DB) ListPruneKeepKnowledgeNames(includeWorkloadRefs bool) ([]string, error) {
	query := `SELECT name FROM controller_knowledge`
	if includeWorkloadRefs {
		query = `
		SELECT name FROM controller_knowledge
		UNION
		SELECT knowledge_name FROM knowledge_refs`
	}
	rows, err := d.Conn().Query(query)
	if err != nil {
		return nil, fmt.Errorf("failed to list prune keep knowledge names: %w", err)
	}
	defer func() {
		_ = rows.Close()
	}()

	names := make([]string, 0)
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, fmt.Errorf("failed to scan prune keep knowledge name: %w", err)
		}
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		names = append(names, name)
	}
	return names, rows.Err()
}
