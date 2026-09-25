package knowledgemanager

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/eclipse-iofog/edgelet/internal/config"
	"github.com/eclipse-iofog/edgelet/internal/modelmanager"
	"github.com/eclipse-iofog/edgelet/internal/modelpull"
	"github.com/eclipse-iofog/edgelet/internal/modelpull/hf"
	"github.com/eclipse-iofog/edgelet/internal/modelpull/oci"
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
	if err := os.WriteFile(filepath.Join(content, "guide.jsonl"), []byte("ok"), 0o644); err != nil {
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
		ContentPaths:      []string{"content/guide.jsonl"},
		TotalBytes:        2,
		Format:            req.FormatHint,
	}
	if err := modelpull.WriteOnDiskManifest(manifestPath, onDisk); err != nil {
		return nil, err
	}
	return &modelpull.Result{
		Digest:           onDisk.Digest,
		ResolvedRevision: "resolved-rev",
		RevisionFloating: strings.TrimSpace(req.Revision) == "",
		TotalBytes:       2,
		ManifestPath:     manifestPath,
		ContentPath:      content,
		ContentPaths:     []string{"content/guide.jsonl"},
		Format:           req.FormatHint,
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

func sampleKnowledge(name, repo, revision string, registryID int, files []string) *models.LocalKnowledge {
	row := &models.LocalKnowledge{
		Name:       name,
		Repo:       repo,
		Revision:   revision,
		RegistryID: registryID,
		Format:     models.KnowledgeFormatJSONL,
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

func waitState(t *testing.T, m *Manager, name, want string) *models.LocalKnowledge {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		row, err := m.db.GetLocalKnowledge(name)
		if err == nil && row.State == want {
			return row
		}
		time.Sleep(10 * time.Millisecond)
	}
	row, _ := m.db.GetLocalKnowledge(name)
	t.Fatalf("timed out waiting for knowledge %s state %s (have %+v)", name, want, row)
	return nil
}

func TestUpsertRevisionChangeRepulls(t *testing.T) {
	m, ociPuller, _ := newTestManager(t)
	row := sampleKnowledge("product-docs", "acme/docs", "v1", 1, nil)
	if _, err := m.UpsertDesired(row); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if err := m.Reconcile(context.Background()); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	ready := waitState(t, m, "product-docs", models.KnowledgeStateReady)
	if ready.Generation != 1 {
		t.Fatalf("expected generation 1, got %d", ready.Generation)
	}
	if ociPuller.callCount() != 1 {
		t.Fatalf("expected 1 pull, got %d", ociPuller.callCount())
	}
	firstManifest, err := os.ReadFile(modelpull.ManifestPath(m.knowledgeRoot, "product-docs"))
	if err != nil {
		t.Fatalf("read first manifest: %v", err)
	}

	update := sampleKnowledge("product-docs", "acme/docs", "v2", 1, nil)
	updated, err := m.UpsertDesired(update)
	if err != nil {
		t.Fatalf("upsert revision change: %v", err)
	}
	if updated.Generation != 2 || updated.State != models.KnowledgeStatePending {
		t.Fatalf("expected generation bump to Pending, got gen=%d state=%s", updated.Generation, updated.State)
	}
	if err := m.Reconcile(context.Background()); err != nil {
		t.Fatalf("reconcile after bump: %v", err)
	}
	waitState(t, m, "product-docs", models.KnowledgeStateReady)
	if ociPuller.callCount() != 2 {
		t.Fatalf("expected re-pull after generation bump, got %d pulls", ociPuller.callCount())
	}
	secondManifest, err := os.ReadFile(modelpull.ManifestPath(m.knowledgeRoot, "product-docs"))
	if err != nil {
		t.Fatalf("read second manifest: %v", err)
	}
	var first, second modelpull.OnDiskManifest
	if err := json.Unmarshal(firstManifest, &first); err != nil {
		t.Fatalf("decode first manifest: %v", err)
	}
	if err := json.Unmarshal(secondManifest, &second); err != nil {
		t.Fatalf("decode second manifest: %v", err)
	}
	if first.RequestedRevision != "v1" || second.RequestedRevision != "v2" {
		t.Fatalf("expected new manifest revision, first=%q second=%q", first.RequestedRevision, second.RequestedRevision)
	}
	if first.IdentityKey == second.IdentityKey {
		t.Fatal("revision change must rewrite pull identity on manifest.json")
	}
}

func TestKnowledgePoolIndependentOfModel(t *testing.T) {
	km, knowledgePuller, _ := newTestManager(t)
	knowledgePuller.block = make(chan struct{})
	for _, name := range []string{"docs-a", "docs-b", "docs-c"} {
		if _, err := km.UpsertDesired(sampleKnowledge(name, "acme/"+name, "latest", 1, nil)); err != nil {
			t.Fatalf("upsert %s: %v", name, err)
		}
	}

	mm := modelmanager.New(km.db, t.TempDir())
	modelPuller := &stubPuller{}
	mm.SetPullers(modelPuller, modelPuller)
	mm.SetTimeout(5 * time.Second)
	modelRow := &models.LocalModel{
		Name:       "gguf-weights",
		Repo:       "org/weights",
		Revision:   "main",
		RegistryID: 1,
		Format:     models.ModelFormatGGUF,
	}
	modelRow.NormalizeDefaults()
	if _, err := mm.UpsertDesired(modelRow); err != nil {
		t.Fatalf("upsert model: %v", err)
	}

	for _, name := range []string{"docs-a", "docs-b", "docs-c"} {
		if _, err := km.StartPull(name); err != nil {
			t.Fatalf("start knowledge %s: %v", name, err)
		}
	}
	modelOp, err := mm.StartPull("gguf-weights")
	if err != nil {
		t.Fatalf("start model pull: %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if atomic.LoadInt32(&knowledgePuller.inflight) == 2 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if got := atomic.LoadInt32(&knowledgePuller.inflight); got != 2 {
		t.Fatalf("expected 2 in-flight knowledge pulls, got %d", got)
	}
	if peak := atomic.LoadInt32(&knowledgePuller.maxIn); peak > 2 {
		t.Fatalf("knowledge cap exceeded: max in-flight %d", peak)
	}

	modelDeadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(modelDeadline) {
		op, ok := mm.GetPull(modelOp.OperationID)
		if ok && op.Status == modelmanager.OpStatusSucceeded {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	modelGot, ok := mm.GetPull(modelOp.OperationID)
	if !ok || modelGot.Status != modelmanager.OpStatusSucceeded {
		t.Fatalf("model pull must proceed while knowledge pool is full, got %+v", modelGot)
	}
	if modelPuller.callCount() != 1 {
		t.Fatalf("expected model pull to run, calls=%d", modelPuller.callCount())
	}
	if knowledgePuller.callCount() != 2 {
		t.Fatalf("third knowledge pull must wait on its own pool, calls=%d", knowledgePuller.callCount())
	}

	close(knowledgePuller.block)
	waitState(t, km, "docs-a", models.KnowledgeStateReady)
	waitState(t, km, "docs-b", models.KnowledgeStateReady)
	waitState(t, km, "docs-c", models.KnowledgeStateReady)
}

func TestDiskCheckRejectsBeforeDownload(t *testing.T) {
	m, ociPuller, _ := newTestManager(t)
	ociPuller.estimate = 50
	m.SetDiskPolicy(t.TempDir(), 20, func(string) (int64, int64, error) {
		return 100, 1000, nil
	})
	if _, err := m.UpsertDesired(sampleKnowledge("product-docs", "acme/docs", "latest", 1, nil)); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	op, err := m.StartPull("product-docs")
	if err != nil {
		t.Fatalf("start pull: %v", err)
	}
	got := waitOp(t, m, op.OperationID)
	if got.Status != OpStatusFailed || !strings.Contains(got.Error, "available-disk threshold") {
		t.Fatalf("expected disk threshold failure, got %+v", got)
	}
	failed := waitState(t, m, "product-docs", models.KnowledgeStateFailed)
	if failed.LastError == "" {
		t.Fatal("expected lastError after disk rejection")
	}
	if _, err := os.Stat(modelpull.ManifestPath(m.knowledgeRoot, "product-docs")); !os.IsNotExist(err) {
		t.Fatal("failed disk check must not write manifest.json")
	}
}

func TestFailedPullRecordsLastError(t *testing.T) {
	m, ociPuller, _ := newTestManager(t)
	ociPuller.err = context.Canceled
	if _, err := m.UpsertDesired(sampleKnowledge("product-docs", "acme/docs", "latest", 1, nil)); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	op, err := m.StartPull("product-docs")
	if err != nil {
		t.Fatalf("start pull: %v", err)
	}
	got := waitOp(t, m, op.OperationID)
	if got.Status != OpStatusFailed || got.Error == "" {
		t.Fatalf("expected failed operation, got %+v", got)
	}
	failed := waitState(t, m, "product-docs", models.KnowledgeStateFailed)
	if failed.LastError == "" {
		t.Fatal("expected lastError on failed knowledge")
	}
	if err := m.Reconcile(context.Background()); err != nil {
		t.Fatalf("reconcile after fail: %v", err)
	}
	if ociPuller.callCount() != 1 {
		t.Fatalf("failed generation should not auto-retry, got %d pulls", ociPuller.callCount())
	}
}

func TestStartPull_SerializesOnePerNameAndMaxTwoGlobal(t *testing.T) {
	m, ociPuller, _ := newTestManager(t)
	ociPuller.block = make(chan struct{})
	for _, name := range []string{"docs-a", "docs-b", "docs-c"} {
		if _, err := m.UpsertDesired(sampleKnowledge(name, "acme/"+name, "latest", 1, nil)); err != nil {
			t.Fatalf("upsert %s: %v", name, err)
		}
	}
	opA, err := m.StartPull("docs-a")
	if err != nil {
		t.Fatalf("start a: %v", err)
	}
	again, err := m.StartPull("docs-a")
	if err != nil {
		t.Fatalf("start a again: %v", err)
	}
	if again.OperationID != opA.OperationID {
		t.Fatalf("expected same operation for in-flight name, got %s vs %s", again.OperationID, opA.OperationID)
	}
	if _, err := m.StartPull("docs-b"); err != nil {
		t.Fatalf("start b: %v", err)
	}
	if _, err := m.StartPull("docs-c"); err != nil {
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
	close(ociPuller.block)
	waitState(t, m, "docs-a", models.KnowledgeStateReady)
	waitState(t, m, "docs-b", models.KnowledgeStateReady)
	waitState(t, m, "docs-c", models.KnowledgeStateReady)
}

func TestStartPull_DispatchesByRegistryType(t *testing.T) {
	m, ociPuller, hfPuller := newTestManager(t)
	ociRow := sampleKnowledge("wiki-faiss", "acme/wiki", "latest", 1, nil)
	if _, err := m.UpsertDesired(ociRow); err != nil {
		t.Fatalf("upsert oci knowledge: %v", err)
	}
	hfRow := sampleKnowledge("product-docs", "acme/manuals", "main", 5, []string{"data/**/*.jsonl"})
	if _, err := m.UpsertDesired(hfRow); err != nil {
		t.Fatalf("upsert hf knowledge: %v", err)
	}

	ociOp, err := m.StartPull("wiki-faiss")
	if err != nil {
		t.Fatalf("oci pull: %v", err)
	}
	hfOp, err := m.StartPull("product-docs")
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

func TestActivePullNames(t *testing.T) {
	m, ociPuller, _ := newTestManager(t)
	ociPuller.block = make(chan struct{})
	if _, err := m.UpsertDesired(sampleKnowledge("product-docs", "acme/docs", "latest", 1, nil)); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if _, err := m.StartPull("product-docs"); err != nil {
		t.Fatalf("start pull: %v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if names := m.ActivePullNames(); len(names) == 1 && names[0] == "product-docs" {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if names := m.ActivePullNames(); len(names) != 1 || names[0] != "product-docs" {
		t.Fatalf("expected active pull name, got %v", names)
	}
	close(ociPuller.block)
	waitState(t, m, "product-docs", models.KnowledgeStateReady)
	deadline = time.Now().Add(2 * time.Second)
	var names []string
	for time.Now().Before(deadline) {
		names = m.ActivePullNames()
		if len(names) == 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if len(names) != 0 {
		t.Fatalf("expected no active pulls after success, got %v", names)
	}
}

func TestApplyManifest_RejectsManagedNameWhileProvisioned(t *testing.T) {
	m, _, _ := newTestManager(t)
	cfg := config.GetInstance()
	origUUID := cfg.IOFogUUID
	cfg.IOFogUUID = "agent-uuid-managed-knowledge"
	t.Cleanup(func() { cfg.IOFogUUID = origUUID })

	item := &models.ControllerKnowledge{
		UUID:       "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee",
		Name:       "fleet-docs",
		Repo:       "org/repo",
		RegistryID: 5,
	}
	if err := m.SaveControllerKnowledge([]*models.ControllerKnowledge{item}); err != nil {
		t.Fatalf("save controller knowledge: %v", err)
	}
	if _, err := m.UpsertDesired(item.ToLocalKnowledge()); err != nil {
		t.Fatalf("upsert managed: %v", err)
	}

	doc := &models.LocalKnowledgeManifest{
		APIVersion: "edgelet.iofog.org/v1",
		Kind:       "Knowledge",
		Spec: models.LocalKnowledgeSpec{
			Repo:     "org/other",
			Registry: 5,
		},
	}
	doc.Metadata.Name = "fleet-docs"
	_, err := m.ApplyManifest(doc)
	if err == nil || !strings.Contains(err.Error(), "controller-managed") {
		t.Fatalf("expected local apply to be rejected for managed name, got %v", err)
	}
}

func TestDefaultAdaptersAreKnowledgeClass(t *testing.T) {
	m := New(nil, t.TempDir())
	hfAdapter, ok := m.hf.(*hf.Adapter)
	if !ok {
		t.Fatalf("expected huggingface adapter, got %T", m.hf)
	}
	if hfAdapter.RepoClass != hf.RepoClassDataset {
		t.Fatalf("knowledge manager must pull datasets, got %q", hfAdapter.RepoClass)
	}
	oras, ok := m.oci.(*oci.Adapter)
	if !ok {
		t.Fatalf("expected oci adapter, got %T", m.oci)
	}
	if !oras.GenericORAS {
		t.Fatal("knowledge manager must use generic ORAS unpack")
	}
}
