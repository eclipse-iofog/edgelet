package store

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	_ "modernc.org/sqlite" // driver registration
)

const dbFileName = "edgelet.db"

var (
	instance *DB
	once     sync.Once
)

// DB wraps the SQLite database connection
type DB struct {
	db   *sql.DB
	path string
	mu   sync.RWMutex
}

// GetInstance returns the singleton DB instance
func GetInstance() *DB {
	once.Do(func() {
		instance = &DB{}
	})
	return instance
}

// Open opens (or creates) the SQLite database at the given directory and runs migrations
func (d *DB) Open(dir string) error {
	d.mu.Lock()

	if err := os.MkdirAll(dir, 0700); err != nil {
		d.mu.Unlock()
		return fmt.Errorf("failed to create db directory: %w", err)
	}

	d.path = filepath.Join(dir, dbFileName)
	db, err := sql.Open("sqlite", d.path+"?_journal_mode=WAL&_synchronous=NORMAL&_foreign_keys=ON")
	if err != nil {
		d.mu.Unlock()
		return fmt.Errorf("failed to open sqlite db: %w", err)
	}

	// Single writer to avoid lock contention
	db.SetMaxOpenConns(1)

	d.db = db
	if err := d.migrate(); err != nil {
		d.mu.Unlock()
		return err
	}
	if err := d.checkIntegrity(); err != nil {
		d.mu.Unlock()
		return err
	}
	d.mu.Unlock()

	cpUUID := ""
	if cp, found, err := d.GetSystemControlPlane(); err == nil && found && cp != nil {
		cpUUID = strings.TrimSpace(cp.ControllerUUID)
	}
	return d.SeedPersistentVolumesFromDisk(dir, cpUUID)
}

// Close checkpoints the WAL (TRUNCATE) then closes the connection.
// Production path: internal/supervisor.(*Supervisor).Stop() calls store.GetInstance().Close() last.
func (d *DB) Close() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.db == nil {
		return nil
	}
	if err := d.checkpointWAL(); err != nil {
		return fmt.Errorf("wal checkpoint: %w", err)
	}
	err := d.db.Close()
	d.db = nil
	return err
}

func (d *DB) checkIntegrity() error {
	var result string
	if err := d.db.QueryRow("PRAGMA integrity_check").Scan(&result); err != nil {
		return fmt.Errorf("integrity_check: %w", err)
	}
	if result != "ok" {
		return fmt.Errorf("sqlite integrity_check failed: %s", result)
	}
	return nil
}

func (d *DB) checkpointWAL() error {
	_, err := d.db.Exec("PRAGMA wal_checkpoint(TRUNCATE)")
	return err
}

// Conn returns the underlying *sql.DB
func (d *DB) Conn() *sql.DB {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.db
}
