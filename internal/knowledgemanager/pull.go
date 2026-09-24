package knowledgemanager

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/eclipse-iofog/edgelet/internal/catalogwake"
	"github.com/eclipse-iofog/edgelet/internal/modelpull"
	"github.com/eclipse-iofog/edgelet/internal/models"
	"github.com/eclipse-iofog/edgelet/internal/utils/logging"
	"github.com/google/uuid"
)

// StartPull begins an async pull for one Knowledge name. One in-flight pull per name;
// a second request returns the existing operation.
//
// The caller context is not used after accept. Download runs on a detached
// background context so HTTP handlers can return 202 without canceling work.
func (m *Manager) StartPull(name string) (*PullOperation, error) {
	if m == nil || m.db == nil {
		return nil, errors.New("knowledge manager is not initialized")
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, errors.New("knowledge name is required")
	}

	row, err := m.db.GetLocalKnowledge(name)
	if err != nil {
		return nil, fmt.Errorf("knowledge %s not found", name)
	}
	if err := refuseLocalSourcePull(row.Source); err != nil {
		return nil, err
	}

	m.mu.Lock()
	if opID, ok := m.active[name]; ok {
		op := copyOp(m.ops[opID])
		m.mu.Unlock()
		return op, nil
	}
	op := &PullOperation{
		OperationID: uuid.NewString(),
		Status:      OpStatusRunning,
		Progress:    0,
		Name:        name,
		RegistryID:  row.RegistryID,
		StartedAt:   m.now().UTC(),
	}
	m.active[name] = op.OperationID
	m.ops[op.OperationID] = op
	started := copyOp(op)
	m.mu.Unlock()

	logging.LogInfo(moduleName, fmt.Sprintf("knowledge pull started name=%s operationId=%s generation=%d", name, started.OperationID, row.Generation))
	go m.runPull(context.Background(), started.OperationID, name)
	return started, nil
}

// GetPull returns a copy of one async pull operation.
func (m *Manager) GetPull(operationID string) (*PullOperation, bool) {
	if m == nil {
		return nil, false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	op, ok := m.ops[strings.TrimSpace(operationID)]
	if !ok {
		return nil, false
	}
	return copyOp(op), true
}

func (m *Manager) runPull(parent context.Context, operationID, name string) {
	defer m.releaseName(name)

	select {
	case m.sem <- struct{}{}:
		defer func() { <-m.sem }()
	case <-parent.Done():
		m.failOp(operationID, parent.Err())
		_ = m.markFailed(name, parent.Err())
		return
	}

	ctx, cancel := context.WithTimeout(parent, m.timeout)
	defer cancel()

	if err := m.doPull(ctx, operationID, name); err != nil {
		m.failOp(operationID, err)
		_ = m.markFailed(name, err)
		logging.LogWarn(moduleName, fmt.Sprintf("knowledge pull failed name=%s operationId=%s err=%v", name, operationID, err))
		return
	}
	m.succeedOp(operationID)
	logging.LogInfo(moduleName, fmt.Sprintf("knowledge pull succeeded name=%s operationId=%s", name, operationID))
}

func (m *Manager) doPull(ctx context.Context, operationID, name string) error {
	row, err := m.db.GetLocalKnowledge(name)
	if err != nil {
		return fmt.Errorf("knowledge %s not found", name)
	}
	if err := m.setState(row.Name, models.KnowledgeStatePulling, ""); err != nil {
		return err
	}
	m.setProgress(operationID, 0)

	reg, err := m.resolveRegistry(row.RegistryID)
	if err != nil {
		return err
	}

	puller, err := m.pullerFor(reg)
	if err != nil {
		return err
	}

	desiredKey := identityForDesired(reg, row)
	if skip, skipErr := m.canSkipPull(row); skipErr != nil {
		return skipErr
	} else if skip {
		if err := m.markReady(row, nil); err != nil {
			return err
		}
		m.setProgress(operationID, 100)
		return nil
	}

	_, _ = modelpull.CleanupStaleIncomplete(m.knowledgeRoot, modelpull.StaleIncompleteAge, m.now())

	result, err := puller.Pull(ctx, modelpull.Request{
		Name:       row.Name,
		Repo:       row.Repo,
		Revision:   row.Revision,
		Registry:   reg,
		Files:      row.Files(),
		ModelsRoot: m.knowledgeRoot,
		FormatHint: row.Format,
		Disk:       m,
		OnProgress: func(downloaded, total int64) {
			m.setByteProgress(operationID, downloaded, total)
		},
	})
	if err != nil {
		return err
	}
	if result == nil {
		return errors.New("knowledge pull returned no result")
	}
	if result.RevisionFloating {
		rev := strings.TrimSpace(row.Revision)
		if rev == "" {
			if reg.NormalizedType() == models.RegistryTypeHF {
				rev = "main"
			} else {
				rev = "latest"
			}
		}
		logging.LogWarn(moduleName, fmt.Sprintf("knowledge %s uses a floating revision %q; pin a commit or digest for reproducible pulls", name, rev))
	}
	if err := recordIdentity(result.ManifestPath, desiredKey); err != nil {
		return err
	}
	if err := m.markReady(row, result); err != nil {
		return err
	}
	m.setProgress(operationID, 100)
	return nil
}

func (m *Manager) pullerFor(reg *models.Registry) (Puller, error) {
	if reg == nil {
		return nil, errors.New("registry is required")
	}
	switch reg.NormalizedType() {
	case models.RegistryTypeOCI:
		if err := models.RequireRegistryType(reg, models.RegistryTypeOCI); err != nil {
			return nil, err
		}
		if m.oci == nil {
			return nil, errors.New("oci knowledge pull adapter is not configured")
		}
		return m.oci, nil
	case models.RegistryTypeHF:
		if err := models.RequireRegistryType(reg, models.RegistryTypeHF); err != nil {
			return nil, err
		}
		if m.hf == nil {
			return nil, errors.New("huggingface knowledge pull adapter is not configured")
		}
		return m.hf, nil
	default:
		return nil, fmt.Errorf("registry %d has type %q; knowledge pull requires type oci or hf", reg.ID, reg.NormalizedType())
	}
}

func recordIdentity(manifestPath, identityKey string) error {
	if strings.TrimSpace(manifestPath) == "" || strings.TrimSpace(identityKey) == "" {
		return nil
	}
	onDisk, err := modelpull.ReadOnDiskManifest(manifestPath)
	if err != nil {
		return err
	}
	onDisk.IdentityKey = identityKey
	return modelpull.WriteOnDiskManifest(manifestPath, onDisk)
}

func (m *Manager) setState(name, state, lastError string) error {
	row, err := m.db.GetLocalKnowledge(name)
	if err != nil {
		return err
	}
	row.State = state
	row.LastError = lastError
	return m.db.UpsertLocalKnowledge(row)
}

func (m *Manager) markFailed(name string, pullErr error) error {
	row, err := m.db.GetLocalKnowledge(name)
	if err != nil {
		return err
	}
	prevState := row.State
	row.State = models.KnowledgeStateFailed
	if pullErr != nil {
		row.LastError = pullErr.Error()
	}
	if err := m.db.UpsertLocalKnowledge(row); err != nil {
		return err
	}
	m.releaseName(row.Name)
	noteCatalogTransition(row.Name, row.Source, prevState, row.State)
	return nil
}

func noteCatalogTransition(name, source, prev, next string) {
	if prev == next {
		return
	}
	if next != models.KnowledgeStateReady && next != models.KnowledgeStateFailed {
		return
	}
	catalogwake.Notify(name, source)
}

func (m *Manager) releaseName(name string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.active, name)
}

func (m *Manager) setProgress(operationID string, progress int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.applyProgressLocked(operationID, progress, -1, -1)
}

func (m *Manager) setByteProgress(operationID string, downloaded, total int64) {
	progress := 0
	if total > 0 {
		progress = int(downloaded * 100 / total)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.applyProgressLocked(operationID, progress, downloaded, total)
}

func (m *Manager) applyProgressLocked(operationID string, progress int, downloaded, total int64) {
	if progress < 0 {
		progress = 0
	}
	if progress > 100 {
		progress = 100
	}
	op, ok := m.ops[operationID]
	if !ok {
		return
	}
	if progress > op.Progress {
		op.Progress = progress
	}
	if downloaded >= 0 && downloaded > op.BytesDownloaded {
		op.BytesDownloaded = downloaded
	}
	if total >= 0 && total > op.BytesTotal {
		op.BytesTotal = total
	}
}

func (m *Manager) succeedOp(operationID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	op, ok := m.ops[operationID]
	if !ok {
		return
	}
	now := m.now().UTC()
	op.EndedAt = &now
	op.Progress = 100
	op.Status = OpStatusSucceeded
}

func (m *Manager) failOp(operationID string, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	op, ok := m.ops[operationID]
	if !ok {
		return
	}
	now := m.now().UTC()
	op.EndedAt = &now
	op.Status = OpStatusFailed
	if err != nil {
		op.Error = err.Error()
	}
}

func copyOp(op *PullOperation) *PullOperation {
	if op == nil {
		return nil
	}
	cp := *op
	if op.EndedAt != nil {
		ended := *op.EndedAt
		cp.EndedAt = &ended
	}
	return &cp
}
