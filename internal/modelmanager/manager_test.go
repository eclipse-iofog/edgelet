package modelmanager

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/eclipse-iofog/edgelet/internal/config"
	"github.com/eclipse-iofog/edgelet/internal/modelpull"
	"github.com/eclipse-iofog/edgelet/internal/modelpull/ocistore"
	"github.com/eclipse-iofog/edgelet/internal/models"
	"github.com/eclipse-iofog/edgelet/internal/store"
)

type stubPuller struct {
	mu       sync.Mutex
	calls    int
	lastType string
	block    chan struct{}
	inflight int32
	maxIn    int32
	err      error
	estimate int64
	failOnce bool
	writeOCI bool
	blob     []byte
	digest   string
	blobs    []string
}

func (s *stubPuller) Pull(ctx context.Context, req modelpull.Request) (*modelpull.Result, error) {
	atomic.AddInt32(&s.inflight, 1)
	cur := atomic.LoadInt32(&s.inflight)
	for {
		peak := atomic.LoadInt32(&s.maxIn)
		if cur <= peak {
			break
		}
		if atomic.CompareAndSwapInt32(&s.maxIn, peak, cur) {
			break
		}
	}
	defer atomic.AddInt32(&s.inflight, -1)

	s.mu.Lock()
	s.calls++
	if req.Registry != nil {
		s.lastType = req.Registry.NormalizedType()
	}
	block := s.block
	fail := s.err
	if s.failOnce {
		fail = context.Canceled
		s.failOnce = false
	}
	s.mu.Unlock()

	if block != nil {
		select {
		case <-block:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if s.estimate > 0 && req.Disk != nil {
		if err := req.Disk.Ensure(s.estimate); err != nil {
			return nil, err
		}
	}
	req.ReportProgress(s.estimate/2, s.estimate)
	if fail != nil {
		return nil, fail
	}

	content := modelpull.ContentDir(req.ModelsRoot, req.Name)
	if err := os.MkdirAll(content, 0o755); err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(content, "weights.bin"), []byte("ok"), 0o644); err != nil {
		return nil, err
	}
	manifestPath := modelpull.ManifestPath(req.ModelsRoot, req.Name)
	onDisk := modelpull.OnDiskManifest{
		MetadataName:      req.Name,
		RegistryID:        req.Registry.ID,
		RegistryType:      req.Registry.NormalizedType(),
		Repo:              req.Repo,
		RequestedRevision: req.Revision,
		ResolvedRevision:  "resolved-rev",
		Digest:            "sha256:abc",
		Files:             req.Files,
		ContentPaths:      []string{"content/weights.bin"},
		Blobs:             s.blobs,
		TotalBytes:        2,
	}
	if s.writeOCI && len(s.blob) > 0 && s.digest != "" {
		store, err := ocistore.Open(modelpull.OCIStoreDir(req.ModelsRoot))
		if err != nil {
			return nil, err
		}
		if err := store.WriteBlob(s.digest, bytes.NewReader(s.blob), int64(len(s.blob))); err != nil {
			return nil, err
		}
		if err := store.WriteManifest(s.digest, []byte(`{"schemaVersion":2}`), []string{req.Repo + ":latest"}, []string{s.digest}); err != nil {
			return nil, err
		}
		if err := store.Track(req.Name, s.digest, []string{s.digest}); err != nil {
			return nil, err
		}
		onDisk.Digest = s.digest
		onDisk.Blobs = []string{s.digest}
	}
	if err := modelpull.WriteOnDiskManifest(manifestPath, onDisk); err != nil {
		return nil, err
	}
	digest := onDisk.Digest
	return &modelpull.Result{
		Digest:           digest,
		ResolvedRevision: "resolved-rev",
		TotalBytes:       2,
		ManifestPath:     manifestPath,
		ContentPath:      content,
		ContentPaths:     []string{"content/weights.bin"},
		Format:           req.FormatHint,
		Blobs:            onDisk.Blobs,
	}, nil
}

func (s *stubPuller) callCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

func newTestManager(t *testing.T) (*Manager, *stubPuller, *stubPuller) {
	t.Helper()
	db := store.GetInstance()
	if err := db.Open(t.TempDir()); err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	ociReg := models.NewRegistryBuilder().SetID(1).SetURL("quay.io").SetType(models.RegistryTypeOCI).Build()
	hfReg := models.NewRegistryBuilder().SetID(5).SetURL("https://huggingface.co").SetType(models.RegistryTypeHF).Build()
	if err := db.UpsertLocalRegistry(ociReg); err != nil {
		t.Fatalf("upsert oci registry: %v", err)
	}
	if err := db.UpsertLocalRegistry(hfReg); err != nil {
		t.Fatalf("upsert hf registry: %v", err)
	}

	ociPuller := &stubPuller{}
	hfPuller := &stubPuller{}
	m := New(db, t.TempDir())
	m.SetPullers(ociPuller, hfPuller)
	m.SetTimeout(5 * time.Second)
	return m, ociPuller, hfPuller
}

func sampleModel(name, repo, revision string, registryID int, files []string) *models.LocalModel {
	row := &models.LocalModel{
		Name:       name,
		Repo:       repo,
		Revision:   revision,
		RegistryID: registryID,
		Format:     models.ModelFormatGGUF,
	}
	row.SetFiles(files)
	row.NormalizeDefaults()
	return row
}

func waitOp(t *testing.T, m *Manager, opID string) *PullOperation {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		op, ok := m.GetPull(opID)
		if ok && (op.Status == OpStatusSucceeded || op.Status == OpStatusFailed) {
			return op
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for pull operation %s", opID)
	return nil
}

func waitState(t *testing.T, m *Manager, name, want string) *models.LocalModel {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		row, err := m.db.GetLocalModel(name)
		if err == nil && row.State == want {
			return row
		}
		time.Sleep(10 * time.Millisecond)
	}
	row, _ := m.db.GetLocalModel(name)
	t.Fatalf("timed out waiting for model %s state %s (have %+v)", name, want, row)
	return nil
}

func TestUpsertGenerationBumpTriggersRepull(t *testing.T) {
	m, ociPuller, _ := newTestManager(t)
	row := sampleModel("gemma3", "ai/gemma3", "4b-q8_0", 1, nil)
	if _, err := m.UpsertDesired(row); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if err := m.Reconcile(context.Background()); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	ops := collectOps(m)
	if len(ops) != 1 {
		t.Fatalf("expected 1 pull operation, got %d", len(ops))
	}
	waitOp(t, m, ops[0])
	ready := waitState(t, m, "gemma3", models.ModelStateReady)
	if ready.Generation != 1 || ready.ObservedGeneration != 1 {
		t.Fatalf("expected observed generation 1, got gen=%d obs=%d", ready.Generation, ready.ObservedGeneration)
	}
	if ociPuller.callCount() != 1 {
		t.Fatalf("expected 1 pull, got %d", ociPuller.callCount())
	}

	update := sampleModel("gemma3", "ai/gemma3", "4b-q4_0", 1, nil)
	updated, err := m.UpsertDesired(update)
	if err != nil {
		t.Fatalf("upsert revision change: %v", err)
	}
	if updated.Generation != 2 || updated.State != models.ModelStatePending {
		t.Fatalf("expected generation bump to Pending, got gen=%d state=%s", updated.Generation, updated.State)
	}
	if err := m.Reconcile(context.Background()); err != nil {
		t.Fatalf("reconcile after bump: %v", err)
	}
	waitState(t, m, "gemma3", models.ModelStateReady)
	if ociPuller.callCount() != 2 {
		t.Fatalf("expected re-pull after generation bump, got %d pulls", ociPuller.callCount())
	}
	final, err := m.db.GetLocalModel("gemma3")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if final.Generation != 2 || final.ObservedGeneration != 2 || final.Revision != "4b-q4_0" {
		t.Fatalf("unexpected final row: %+v", final)
	}
}

func TestIdentityKeyIdempotentSkipWhenManifestMatches(t *testing.T) {
	m, ociPuller, _ := newTestManager(t)
	row := sampleModel("gemma3", "ai/gemma3", "4b-q8_0", 1, nil)
	if _, err := m.UpsertDesired(row); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	op, err := m.StartPull("gemma3")
	if err != nil {
		t.Fatalf("start pull: %v", err)
	}
	if waitOp(t, m, op.OperationID).Status != OpStatusSucceeded {
		t.Fatalf("first pull failed: %+v", op)
	}
	if ociPuller.callCount() != 1 {
		t.Fatalf("expected 1 pull, got %d", ociPuller.callCount())
	}

	retry, err := m.StartPull("gemma3")
	if err != nil {
		t.Fatalf("start pull again: %v", err)
	}
	got := waitOp(t, m, retry.OperationID)
	if got.Status != OpStatusSucceeded {
		t.Fatalf("idempotent pull failed: %+v", got)
	}
	if ociPuller.callCount() != 1 {
		t.Fatalf("expected identity skip (still 1 pull), got %d", ociPuller.callCount())
	}

	same := sampleModel("gemma3", "ai/gemma3", "4b-q8_0", 1, nil)
	same.Format = models.ModelFormatUnknown
	bumped, err := m.UpsertDesired(same)
	if err != nil {
		t.Fatalf("format-only upsert: %v", err)
	}
	if bumped.Generation != 2 {
		t.Fatalf("expected format change to bump generation, got %d", bumped.Generation)
	}
	if err := m.Reconcile(context.Background()); err != nil {
		t.Fatalf("reconcile format-only: %v", err)
	}
	waitState(t, m, "gemma3", models.ModelStateReady)
	if ociPuller.callCount() != 1 {
		t.Fatalf("expected identity skip after format-only generation bump, got %d pulls", ociPuller.callCount())
	}
}

func TestStateTransitionsReadyAndFailed(t *testing.T) {
	m, ociPuller, _ := newTestManager(t)
	ociPuller.block = make(chan struct{})
	row := sampleModel("gemma3", "ai/gemma3", "latest", 1, nil)
	if _, err := m.UpsertDesired(row); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	stored, err := m.db.GetLocalModel("gemma3")
	if err != nil || stored.State != models.ModelStatePending {
		t.Fatalf("expected Pending after upsert, got %+v err=%v", stored, err)
	}

	op, err := m.StartPull("gemma3")
	if err != nil {
		t.Fatalf("start pull: %v", err)
	}
	waitState(t, m, "gemma3", models.ModelStatePulling)
	close(ociPuller.block)
	if waitOp(t, m, op.OperationID).Status != OpStatusSucceeded {
		t.Fatal("expected succeeded operation")
	}
	ready := waitState(t, m, "gemma3", models.ModelStateReady)
	if ready.ObservedGeneration != ready.Generation {
		t.Fatalf("expected observed generation to match, got %+v", ready)
	}

	failing := &stubPuller{err: context.Canceled}
	m.SetPullers(failing, &stubPuller{})
	update := sampleModel("gemma3", "ai/gemma3", "other", 1, nil)
	if _, err := m.UpsertDesired(update); err != nil {
		t.Fatalf("upsert fail path: %v", err)
	}
	failOp, err := m.StartPull("gemma3")
	if err != nil {
		t.Fatalf("start failing pull: %v", err)
	}
	if waitOp(t, m, failOp.OperationID).Status != OpStatusFailed {
		t.Fatal("expected failed operation")
	}
	failed := waitState(t, m, "gemma3", models.ModelStateFailed)
	if failed.ObservedGeneration != failed.Generation || failed.LastError == "" {
		t.Fatalf("expected failed row with observed generation, got %+v", failed)
	}
	if err := m.Reconcile(context.Background()); err != nil {
		t.Fatalf("reconcile after fail: %v", err)
	}
	if failing.callCount() != 1 {
		t.Fatalf("failed generation should not auto-retry, got %d pulls", failing.callCount())
	}
}

func TestStartPull_DispatchesByRegistryType(t *testing.T) {
	m, ociPuller, hfPuller := newTestManager(t)
	ociRow := sampleModel("gemma3", "ai/gemma3", "4b-q8_0", 1, nil)
	if _, err := m.UpsertDesired(ociRow); err != nil {
		t.Fatalf("upsert oci model: %v", err)
	}
	hfRow := sampleModel("llama", "second-state/Llama-2-7B-Chat-GGUF", "main", 5, []string{"model.gguf"})
	if _, err := m.UpsertDesired(hfRow); err != nil {
		t.Fatalf("upsert hf model: %v", err)
	}

	ociOp, err := m.StartPull("gemma3")
	if err != nil {
		t.Fatalf("oci pull: %v", err)
	}
	hfOp, err := m.StartPull("llama")
	if err != nil {
		t.Fatalf("hf pull: %v", err)
	}
	if waitOp(t, m, ociOp.OperationID).Status != OpStatusSucceeded {
		t.Fatal("oci pull failed")
	}
	if waitOp(t, m, hfOp.OperationID).Status != OpStatusSucceeded {
		t.Fatal("hf pull failed")
	}
	if ociPuller.callCount() != 1 || hfPuller.callCount() != 1 {
		t.Fatalf("expected one call per adapter, oci=%d hf=%d", ociPuller.callCount(), hfPuller.callCount())
	}
	if ociPuller.lastType != models.RegistryTypeOCI || hfPuller.lastType != models.RegistryTypeHF {
		t.Fatalf("adapter type dispatch oci=%q hf=%q", ociPuller.lastType, hfPuller.lastType)
	}
}

func TestStartPull_DetachesFromCaller(t *testing.T) {
	m, _, _ := newTestManager(t)
	row := sampleModel("gemma3", "ai/gemma3", "4b-q8_0", 1, nil)
	if _, err := m.UpsertDesired(row); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	op, err := m.StartPull("gemma3")
	if err != nil {
		t.Fatalf("start pull: %v", err)
	}
	got := waitOp(t, m, op.OperationID)
	if got.Status != OpStatusSucceeded {
		t.Fatalf("detached pull must succeed: %+v", got)
	}
}

func TestStartPull_SerializesOnePerNameAndMaxTwoGlobal(t *testing.T) {
	m, ociPuller, _ := newTestManager(t)
	ociPuller.block = make(chan struct{})
	for _, name := range []string{"model-a", "model-b", "model-c"} {
		if _, err := m.UpsertDesired(sampleModel(name, "ai/"+name, "latest", 1, nil)); err != nil {
			t.Fatalf("upsert %s: %v", name, err)
		}
	}
	opA, err := m.StartPull("model-a")
	if err != nil {
		t.Fatalf("start a: %v", err)
	}
	again, err := m.StartPull("model-a")
	if err != nil {
		t.Fatalf("start a again: %v", err)
	}
	if again.OperationID != opA.OperationID {
		t.Fatalf("expected same operation for in-flight name, got %s vs %s", again.OperationID, opA.OperationID)
	}
	if _, err := m.StartPull("model-b"); err != nil {
		t.Fatalf("start b: %v", err)
	}
	if _, err := m.StartPull("model-c"); err != nil {
		t.Fatalf("start c: %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if atomic.LoadInt32(&ociPuller.inflight) == 2 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if got := atomic.LoadInt32(&ociPuller.inflight); got != 2 {
		t.Fatalf("expected 2 in-flight pulls, got %d", got)
	}
	if peak := atomic.LoadInt32(&ociPuller.maxIn); peak > 2 {
		t.Fatalf("global cap exceeded: max in-flight %d", peak)
	}
	close(ociPuller.block)
	waitState(t, m, "model-a", models.ModelStateReady)
	waitState(t, m, "model-b", models.ModelStateReady)
	waitState(t, m, "model-c", models.ModelStateReady)
}

func TestStartPull_RegistryTypeMismatchUsesMatchingAdapter(t *testing.T) {
	m, ociPuller, hfPuller := newTestManager(t)
	// Hugging Face repo path against an oci registry must use the oci adapter,
	// not the Hub client — pairing the wrong adapter is a type mismatch.
	row := sampleModel("llama", "second-state/Llama-2-7B-Chat-GGUF", "main", 1, []string{"model.gguf"})
	if _, err := m.UpsertDesired(row); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	op, err := m.StartPull("llama")
	if err != nil {
		t.Fatalf("start pull: %v", err)
	}
	if waitOp(t, m, op.OperationID).Status != OpStatusSucceeded {
		t.Fatal("expected oci adapter to handle oci registry")
	}
	if ociPuller.callCount() != 1 || hfPuller.callCount() != 0 {
		t.Fatalf("oci registry must not call huggingface adapter, oci=%d hf=%d", ociPuller.callCount(), hfPuller.callCount())
	}

	if err := models.RequireRegistryType(
		models.NewRegistryBuilder().SetID(1).SetURL("quay.io").SetType(models.RegistryTypeOCI).Build(),
		models.RegistryTypeHF,
	); err == nil || !strings.Contains(err.Error(), "hf") {
		t.Fatalf("expected explicit hf-vs-oci mismatch error, got: %v", err)
	}
}

func TestSaveControllerModelsStub(t *testing.T) {
	m, _, _ := newTestManager(t)
	item := &models.ControllerModel{
		UUID:       "3f2c8a1e-2b64-4c0d-9f11-0a1b2c3d4e5f",
		Name:       "llama-2-7b-q2k",
		Repo:       "second-state/Llama-2-7B-Chat-GGUF",
		Revision:   "main",
		RegistryID: 5,
	}
	item.SetFiles([]string{"model.gguf"})
	if err := m.SaveControllerModels([]*models.ControllerModel{item}); err != nil {
		t.Fatalf("save controller models: %v", err)
	}
	local, controller, err := m.LoadDesired()
	if err != nil {
		t.Fatalf("load desired: %v", err)
	}
	if len(local) != 0 {
		t.Fatalf("stub must not create local rows, got %d", len(local))
	}
	if len(controller) != 1 || controller[0].Name != item.Name {
		t.Fatalf("expected persisted controller model, got %+v", controller)
	}
	if err := m.Reconcile(context.Background()); err != nil {
		t.Fatalf("reconcile stub rows: %v", err)
	}
}

func TestIdentityKeyStableAcrossFileOrder(t *testing.T) {
	a := IdentityKey(5, models.RegistryTypeHF, "org/repo", "main", []string{"b.gguf", "a.gguf"})
	b := IdentityKey(5, "HF", "org/repo", "main", []string{"a.gguf", "b.gguf"})
	if a != b {
		t.Fatalf("identity key should be order-independent: %s vs %s", a, b)
	}
	c := IdentityKey(5, models.RegistryTypeHF, "org/repo", "main", []string{"a.gguf"})
	if a == c {
		t.Fatal("different files must produce a different identity key")
	}
}

func TestPullOperationSnapshotShape(t *testing.T) {
	ended := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	op := PullOperation{
		OperationID:     "op-1",
		Status:          OpStatusSucceeded,
		Progress:        100,
		BytesDownloaded: 50,
		BytesTotal:      100,
		Name:            "gemma3",
		RegistryID:      1,
		StartedAt:       ended.Add(-time.Minute),
		EndedAt:         &ended,
	}
	snap := op.Snapshot()
	for _, key := range []string{"operationId", "status", "progress", "bytesDownloaded", "bytesTotal", "name", "registryId", "startedAt", "endedAt"} {
		if _, ok := snap[key]; !ok {
			t.Fatalf("missing %s in snapshot: %#v", key, snap)
		}
	}
}

func TestDiskCheck_RejectsWhenOverThreshold(t *testing.T) {
	m, ociPuller, _ := newTestManager(t)
	ociPuller.estimate = 50
	m.SetDiskPolicy(t.TempDir(), 20, func(string) (int64, int64, error) {
		return 100, 1000, nil
	})
	if _, err := m.UpsertDesired(sampleModel("gemma3", "ai/gemma3", "latest", 1, nil)); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	op, err := m.StartPull("gemma3")
	if err != nil {
		t.Fatalf("start pull: %v", err)
	}
	got := waitOp(t, m, op.OperationID)
	if got.Status != OpStatusFailed || !strings.Contains(got.Error, "available-disk threshold") {
		t.Fatalf("expected disk threshold failure, got %+v", got)
	}
	if ociPuller.callCount() != 1 {
		t.Fatalf("puller should run estimate then fail, calls=%d", ociPuller.callCount())
	}
	if _, err := os.Stat(modelpull.ManifestPath(m.modelsRoot, "gemma3")); !os.IsNotExist(err) {
		t.Fatal("failed disk check must not write manifest.json")
	}
}

func TestDiskCheck_UsesLiveAvailableDiskThreshold(t *testing.T) {
	m, _, _ := newTestManager(t)
	dir := t.TempDir()
	m.SetDiskPolicy(dir, 20, func(string) (int64, int64, error) {
		return 100, 1000, nil
	})
	if err := m.Ensure(50); err == nil || !strings.Contains(err.Error(), "available-disk threshold is 20%") {
		t.Fatalf("snapshot 20%% should reject, got %v", err)
	}

	cfg := config.GetInstance()
	originalThreshold := cfg.AvailableDiskThreshold
	originalDir := cfg.DiskDirectory
	t.Cleanup(func() {
		cfg.AvailableDiskThreshold = originalThreshold
		cfg.DiskDirectory = originalDir
	})
	cfg.DiskDirectory = dir
	cfg.AvailableDiskThreshold = 1
	m.SetLiveConfig(cfg)

	if err := m.Ensure(50); err != nil {
		t.Fatalf("live 1%% should allow the pull, got %v", err)
	}

	cfg.AvailableDiskThreshold = 20
	if err := m.Ensure(50); err == nil || !strings.Contains(err.Error(), "available-disk threshold is 20%") {
		t.Fatalf("live 20%% should reject again, got %v", err)
	}

	cfg.AvailableDiskThreshold = 0
	if err := m.Ensure(50); err != nil {
		t.Fatalf("live 0%% should only require the payload to fit, got %v", err)
	}
}

func TestPullResumeAfterInterrupt(t *testing.T) {
	m, ociPuller, _ := newTestManager(t)
	ociPuller.failOnce = true
	if _, err := m.UpsertDesired(sampleModel("gemma3", "ai/gemma3", "latest", 1, nil)); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	first, err := m.StartPull("gemma3")
	if err != nil {
		t.Fatalf("first pull: %v", err)
	}
	if waitOp(t, m, first.OperationID).Status != OpStatusFailed {
		t.Fatal("expected interrupted pull to fail")
	}

	second, err := m.StartPull("gemma3")
	if err != nil {
		t.Fatalf("resume pull: %v", err)
	}
	if waitOp(t, m, second.OperationID).Status != OpStatusSucceeded {
		t.Fatal("expected resumed pull to succeed")
	}
	waitState(t, m, "gemma3", models.ModelStateReady)
	if ociPuller.callCount() != 2 {
		t.Fatalf("expected interrupt then continue, calls=%d", ociPuller.callCount())
	}
}

func TestPruneDangling_RemovesUnreferencedModel(t *testing.T) {
	m, _, _ := newTestManager(t)
	orphan := plantModelTree(t, m.modelsRoot, "leftover")

	report, err := m.PruneDangling()
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if len(report.Removed) != 1 || report.Removed[0] != "leftover" {
		t.Fatalf("expected leftover removed, got %+v", report)
	}
	if _, err := os.Stat(orphan); !os.IsNotExist(err) {
		t.Fatal("expected leftover directory removed")
	}
}

func TestPrune_SharedOCIBlobRetained(t *testing.T) {
	m, ociPuller, _ := newTestManager(t)
	payload := []byte("shared-weights")
	sum := sha256.Sum256(payload)
	digest := "sha256:" + hex.EncodeToString(sum[:])
	ociPuller.writeOCI = true
	ociPuller.blob = payload
	ociPuller.digest = digest

	for _, name := range []string{"model-a", "model-b"} {
		if _, err := m.UpsertDesired(sampleModel(name, "ai/shared", "latest", 1, nil)); err != nil {
			t.Fatalf("upsert %s: %v", name, err)
		}
		op, err := m.StartPull(name)
		if err != nil {
			t.Fatalf("pull %s: %v", name, err)
		}
		if waitOp(t, m, op.OperationID).Status != OpStatusSucceeded {
			t.Fatalf("pull %s failed", name)
		}
	}

	if err := m.Remove("model-a"); err != nil {
		t.Fatalf("remove a: %v", err)
	}
	store, err := ocistore.Open(modelpull.OCIStoreDir(m.modelsRoot))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	has, err := store.HasBlob(digest)
	if err != nil || !has {
		t.Fatalf("shared blob must remain after first remove, has=%v err=%v", has, err)
	}

	if err := m.Remove("model-b"); err != nil {
		t.Fatalf("remove b: %v", err)
	}
	has, err = store.HasBlob(digest)
	if err != nil || has {
		t.Fatalf("blob should be gone after last remove, has=%v err=%v", has, err)
	}
}

func TestRemove_RefusedWhileBound(t *testing.T) {
	m, _, _ := newTestManager(t)
	if _, err := m.UpsertDesired(sampleModel("bound-model", "ai/bound", "latest", 1, nil)); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if err := m.db.ReplaceWorkloadModelRefs("ms-1", []string{"bound-model"}); err != nil {
		t.Fatalf("bind ref: %v", err)
	}
	err := m.Remove("bound-model")
	if err == nil || !strings.Contains(err.Error(), "bound") {
		t.Fatalf("expected refuse while bound, got %v", err)
	}
	if _, err := m.db.GetLocalModel("bound-model"); err != nil {
		t.Fatalf("model row must remain: %v", err)
	}
}

func collectOps(m *Manager) []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	ids := make([]string, 0, len(m.ops))
	for id := range m.ops {
		ids = append(ids, id)
	}
	return ids
}

func TestExampleModelsReachReadyWithContent(t *testing.T) {
	m, _, _ := newTestManager(t)
	examples := []struct {
		name       string
		repo       string
		revision   string
		registryID int
		files      []string
		format     string
	}{
		{
			name: "llama-2-7b-q2k", repo: "second-state/Llama-2-7B-Chat-GGUF",
			revision: "064fe43ea8c1e1f93477ef4a170bdc2b244ef02c", registryID: 5,
			files: []string{"llama-2-7b-chat.Q5_K_M.gguf"}, format: models.ModelFormatGGUF,
		},
		{
			name: "gemma3", repo: "ai/gemma3", revision: "4b-q8_0", registryID: 1,
			format: models.ModelFormatGGUF,
		},
		{
			name: "gemma3-4b-q4-k-m", repo: "ai/gemma3",
			revision:   "sha256:1eca257ec64d465cf38f766561e28eddb3b764adfba703288106a31cd2fe84a8",
			registryID: 1, format: models.ModelFormatGGUF,
		},
		{
			name: "qwen3-8-27b", repo: "Qwen/Qwen3.8-27B", revision: "main", registryID: 5,
			files: []string{
				"model-00001-of-00018.safetensors",
				"model.safetensors.index.json",
				"tokenizer.json",
				"config.json",
			},
			format: models.ModelFormatSafetensors,
		},
	}

	for _, ex := range examples {
		row := sampleModel(ex.name, ex.repo, ex.revision, ex.registryID, ex.files)
		row.Format = ex.format
		if _, err := m.UpsertDesired(row); err != nil {
			t.Fatalf("upsert %s: %v", ex.name, err)
		}
	}
	if err := m.Reconcile(context.Background()); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	for _, ex := range examples {
		ready := waitState(t, m, ex.name, models.ModelStateReady)
		content := modelpull.ContentDir(m.modelsRoot, ex.name)
		entries, err := os.ReadDir(content)
		if err != nil {
			t.Fatalf("content dir for %s: %v", ex.name, err)
		}
		if len(entries) == 0 {
			t.Fatalf("expected populated content/ for %s", ex.name)
		}
		if strings.TrimSpace(ready.ContentPath) == "" {
			t.Fatalf("expected content path on Ready row for %s", ex.name)
		}
		if _, err := os.Stat(modelpull.ManifestPath(m.modelsRoot, ex.name)); err != nil {
			t.Fatalf("expected on-disk manifest for %s: %v", ex.name, err)
		}
	}
}

func TestApplyControllerModels_UpsertsManagedAndWinsName(t *testing.T) {
	m, _, hfPuller := newTestManager(t)

	local := sampleModel("test-model", "org/old", "main", 5, []string{"old.gguf"})
	if _, err := m.UpsertDesired(local); err != nil {
		t.Fatalf("seed local: %v", err)
	}

	item := &models.ControllerModel{
		UUID:       "3f2c8a1e-2b64-4c0d-9f11-0a1b2c3d4e5f",
		Name:       "test-model",
		Repo:       "second-state/Llama-2-7B-Chat-GGUF",
		Revision:   "064fe43ea8c1e1f93477ef4a170bdc2b244ef02c",
		RegistryID: 5,
		Format:     models.ModelFormatGGUF,
	}
	item.SetFiles([]string{"llama-2-7b-chat.Q5_K_M.gguf"})
	if err := m.ApplyControllerModels(context.Background(), []*models.ControllerModel{item}); err != nil {
		t.Fatalf("apply controller models: %v", err)
	}

	got, err := m.db.GetLocalModel("test-model")
	if err != nil {
		t.Fatalf("get local: %v", err)
	}
	if got.Source != models.ModelSourceManaged {
		t.Fatalf("expected managed source, got %q", got.Source)
	}
	if got.Repo != item.Repo {
		t.Fatalf("expected controller repo to win, got %q", got.Repo)
	}
	ready := waitState(t, m, "test-model", models.ModelStateReady)
	if ready.Source != models.ModelSourceManaged {
		t.Fatalf("expected managed source after pull, got %q", ready.Source)
	}
	if hfPuller.callCount() < 1 {
		t.Fatal("expected huggingface pull for managed model")
	}
}

func TestApplyManifest_RejectsManagedNameWhileProvisioned(t *testing.T) {
	m, _, _ := newTestManager(t)
	cfg := config.GetInstance()
	origUUID := cfg.IOFogUUID
	cfg.IOFogUUID = "agent-uuid-managed-name"
	t.Cleanup(func() { cfg.IOFogUUID = origUUID })

	item := &models.ControllerModel{
		UUID:       "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee",
		Name:       "fleet-model",
		Repo:       "org/repo",
		RegistryID: 5,
	}
	if err := m.SaveControllerModels([]*models.ControllerModel{item}); err != nil {
		t.Fatalf("save controller models: %v", err)
	}
	if _, err := m.UpsertDesired(item.ToLocalModel()); err != nil {
		t.Fatalf("upsert managed: %v", err)
	}

	doc := &models.LocalModelManifest{
		APIVersion: "edgelet.iofog.org/v1",
		Kind:       "Model",
		Spec: models.LocalModelSpec{
			Repo:     "org/other",
			Registry: 5,
		},
	}
	doc.Metadata.Name = "fleet-model"
	_, err := m.ApplyManifest(doc)
	if err == nil || !strings.Contains(err.Error(), "controller-managed") {
		t.Fatalf("expected local apply to be rejected for managed name, got %v", err)
	}
}
