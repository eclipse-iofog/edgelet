package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/eclipse-iofog/edgelet/internal/models"
)

const volumeCleanupFileName = "volume-cleanup.json"

const (
	// PersistentVolumeKindWorkload is a user or controller microservice volume.
	PersistentVolumeKindWorkload = "workload"
	// PersistentVolumeKindControlPlane is an on-node controller volume.
	PersistentVolumeKindControlPlane = "controlplane"
)

// PersistentVolumeRecord is one consumer row in the persistent volume ledger.
type PersistentVolumeRecord struct {
	UUID           string
	VolumeName     string
	Scope          string
	Kind           string
	HostPath       string
	CreatedAt      int64
	LastDesiredAt  int64
	UnreferencedAt *int64
}

// PersistentVolumeKeepSet is the set of private UUIDs and shared names that
// must not be destroyed. Listed containers are unioned with desired-state and
// still-referenced ledger rows.
type PersistentVolumeKeepSet struct {
	PrivateUUIDs []string
	SharedNames  []string
}

// UnreferencedPersistentVolume is a workload claim whose last consumer has left
// desired state. Shared names appear once when every consumer is unreferenced.
type UnreferencedPersistentVolume struct {
	UUID             string
	VolumeName       string
	Scope            string
	Kind             string
	HostPath         string
	UnreferencedAt   int64
	LastConsumerUUID string
}

// PersistentVolumeHostPath returns the on-disk directory for a persistent VOLUME.
func PersistentVolumeHostPath(diskDirectory, uuid, name, scope string) string {
	diskDirectory = strings.TrimSpace(diskDirectory)
	uuid = strings.TrimSpace(uuid)
	name = strings.TrimSpace(name)
	if strings.TrimSpace(scope) == models.VolumeScopeShared {
		return filepath.Join(diskDirectory, "volumes", "shared", name)
	}
	return filepath.Join(diskDirectory, "volumes", "data", uuid, name)
}

func canonicalPersistentVolumeKind(kind string) string {
	if strings.TrimSpace(kind) == PersistentVolumeKindControlPlane {
		return PersistentVolumeKindControlPlane
	}
	return PersistentVolumeKindWorkload
}

func (d *DB) diskDirectory() string {
	if d == nil || strings.TrimSpace(d.path) == "" {
		return ""
	}
	return filepath.Dir(d.path)
}

// DiskDirectory is the directory that contains edgelet.db. Persistent VOLUME
// trees live beside the database under volumes/data and volumes/shared.
func (d *DB) DiskDirectory() string {
	return d.diskDirectory()
}

// UpsertPersistentVolume inserts or refreshes a consumer row while the UUID is
// in desired state. Shared rows from different UUIDs coexist for the same name.
func (d *DB) UpsertPersistentVolume(uuid, name, kind, scope, hostPath string) error {
	if d.Conn() == nil {
		return errors.New("database is closed")
	}
	uuid = strings.TrimSpace(uuid)
	name = strings.TrimSpace(name)
	if uuid == "" {
		return errors.New("microservice uuid is required")
	}
	if name == "" {
		return errors.New("volume name is required")
	}

	kind = canonicalPersistentVolumeKind(kind)
	scope = models.CanonicalVolumeScope(scope)
	if kind == PersistentVolumeKindControlPlane {
		scope = models.VolumeScopePrivate
	}
	hostPath = strings.TrimSpace(hostPath)
	if hostPath == "" {
		hostPath = PersistentVolumeHostPath(d.diskDirectory(), uuid, name, scope)
	}

	_, err := d.Conn().Exec(
		`INSERT INTO persistent_volumes (
			ms_uuid, volume_name, scope, kind, host_path, last_desired_at, unreferenced_at
		) VALUES (?, ?, ?, ?, ?, strftime('%s','now'), NULL)
		ON CONFLICT(ms_uuid, volume_name) DO UPDATE SET
			scope = excluded.scope,
			kind = excluded.kind,
			host_path = excluded.host_path,
			last_desired_at = strftime('%s','now'),
			unreferenced_at = NULL`,
		uuid, name, scope, kind, hostPath,
	)
	if err != nil {
		return fmt.Errorf("failed to upsert persistent volume %s/%s: %w", uuid, name, err)
	}
	return nil
}

// UpsertPersistentVolumesFromMappings records VOLUME mappings only.
// BIND and VOLUME_MOUNT never create a shared claim.
func (d *DB) UpsertPersistentVolumesFromMappings(uuid, kind string, mappings []*models.VolumeMapping) error {
	for _, vm := range mappings {
		if vm == nil || vm.Type != models.VolumeMappingTypeVolume {
			continue
		}
		name := strings.TrimSpace(vm.HostDestination)
		if name == "" {
			continue
		}
		if err := d.UpsertPersistentVolume(uuid, name, kind, vm.EffectiveVolumeScope(), ""); err != nil {
			return err
		}
	}
	return nil
}

// MarkPersistentVolumesUnreferenced stamps consumer rows when a UUID leaves
// desired state. Files are not deleted.
func (d *DB) MarkPersistentVolumesUnreferenced(uuid string) error {
	if d.Conn() == nil {
		return errors.New("database is closed")
	}
	uuid = strings.TrimSpace(uuid)
	if uuid == "" {
		return nil
	}
	_, err := d.Conn().Exec(
		`UPDATE persistent_volumes
		 SET unreferenced_at = strftime('%s','now')
		 WHERE ms_uuid = ? AND unreferenced_at IS NULL`,
		uuid,
	)
	if err != nil {
		return fmt.Errorf("failed to mark persistent volumes unreferenced for %s: %w", uuid, err)
	}
	return nil
}

// GetPersistentVolume returns one consumer row.
func (d *DB) GetPersistentVolume(uuid, name string) (*PersistentVolumeRecord, error) {
	if d.Conn() == nil {
		return nil, errors.New("database is closed")
	}
	row := d.Conn().QueryRow(
		`SELECT ms_uuid, volume_name, scope, kind, host_path, created_at, last_desired_at, unreferenced_at
		 FROM persistent_volumes WHERE ms_uuid = ? AND volume_name = ?`,
		strings.TrimSpace(uuid), strings.TrimSpace(name),
	)
	return scanPersistentVolume(row)
}

// ListPersistentVolumes returns every consumer row.
func (d *DB) ListPersistentVolumes() ([]PersistentVolumeRecord, error) {
	rows, err := d.queryPersistentVolumes(
		`SELECT ms_uuid, volume_name, scope, kind, host_path, created_at, last_desired_at, unreferenced_at
		 FROM persistent_volumes ORDER BY ms_uuid, volume_name`,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to list persistent volumes: %w", err)
	}
	return rows, nil
}

func (d *DB) queryPersistentVolumes(query string, args ...any) ([]PersistentVolumeRecord, error) {
	if d.Conn() == nil {
		return nil, errors.New("database is closed")
	}
	rows, err := d.Conn().Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer func() {
		_ = rows.Close()
	}()

	result := make([]PersistentVolumeRecord, 0)
	for rows.Next() {
		rec, scanErr := scanPersistentVolume(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		result = append(result, *rec)
	}
	return result, rows.Err()
}

// ListPersistentVolumesForUUID returns every consumer row for one microservice.
func (d *DB) ListPersistentVolumesForUUID(uuid string) ([]PersistentVolumeRecord, error) {
	rows, err := d.queryPersistentVolumes(
		`SELECT ms_uuid, volume_name, scope, kind, host_path, created_at, last_desired_at, unreferenced_at
		 FROM persistent_volumes WHERE ms_uuid = ? ORDER BY volume_name`,
		strings.TrimSpace(uuid),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to list persistent volumes for %s: %w", uuid, err)
	}
	return rows, nil
}

// ListSharedPersistentVolumeConsumers returns every consumer of a shared name.
func (d *DB) ListSharedPersistentVolumeConsumers(name string) ([]PersistentVolumeRecord, error) {
	rows, err := d.queryPersistentVolumes(
		`SELECT ms_uuid, volume_name, scope, kind, host_path, created_at, last_desired_at, unreferenced_at
		 FROM persistent_volumes WHERE volume_name = ? AND scope = ? ORDER BY ms_uuid`,
		strings.TrimSpace(name), models.VolumeScopeShared,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to list shared volume consumers for %s: %w", name, err)
	}
	return rows, nil
}

// DeletePersistentVolume removes one consumer row after the directory is destroyed.
func (d *DB) DeletePersistentVolume(uuid, name string) error {
	if d.Conn() == nil {
		return errors.New("database is closed")
	}
	uuid = strings.TrimSpace(uuid)
	name = strings.TrimSpace(name)
	if uuid == "" {
		return errors.New("microservice uuid is required")
	}
	if name == "" {
		return errors.New("volume name is required")
	}
	_, err := d.Conn().Exec(
		`DELETE FROM persistent_volumes WHERE ms_uuid = ? AND volume_name = ?`,
		uuid, name,
	)
	if err != nil {
		return fmt.Errorf("failed to delete persistent volume %s/%s: %w", uuid, name, err)
	}
	return nil
}

// DeleteSharedPersistentVolumes removes every consumer row for a shared name.
func (d *DB) DeleteSharedPersistentVolumes(name string) error {
	if d.Conn() == nil {
		return errors.New("database is closed")
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return errors.New("volume name is required")
	}
	_, err := d.Conn().Exec(
		`DELETE FROM persistent_volumes WHERE volume_name = ? AND scope = ?`,
		name, models.VolumeScopeShared,
	)
	if err != nil {
		return fmt.Errorf("failed to delete shared persistent volume %s: %w", name, err)
	}
	return nil
}

// PersistentVolumeKeepSet builds the destroy keep-set from desired state,
// still-referenced ledger rows, and listed containers.
func (d *DB) PersistentVolumeKeepSet(listedPrivateUUIDs, listedSharedNames []string) (PersistentVolumeKeepSet, error) {
	private := make(map[string]struct{})
	shared := make(map[string]struct{})
	addTrimmed(private, listedPrivateUUIDs)
	addTrimmed(shared, listedSharedNames)

	desired, err := d.desiredStateUUIDs()
	if err != nil {
		return PersistentVolumeKeepSet{}, err
	}
	for uuid := range desired {
		private[uuid] = struct{}{}
	}

	rows, err := d.Conn().Query(
		`SELECT ms_uuid, volume_name, scope, unreferenced_at FROM persistent_volumes`,
	)
	if err != nil {
		return PersistentVolumeKeepSet{}, fmt.Errorf("failed to query persistent volume keep-set: %w", err)
	}
	defer func() {
		_ = rows.Close()
	}()

	for rows.Next() {
		var (
			uuid, name, scope string
			unreferencedAt    sql.NullInt64
		)
		if scanErr := rows.Scan(&uuid, &name, &scope, &unreferencedAt); scanErr != nil {
			return PersistentVolumeKeepSet{}, fmt.Errorf("failed to scan persistent volume keep-set: %w", scanErr)
		}
		switch scope {
		case models.VolumeScopePrivate:
			if !unreferencedAt.Valid {
				private[uuid] = struct{}{}
			}
		case models.VolumeScopeShared:
			if !unreferencedAt.Valid {
				shared[name] = struct{}{}
				continue
			}
			if _, ok := desired[uuid]; ok {
				shared[name] = struct{}{}
			}
		}
	}
	if err := rows.Err(); err != nil {
		return PersistentVolumeKeepSet{}, err
	}

	return PersistentVolumeKeepSet{
		PrivateUUIDs: sortedKeys(private),
		SharedNames:  sortedKeys(shared),
	}, nil
}

// PrivateVolumeUUIDKept reports whether a private UUID is in the keep-set.
func (d *DB) PrivateVolumeUUIDKept(uuid string, listedContainerUUIDs []string) (bool, error) {
	uuid = strings.TrimSpace(uuid)
	if uuid == "" {
		return false, nil
	}
	keep, err := d.PersistentVolumeKeepSet(listedContainerUUIDs, nil)
	if err != nil {
		return false, err
	}
	for _, kept := range keep.PrivateUUIDs {
		if kept == uuid {
			return true, nil
		}
	}
	return false, nil
}

// SharedVolumeNameKept reports whether a shared name is in the keep-set.
func (d *DB) SharedVolumeNameKept(name string, listedSharedNames []string) (bool, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return false, nil
	}
	keep, err := d.PersistentVolumeKeepSet(nil, listedSharedNames)
	if err != nil {
		return false, err
	}
	for _, kept := range keep.SharedNames {
		if kept == name {
			return true, nil
		}
	}
	return false, nil
}

// ListUnreferencedWorkloadVolumes returns private orphans and shared names
// whose last consumer is unreferenced. Control-plane kind is omitted.
func (d *DB) ListUnreferencedWorkloadVolumes() ([]UnreferencedPersistentVolume, error) {
	if d.Conn() == nil {
		return nil, errors.New("database is closed")
	}
	desired, err := d.desiredStateUUIDs()
	if err != nil {
		return nil, err
	}

	rows, err := d.Conn().Query(
		`SELECT ms_uuid, volume_name, scope, kind, host_path, unreferenced_at
		 FROM persistent_volumes
		 WHERE kind = ? AND unreferenced_at IS NOT NULL
		 ORDER BY scope, volume_name, ms_uuid`,
		PersistentVolumeKindWorkload,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to list unreferenced persistent volumes: %w", err)
	}
	defer func() {
		_ = rows.Close()
	}()

	type sharedAgg struct {
		name             string
		hostPath         string
		unreferencedAt   int64
		lastConsumerUUID string
		stillDesired     int
	}
	shared := make(map[string]*sharedAgg)
	out := make([]UnreferencedPersistentVolume, 0)

	for rows.Next() {
		var (
			uuid, name, scope, kind, hostPath string
			unreferencedAt                    int64
		)
		if scanErr := rows.Scan(&uuid, &name, &scope, &kind, &hostPath, &unreferencedAt); scanErr != nil {
			return nil, fmt.Errorf("failed to scan unreferenced persistent volume: %w", scanErr)
		}
		if scope == models.VolumeScopeShared {
			agg := shared[name]
			if agg == nil {
				agg = &sharedAgg{
					name:             name,
					hostPath:         hostPath,
					unreferencedAt:   unreferencedAt,
					lastConsumerUUID: uuid,
				}
				shared[name] = agg
			}
			if unreferencedAt >= agg.unreferencedAt {
				agg.unreferencedAt = unreferencedAt
				agg.lastConsumerUUID = uuid
			}
			if _, ok := desired[uuid]; ok {
				agg.stillDesired++
			}
			continue
		}
		if _, ok := desired[uuid]; ok {
			continue
		}
		out = append(out, UnreferencedPersistentVolume{
			UUID:             uuid,
			VolumeName:       name,
			Scope:            scope,
			Kind:             kind,
			HostPath:         hostPath,
			UnreferencedAt:   unreferencedAt,
			LastConsumerUUID: uuid,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	liveShared, err := d.liveSharedConsumerCounts()
	if err != nil {
		return nil, err
	}
	sharedNames := make([]string, 0, len(shared))
	for name := range shared {
		sharedNames = append(sharedNames, name)
	}
	slices.Sort(sharedNames)
	for _, name := range sharedNames {
		agg := shared[name]
		if liveShared[name] > 0 || agg.stillDesired > 0 {
			continue
		}
		out = append(out, UnreferencedPersistentVolume{
			VolumeName:       name,
			Scope:            models.VolumeScopeShared,
			Kind:             PersistentVolumeKindWorkload,
			HostPath:         agg.hostPath,
			UnreferencedAt:   agg.unreferencedAt,
			LastConsumerUUID: agg.lastConsumerUUID,
		})
	}
	return out, nil
}

func (d *DB) liveSharedConsumerCounts() (map[string]int, error) {
	rows, err := d.Conn().Query(
		`SELECT volume_name, COUNT(*)
		 FROM persistent_volumes
		 WHERE kind = ? AND scope = ? AND unreferenced_at IS NULL
		 GROUP BY volume_name`,
		PersistentVolumeKindWorkload, models.VolumeScopeShared,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to count live shared volume consumers: %w", err)
	}
	defer func() {
		_ = rows.Close()
	}()
	out := make(map[string]int)
	for rows.Next() {
		var name string
		var n int
		if err := rows.Scan(&name, &n); err != nil {
			return nil, fmt.Errorf("failed to scan shared volume consumers: %w", err)
		}
		out[name] = n
	}
	return out, rows.Err()
}

// SeedPersistentVolumesFromDisk records existing private data directories.
// It does not walk volumes/shared or delete files.
func (d *DB) SeedPersistentVolumesFromDisk(diskDirectory, cpUUID string) error {
	if d.Conn() == nil {
		return errors.New("database is closed")
	}
	diskDirectory = strings.TrimSpace(diskDirectory)
	if diskDirectory == "" {
		return nil
	}
	cpUUID = strings.TrimSpace(cpUUID)
	dataRoot := filepath.Join(diskDirectory, "volumes", "data")
	uuidEntries, err := os.ReadDir(dataRoot)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read persistent volume data directory: %w", err)
	}

	desired, err := d.desiredStateUUIDs()
	if err != nil {
		return err
	}

	for _, uuidEntry := range uuidEntries {
		if !uuidEntry.IsDir() {
			continue
		}
		uuid := uuidEntry.Name()
		nameEntries, readErr := os.ReadDir(filepath.Join(dataRoot, uuid))
		if readErr != nil {
			return fmt.Errorf("read persistent volume uuid directory %s: %w", uuid, readErr)
		}
		kind := PersistentVolumeKindWorkload
		if cpUUID != "" && uuid == cpUUID {
			kind = PersistentVolumeKindControlPlane
		}
		_, stillDesired := desired[uuid]
		for _, nameEntry := range nameEntries {
			if !nameEntry.IsDir() {
				continue
			}
			name := nameEntry.Name()
			hostPath := PersistentVolumeHostPath(diskDirectory, uuid, name, models.VolumeScopePrivate)
			if stillDesired {
				_, err = d.Conn().Exec(
					`INSERT OR IGNORE INTO persistent_volumes (
						ms_uuid, volume_name, scope, kind, host_path, unreferenced_at
					) VALUES (?, ?, ?, ?, ?, NULL)`,
					uuid, name, models.VolumeScopePrivate, kind, hostPath,
				)
			} else {
				_, err = d.Conn().Exec(
					`INSERT OR IGNORE INTO persistent_volumes (
						ms_uuid, volume_name, scope, kind, host_path, unreferenced_at
					) VALUES (?, ?, ?, ?, ?, strftime('%s','now'))`,
					uuid, name, models.VolumeScopePrivate, kind, hostPath,
				)
			}
			if err != nil {
				return fmt.Errorf("failed to seed persistent volume %s/%s: %w", uuid, name, err)
			}
		}
	}
	return nil
}

func volumeMappingsFromManifestYAML(manifestYAML string) []*models.VolumeMapping {
	if strings.TrimSpace(manifestYAML) == "" {
		return nil
	}
	doc := &models.LocalDeployManifest{}
	if err := yaml.Unmarshal([]byte(manifestYAML), doc); err != nil {
		return nil
	}
	built := models.BuildMicroserviceFromLocalManifest(doc, "local", "image")
	if built == nil {
		return nil
	}
	return built.VolumeMappings
}

func (d *DB) desiredStateUUIDs() (map[string]struct{}, error) {
	out := make(map[string]struct{})
	if err := d.collectUUIDs(`SELECT uuid FROM controller_microservices`, out); err != nil {
		return nil, err
	}
	if err := d.collectUUIDs(`SELECT local_uuid FROM local_workloads`, out); err != nil {
		return nil, err
	}
	cp, found, err := d.GetSystemControlPlane()
	if err != nil {
		return nil, err
	}
	if found && cp != nil {
		if uuid := strings.TrimSpace(cp.ControllerUUID); uuid != "" {
			out[uuid] = struct{}{}
		}
	}
	return out, nil
}

func (d *DB) collectUUIDs(query string, out map[string]struct{}) error {
	rows, err := d.Conn().Query(query)
	if err != nil {
		return fmt.Errorf("failed to query desired-state uuids: %w", err)
	}
	defer func() {
		_ = rows.Close()
	}()
	for rows.Next() {
		var uuid string
		if err := rows.Scan(&uuid); err != nil {
			return fmt.Errorf("failed to scan desired-state uuid: %w", err)
		}
		uuid = strings.TrimSpace(uuid)
		if uuid == "" {
			continue
		}
		out[uuid] = struct{}{}
	}
	return rows.Err()
}

type persistentVolumeScanner interface {
	Scan(dest ...any) error
}

func scanPersistentVolume(row persistentVolumeScanner) (*PersistentVolumeRecord, error) {
	var rec PersistentVolumeRecord
	var unreferenced sql.NullInt64
	if err := row.Scan(
		&rec.UUID, &rec.VolumeName, &rec.Scope, &rec.Kind, &rec.HostPath,
		&rec.CreatedAt, &rec.LastDesiredAt, &unreferenced,
	); err != nil {
		return nil, err
	}
	if unreferenced.Valid {
		v := unreferenced.Int64
		rec.UnreferencedAt = &v
	}
	return &rec, nil
}

func addTrimmed(dst map[string]struct{}, values []string) {
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		dst[value] = struct{}{}
	}
}

func sortedKeys(set map[string]struct{}) []string {
	out := make([]string, 0, len(set))
	for key := range set {
		out = append(out, key)
	}
	slices.Sort(out)
	return out
}

func (d *DB) volumeCleanupPath() string {
	if d == nil || strings.TrimSpace(d.path) == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(d.path), volumeCleanupFileName)
}

// AddPersistentVolumeCleanupUUID records a reserved cleanup bit for later
// explicit orphan prune. It does not delete files or start a sweeper.
func (d *DB) AddPersistentVolumeCleanupUUID(uuid string) error {
	uuid = strings.TrimSpace(uuid)
	if uuid == "" {
		return nil
	}
	current, err := d.ListPersistentVolumeCleanupUUIDs()
	if err != nil {
		return err
	}
	for _, existing := range current {
		if existing == uuid {
			return nil
		}
	}
	current = append(current, uuid)
	slices.Sort(current)
	return d.writePersistentVolumeCleanupUUIDs(current)
}

// ListPersistentVolumeCleanupUUIDs returns UUIDs whose reserved cleanup bit
// may shorten the orphan-prune grace window.
func (d *DB) ListPersistentVolumeCleanupUUIDs() ([]string, error) {
	path := d.volumeCleanupPath()
	if path == "" {
		return nil, nil
	}
	data, err := os.ReadFile(path) // #nosec G304 -- path is derived from the open database directory
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to read volume cleanup list: %w", err)
	}
	var uuids []string
	if err := json.Unmarshal(data, &uuids); err != nil {
		return nil, fmt.Errorf("failed to parse volume cleanup list: %w", err)
	}
	out := make([]string, 0, len(uuids))
	seen := make(map[string]struct{}, len(uuids))
	for _, uuid := range uuids {
		uuid = strings.TrimSpace(uuid)
		if uuid == "" {
			continue
		}
		if _, ok := seen[uuid]; ok {
			continue
		}
		seen[uuid] = struct{}{}
		out = append(out, uuid)
	}
	slices.Sort(out)
	return out, nil
}

func (d *DB) writePersistentVolumeCleanupUUIDs(uuids []string) error {
	path := d.volumeCleanupPath()
	if path == "" {
		return errors.New("database path is not set")
	}
	if uuids == nil {
		uuids = []string{}
	}
	data, err := json.Marshal(uuids)
	if err != nil {
		return fmt.Errorf("failed to encode volume cleanup list: %w", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf("failed to write volume cleanup list: %w", err)
	}
	return nil
}
