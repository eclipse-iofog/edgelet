package modelmanager

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/eclipse-iofog/edgelet/internal/config"
	"github.com/eclipse-iofog/edgelet/internal/modelpull"
	"github.com/eclipse-iofog/edgelet/internal/modelpull/hf"
	"github.com/eclipse-iofog/edgelet/internal/modelpull/oci"
	"github.com/eclipse-iofog/edgelet/internal/models"
	"github.com/eclipse-iofog/edgelet/internal/store"
	"github.com/eclipse-iofog/edgelet/internal/utils/logging"
	"github.com/shirou/gopsutil/v4/disk"
)

const (
	moduleName = "Model Manager"

	// MaxConcurrentPulls is the global cap on in-flight model downloads.
	MaxConcurrentPulls = 2
	// DefaultPullTimeout is the default deadline for one model pull.
	DefaultPullTimeout = 6 * time.Hour

	OpStatusRunning   = "running"
	OpStatusSucceeded = "succeeded"
	OpStatusFailed    = "failed"
)

// Puller downloads and materializes one model artifact.
type Puller interface {
	Pull(ctx context.Context, req modelpull.Request) (*modelpull.Result, error)
}

// PullOperation is the async pull status record (mirrors images:pull where sensible).
type PullOperation struct {
	OperationID     string
	Status          string
	Progress        int
	BytesDownloaded int64
	BytesTotal      int64
	Name            string
	RegistryID      int
	Error           string
	StartedAt       time.Time
	EndedAt         *time.Time
}

// Snapshot returns a JSON-ready view of the operation.
func (op PullOperation) Snapshot() map[string]any {
	response := map[string]any{
		"operationId": op.OperationID,
		"status":      op.Status,
		"progress":    op.Progress,
		"name":        op.Name,
		"startedAt":   op.StartedAt.Format(time.RFC3339Nano),
	}
	if op.BytesDownloaded > 0 || op.BytesTotal > 0 {
		response["bytesDownloaded"] = op.BytesDownloaded
	}
	if op.BytesTotal > 0 {
		response["bytesTotal"] = op.BytesTotal
	}
	if op.RegistryID > 0 {
		response["registryId"] = op.RegistryID
	}
	if op.EndedAt != nil {
		response["endedAt"] = op.EndedAt.Format(time.RFC3339Nano)
	}
	if strings.TrimSpace(op.Error) != "" {
		response["error"] = op.Error
	}
	return response
}

// Manager reconciles local Model desired state and orchestrates artifact pulls.
type Manager struct {
	db            *store.DB
	modelsRoot    string
	oci           Puller
	hf            Puller
	timeout       time.Duration
	now           func() time.Time
	diskMu        sync.RWMutex
	liveCfg       *config.Config
	diskDirectory string
	diskThreshold int64
	diskStats     func(path string) (free, total int64, err error)

	sem chan struct{}

	mu     sync.Mutex
	active map[string]string // metadata.name -> operation id
	ops    map[string]*PullOperation
}

// New constructs a Manager that reads desired models from db and writes under modelsRoot.
func New(db *store.DB, modelsRoot string) *Manager {
	m := &Manager{
		db:            db,
		modelsRoot:    strings.TrimSpace(modelsRoot),
		oci:           &oci.Adapter{},
		hf:            &hf.Adapter{},
		timeout:       DefaultPullTimeout,
		now:           time.Now,
		diskDirectory: filepath.Dir(strings.TrimSpace(modelsRoot)),
		diskThreshold: 20,
		diskStats:     defaultDiskStats,
		sem:           make(chan struct{}, MaxConcurrentPulls),
		active:        make(map[string]string),
		ops:           make(map[string]*PullOperation),
	}
	return m
}

// SetPullers replaces the OCI and Hugging Face adapters (tests).
func (m *Manager) SetPullers(ociPuller, hfPuller Puller) {
	if m == nil {
		return
	}
	m.oci = ociPuller
	m.hf = hfPuller
}

// SetTimeout overrides the default pull deadline.
func (m *Manager) SetTimeout(d time.Duration) {
	if m == nil || d <= 0 {
		return
	}
	m.timeout = d
}

// SetLiveConfig makes free-space checks read diskDirectory and
// availableDiskThreshold from the live Config singleton so CLI PATCH and
// controller config pushes apply without restarting the daemon.
func (m *Manager) SetLiveConfig(cfg *config.Config) {
	if m == nil {
		return
	}
	m.diskMu.Lock()
	m.liveCfg = cfg
	m.diskMu.Unlock()
}

// SetDiskPolicy configures the free-space check used before a payload download.
func (m *Manager) SetDiskPolicy(directory string, thresholdPercent int64, stats func(path string) (free, total int64, err error)) {
	if m == nil {
		return
	}
	m.diskMu.Lock()
	defer m.diskMu.Unlock()
	if strings.TrimSpace(directory) != "" {
		m.diskDirectory = strings.TrimSpace(directory)
	}
	if thresholdPercent >= 0 {
		m.diskThreshold = thresholdPercent
	}
	if stats != nil {
		m.diskStats = stats
	}
}

// Ensure implements modelpull.DiskGuard.
func (m *Manager) Ensure(needed int64) error {
	if m == nil || needed <= 0 {
		return nil
	}
	path, threshold, stats := m.currentDiskPolicy()
	if path == "" {
		path = m.modelsRoot
	}
	if stats == nil {
		return nil
	}
	if err := os.MkdirAll(path, 0o755); err != nil { // #nosec G301 -- model disk root must be traversable for operator inspect and later bind mounts
		return fmt.Errorf("prepare disk directory %s: %w", path, err)
	}
	free, total, err := stats(path)
	if err != nil {
		return fmt.Errorf("measure disk space under %s: %w", path, err)
	}
	return modelpull.EnsureDiskSpace(needed, free, total, threshold, path)
}

func (m *Manager) currentDiskPolicy() (directory string, threshold int64, stats func(path string) (free, total int64, err error)) {
	m.diskMu.RLock()
	directory = strings.TrimSpace(m.diskDirectory)
	threshold = m.diskThreshold
	stats = m.diskStats
	cfg := m.liveCfg
	m.diskMu.RUnlock()
	if cfg == nil {
		return directory, threshold, stats
	}
	if dir := strings.TrimSpace(cfg.DiskDirectory); dir != "" {
		directory = dir
	}
	threshold = cfg.AvailableDiskThreshold
	if threshold < 0 {
		threshold = 0
	}
	return directory, threshold, stats
}

func defaultDiskStats(path string) (free, total int64, err error) {
	usage, err := disk.Usage(path)
	if err != nil {
		return 0, 0, err
	}
	return int64(usage.Free), int64(usage.Total), nil // #nosec G115 -- disk size is below int64 max in practice
}

// ApplyManifest upserts a validated Model document by metadata.name.
func (m *Manager) ApplyManifest(doc *models.LocalModelManifest) (*models.LocalModel, error) {
	if m == nil || m.db == nil {
		return nil, errors.New("model manager is not initialized")
	}
	if doc == nil {
		return nil, errors.New("manifest is nil")
	}
	if err := refuseLocalModelApply(); err != nil {
		return nil, err
	}
	if err := m.rejectLocalApplyForManagedName(strings.TrimSpace(doc.Metadata.Name)); err != nil {
		return nil, err
	}
	reg, err := m.resolveRegistry(doc.Spec.Registry)
	if err != nil {
		return nil, err
	}
	if err := doc.ValidateWithRegistry(reg); err != nil {
		return nil, err
	}
	row := doc.ToLocalModel()
	return m.UpsertDesired(row)
}

func (m *Manager) rejectLocalApplyForManagedName(name string) error {
	if m == nil || m.db == nil || strings.TrimSpace(name) == "" {
		return nil
	}
	if strings.TrimSpace(config.GetInstance().IOFogUUID) == "" {
		return nil
	}
	existing, err := m.db.GetLocalModel(name)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if existing != nil && existing.Source == models.ModelSourceManaged {
		return fmt.Errorf("cannot apply a local Model named %q while a controller-managed model occupies that name", name)
	}
	fleet, err := m.db.LoadControllerModels()
	if err != nil {
		return err
	}
	for _, item := range fleet {
		if item != nil && item.Name == name {
			return fmt.Errorf("cannot apply a local Model named %q while a controller-managed model occupies that name", name)
		}
	}
	return nil
}

// UpsertDesired inserts or updates a local Model row, bumping generation on spec drift.
func (m *Manager) UpsertDesired(incoming *models.LocalModel) (*models.LocalModel, error) {
	if m == nil || m.db == nil {
		return nil, errors.New("model manager is not initialized")
	}
	if incoming == nil {
		return nil, errors.New("model is nil")
	}
	incoming.NormalizeDefaults()
	if strings.TrimSpace(incoming.Name) == "" {
		return nil, errors.New("model name is required")
	}

	existing, err := m.db.GetLocalModel(incoming.Name)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	nowSec := m.now().Unix()
	if existing == nil {
		incoming.Generation = 1
		incoming.ObservedGeneration = 0
		incoming.State = models.ModelStatePending
		incoming.LastError = ""
		incoming.LastTransitionAt = nowSec
	} else if specFingerprint(existing) != specFingerprint(incoming) {
		incoming.Generation = existing.Generation + 1
		incoming.ObservedGeneration = 0
		incoming.State = models.ModelStatePending
		incoming.LastError = ""
		incoming.LastTransitionAt = nowSec
		incoming.ResolvedRevision = ""
		incoming.Digest = ""
		incoming.PulledAt = 0
	} else {
		incoming.Generation = existing.Generation
		incoming.ObservedGeneration = existing.ObservedGeneration
		incoming.State = existing.State
		incoming.LastError = existing.LastError
		incoming.LastTransitionAt = existing.LastTransitionAt
		incoming.ResolvedRevision = existing.ResolvedRevision
		incoming.Digest = existing.Digest
		incoming.RevisionFloating = existing.RevisionFloating
		incoming.TotalBytes = existing.TotalBytes
		incoming.PulledAt = existing.PulledAt
		incoming.ContentPath = existing.ContentPath
		incoming.ManifestPath = existing.ManifestPath
	}
	incoming.LastReconcileAt = existingReconcile(existing)
	if err := m.db.UpsertLocalModel(incoming); err != nil {
		return nil, err
	}
	return m.db.GetLocalModel(incoming.Name)
}

func existingReconcile(existing *models.LocalModel) int64 {
	if existing == nil {
		return 0
	}
	return existing.LastReconcileAt
}

// LoadDesired returns local Model rows plus any persisted controller Model rows.
func (m *Manager) LoadDesired() ([]*models.LocalModel, []*models.ControllerModel, error) {
	if m == nil || m.db == nil {
		return nil, nil, errors.New("model manager is not initialized")
	}
	local, err := m.db.ListLocalModels()
	if err != nil {
		return nil, nil, err
	}
	controller, err := m.db.LoadControllerModels()
	if err != nil {
		return nil, nil, err
	}
	return local, controller, nil
}

// SaveControllerModels persists fleet-desired Model rows without starting a sync worker.
func (m *Manager) SaveControllerModels(items []*models.ControllerModel) error {
	if m == nil || m.db == nil {
		return errors.New("model manager is not initialized")
	}
	return m.db.SaveControllerModels(items)
}

// ApplyControllerModels replaces the fleet-desired Model snapshot and upserts
// each row as a managed local Model. A managed Model occupies that on-disk name.
func (m *Manager) ApplyControllerModels(ctx context.Context, items []*models.ControllerModel) error {
	if m == nil || m.db == nil {
		return errors.New("model manager is not initialized")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := m.SaveControllerModels(items); err != nil {
		return err
	}
	for _, item := range items {
		if item == nil {
			continue
		}
		row := item.ToLocalModel()
		if row == nil || strings.TrimSpace(row.Name) == "" {
			continue
		}
		if _, err := m.UpsertDesired(row); err != nil {
			logging.LogWarn(moduleName, fmt.Sprintf("managed model %s upsert failed: %v", row.Name, err))
		}
	}
	return m.Reconcile(ctx)
}

// Reconcile compares desired local Models to on-disk state and starts pulls when needed.
func (m *Manager) Reconcile(ctx context.Context) error {
	if m == nil || m.db == nil {
		return errors.New("model manager is not initialized")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	local, _, err := m.LoadDesired()
	if err != nil {
		return err
	}
	for _, row := range local {
		if row == nil {
			continue
		}
		if err := refuseLocalSourcePull(row.Source); err != nil {
			continue
		}
		needed, needErr := m.needsPull(row)
		if needErr != nil {
			logging.LogWarn(moduleName, fmt.Sprintf("model %s reconcile check failed: %v", row.Name, needErr))
			continue
		}
		if !needed {
			if err := m.touchReconcile(row.Name); err != nil {
				return err
			}
			continue
		}
		if _, err := m.StartPull(row.Name); err != nil {
			logging.LogWarn(moduleName, fmt.Sprintf("model %s pull start failed: %v", row.Name, err))
		}
	}
	return nil
}

func (m *Manager) needsPull(row *models.LocalModel) (bool, error) {
	if row == nil {
		return false, nil
	}
	if m.hasActivePull(row.Name) {
		return false, nil
	}
	if row.Generation > row.ObservedGeneration {
		if skip, err := m.canSkipPull(row); err != nil {
			return false, err
		} else if skip {
			return false, m.markObservedReady(row, nil)
		}
		return true, nil
	}
	switch row.State {
	case models.ModelStatePending:
		return true, nil
	case models.ModelStateReady:
		skip, err := m.canSkipPull(row)
		if err != nil {
			return false, err
		}
		return !skip, nil
	default:
		return false, nil
	}
}

func (m *Manager) canSkipPull(row *models.LocalModel) (bool, error) {
	reg, err := m.resolveRegistry(row.RegistryID)
	if err != nil {
		return false, err
	}
	desired := identityForDesired(reg, row)
	onDisk, readErr := modelpull.ReadOnDiskManifest(modelpull.ManifestPath(m.modelsRoot, row.Name))
	if readErr != nil {
		return false, nil
	}
	if !identityMatchesManifest(desired, onDisk, reg, row) {
		return false, nil
	}
	info, statErr := os.Stat(modelpull.ContentDir(m.modelsRoot, row.Name))
	if statErr != nil || !info.IsDir() {
		return false, nil
	}
	return true, nil
}

func (m *Manager) hasActivePull(name string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.active[strings.TrimSpace(name)]
	return ok
}

func (m *Manager) resolveRegistry(id int) (*models.Registry, error) {
	if m.db == nil {
		return nil, errors.New("store is not initialized")
	}
	if id <= 0 {
		return nil, errors.New("spec.registry is required")
	}
	reg, err := m.db.LookupRegistry(id)
	if err != nil || reg == nil {
		return nil, fmt.Errorf("registry %d not found", id)
	}
	reg.NormalizeDefaults()
	if !models.ValidRegistryType(reg.Type) {
		return nil, fmt.Errorf("registry %d has unsupported type %q", reg.ID, reg.Type)
	}
	return reg, nil
}

func (m *Manager) touchReconcile(name string) error {
	row, err := m.db.GetLocalModel(name)
	if err != nil {
		return err
	}
	row.LastReconcileAt = m.now().Unix()
	return m.db.UpsertLocalModel(row)
}

func (m *Manager) markObservedReady(row *models.LocalModel, result *modelpull.Result) error {
	current, err := m.db.GetLocalModel(row.Name)
	if err != nil {
		return err
	}
	nowSec := m.now().Unix()
	prevState := current.State
	if current.State != models.ModelStateReady {
		current.LastTransitionAt = nowSec
	}
	current.State = models.ModelStateReady
	current.LastError = ""
	current.ObservedGeneration = current.Generation
	current.LastReconcileAt = nowSec
	if result != nil {
		current.ResolvedRevision = result.ResolvedRevision
		current.Digest = result.Digest
		current.RevisionFloating = result.RevisionFloating
		current.TotalBytes = result.TotalBytes
		current.ManifestPath = result.ManifestPath
		current.ContentPath = result.ContentPath
		current.PulledAt = nowSec
		if strings.TrimSpace(result.Format) != "" {
			current.Format = result.Format
		}
	} else {
		current.ManifestPath = modelpull.ManifestPath(m.modelsRoot, current.Name)
		current.ContentPath = modelpull.ContentDir(m.modelsRoot, current.Name)
	}
	if err := m.db.UpsertLocalModel(current); err != nil {
		return err
	}
	// Drop the in-flight name before waking reconcile. A slow catalog scan
	// must not leave a finished pull looking active.
	m.releaseName(current.Name)
	noteCatalogTransition(current.Name, current.Source, prevState, current.State)
	return nil
}
