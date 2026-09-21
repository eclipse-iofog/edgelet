package hf

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/eclipse-iofog/edgelet/internal/modelpull"
	"github.com/eclipse-iofog/edgelet/internal/models"
)

const fixtureCommit = "064fe43ea8c1e1f93477ef4a170bdc2b244ef02c"

type hubFixture struct {
	sha       string
	repo      string
	files     map[string][]byte
	token     string
	failAfter map[string]int
	mu        sync.Mutex
	sawAuth   string
	apiPaths  []string
	// resolvePaths records every /resolve/ request so tests can assert
	// dataset downloads use /datasets/{repo}/resolve/ and models do not.
	resolvePaths []string
	// requireDatasetResolve rejects model-style file URLs (as the Hub does
	// for a dataset repo that has no matching model path).
	requireDatasetResolve bool
}

func newHubFixture(repo string, files map[string][]byte) *hubFixture {
	return &hubFixture{
		sha:       fixtureCommit,
		repo:      repo,
		files:     files,
		failAfter: map[string]int{},
	}
}

func (f *hubFixture) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.sawAuth = r.Header.Get("Authorization")
		f.mu.Unlock()
		if f.token != "" && r.Header.Get("Authorization") != "Bearer "+f.token {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api/models/") || strings.HasPrefix(r.URL.Path, "/api/datasets/") {
			f.mu.Lock()
			f.apiPaths = append(f.apiPaths, r.URL.Path)
			f.mu.Unlock()
			f.serveInfo(w, r)
			return
		}
		if idx := strings.Index(r.URL.Path, "/resolve/"); idx >= 0 {
			f.mu.Lock()
			f.resolvePaths = append(f.resolvePaths, r.URL.Path)
			requireDataset := f.requireDatasetResolve
			f.mu.Unlock()
			if requireDataset && !strings.HasPrefix(r.URL.Path, "/datasets/") {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			f.serveFile(w, r)
			return
		}
		http.NotFound(w, r)
	})
}

func (f *hubFixture) serveInfo(w http.ResponseWriter, _ *http.Request) {
	type sibling struct {
		RFilename string `json:"rfilename"`
		Size      int64  `json:"size"`
	}
	payload := struct {
		SHA      string    `json:"sha"`
		Siblings []sibling `json:"siblings"`
	}{SHA: f.sha}
	for name, body := range f.files {
		payload.Siblings = append(payload.Siblings, sibling{RFilename: name, Size: int64(len(body))})
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(raw)
}

func (f *hubFixture) serveFile(w http.ResponseWriter, r *http.Request) {
	idx := strings.Index(r.URL.Path, "/resolve/")
	rest := r.URL.Path[idx+len("/resolve/"):]
	_, filePath, ok := strings.Cut(rest, "/")
	if !ok {
		http.NotFound(w, r)
		return
	}
	f.mu.Lock()
	payload, exists := f.files[filePath]
	failAfter := f.failAfter[filePath]
	f.mu.Unlock()
	if !exists {
		http.NotFound(w, r)
		return
	}

	start := int64(0)
	if rng := r.Header.Get("Range"); strings.HasPrefix(rng, "bytes=") {
		spec := strings.TrimPrefix(rng, "bytes=")
		spec = strings.TrimSuffix(spec, "-")
		if n, err := strconv.ParseInt(spec, 10, 64); err == nil {
			start = n
		}
	}
	if start > 0 {
		if start > int64(len(payload)) {
			http.Error(w, "invalid range", http.StatusRequestedRangeNotSatisfiable)
			return
		}
		body := payload[start:]
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Range", "bytes "+strconv.FormatInt(start, 10)+"-"+strconv.Itoa(len(payload)-1)+"/"+strconv.Itoa(len(payload)))
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		w.WriteHeader(http.StatusPartialContent)
		if r.Method != http.MethodHead {
			_, _ = w.Write(body)
		}
		return
	}

	w.Header().Set("Content-Type", "application/octet-stream")
	if r.Method != http.MethodHead && failAfter > 0 && failAfter < len(payload) {
		w.Header().Set("Content-Length", strconv.Itoa(len(payload)))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(payload[:failAfter])
		f.mu.Lock()
		delete(f.failAfter, filePath)
		f.mu.Unlock()
		return
	}
	w.Header().Set("Content-Length", strconv.Itoa(len(payload)))
	if r.Method == http.MethodHead {
		w.WriteHeader(http.StatusOK)
		return
	}
	_, _ = w.Write(payload)
}

func startHub(t *testing.T, fixture *hubFixture) (*httptest.Server, *models.Registry) {
	t.Helper()
	srv := httptest.NewServer(fixture.handler())
	t.Cleanup(srv.Close)
	reg := models.NewRegistryBuilder().
		SetID(5).
		SetURL(srv.URL).
		SetType(models.RegistryTypeHF).
		SetIsPublic(fixture.token == "").
		SetPassword(fixture.token).
		SetInsecure(true).
		Build()
	return srv, reg
}

func TestAdapterPull_SingleGGUF(t *testing.T) {
	weights := []byte("GGUF-single-file")
	fixture := newHubFixture("second-state/Llama-2-7B-Chat-GGUF", map[string][]byte{
		"llama-2-7b-chat.Q5_K_M.gguf": weights,
		"llama-2-7b-chat.Q4_K_M.gguf": []byte("other-quant"),
		"README.md":                   []byte("readme"),
	})
	_, reg := startHub(t, fixture)
	root := t.TempDir()
	result, err := (&Adapter{}).Pull(context.Background(), modelpull.Request{
		Name:       "llama-2-7b-q2k",
		Repo:       fixture.repo,
		Revision:   fixtureCommit,
		Registry:   reg,
		Files:      []string{"llama-2-7b-chat.Q5_K_M.gguf"},
		ModelsRoot: root,
		FormatHint: models.ModelFormatGGUF,
	})
	if err != nil {
		t.Fatalf("pull: %v", err)
	}
	if result.ResolvedRevision != fixtureCommit {
		t.Fatalf("resolved revision: got %q", result.ResolvedRevision)
	}
	if result.RevisionFloating || result.RevisionKind != modelpull.RevisionKindCommit {
		t.Fatalf("expected pinned commit, got %+v", result)
	}
	if result.Format != models.ModelFormatGGUF {
		t.Fatalf("format: got %q", result.Format)
	}

	got, err := os.ReadFile(filepath.Join(root, "llama-2-7b-q2k", "content", "llama-2-7b-chat.Q5_K_M.gguf"))
	if err != nil {
		t.Fatalf("read content: %v", err)
	}
	if !bytes.Equal(got, weights) {
		t.Fatal("materialized weights mismatch")
	}
	if _, err := os.Stat(filepath.Join(root, "llama-2-7b-q2k", "content", "llama-2-7b-chat.Q4_K_M.gguf")); !os.IsNotExist(err) {
		t.Fatal("must not download unselected gguf")
	}
	if _, err := os.Stat(filepath.Join(root, "oci-store")); !os.IsNotExist(err) {
		t.Fatal("hub pulls must not create oci-store")
	}

	onDisk := readOnDisk(t, root, "llama-2-7b-q2k")
	if onDisk.RegistryType != models.RegistryTypeHF || onDisk.ResolvedRevision != fixtureCommit {
		t.Fatalf("on-disk manifest: %+v", onDisk)
	}
	if len(onDisk.Files) != 1 || onDisk.Files[0] != "llama-2-7b-chat.Q5_K_M.gguf" {
		t.Fatalf("files: %v", onDisk.Files)
	}
	if len(onDisk.ContentPaths) != 1 || onDisk.ContentPaths[0] != "content/llama-2-7b-chat.Q5_K_M.gguf" {
		t.Fatalf("contentPaths: %v", onDisk.ContentPaths)
	}
}

func TestAdapterPull_ShardedSafetensorsExplicit(t *testing.T) {
	index := []byte(`{"weight_map":{"a":"model-00001-of-00002.safetensors","b":"model-00002-of-00002.safetensors"}}`)
	fixture := newHubFixture("Qwen/Qwen3.8-27B", map[string][]byte{
		"model.safetensors.index.json":     index,
		"model-00001-of-00002.safetensors": []byte("shard-one"),
		"model-00002-of-00002.safetensors": []byte("shard-two"),
		"tokenizer.json":                   []byte(`{"tok":true}`),
		"config.json":                      []byte(`{"arch":"qwen"}`),
		"README.md":                        []byte("skip-me"),
	})
	_, reg := startHub(t, fixture)
	root := t.TempDir()
	files := []string{
		"model-00001-of-00002.safetensors",
		"model-00002-of-00002.safetensors",
		"model.safetensors.index.json",
		"tokenizer.json",
		"config.json",
	}
	result, err := (&Adapter{}).Pull(context.Background(), modelpull.Request{
		Name:       "qwen3-8-27b",
		Repo:       fixture.repo,
		Revision:   "main",
		Registry:   reg,
		Files:      files,
		ModelsRoot: root,
	})
	if err != nil {
		t.Fatalf("pull: %v", err)
	}
	if !result.RevisionFloating || result.RevisionKind != modelpull.RevisionKindBranch {
		t.Fatalf("expected floating branch, got %+v", result)
	}
	if result.ResolvedRevision != fixtureCommit {
		t.Fatalf("resolved revision must be the commit hash, got %q", result.ResolvedRevision)
	}
	if result.Format != models.ModelFormatSafetensors {
		t.Fatalf("format: got %q", result.Format)
	}
	for _, name := range files {
		if _, err := os.Stat(filepath.Join(root, "qwen3-8-27b", "content", name)); err != nil {
			t.Fatalf("missing %s: %v", name, err)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "qwen3-8-27b", "content", "README.md")); !os.IsNotExist(err) {
		t.Fatal("explicit list must not pull README")
	}
}

func TestAdapterPull_SnapshotEmptyFiles(t *testing.T) {
	index := []byte(`{"weight_map":{"a":"model-00001-of-00002.safetensors","b":"model-00002-of-00002.safetensors"}}`)
	fixture := newHubFixture("Qwen/Qwen3.8-27B", map[string][]byte{
		"model.safetensors.index.json":     index,
		"model-00001-of-00002.safetensors": []byte("shard-one"),
		"model-00002-of-00002.safetensors": []byte("shard-two"),
		"tokenizer.json":                   []byte(`{"tok":true}`),
		"tokenizer_config.json":            []byte(`{}`),
		"vocab.json":                       []byte(`{}`),
		"video_preprocessor_config.json":   []byte(`{}`),
		"config.json":                      []byte(`{"arch":"qwen"}`),
		"README.md":                        []byte("skip-me"),
		".gitattributes":                   []byte("*"),
	})
	_, reg := startHub(t, fixture)
	root := t.TempDir()
	result, err := (&Adapter{}).Pull(context.Background(), modelpull.Request{
		Name:       "qwen-snapshot",
		Repo:       fixture.repo,
		Revision:   "",
		Registry:   reg,
		Files:      []string{},
		ModelsRoot: root,
	})
	if err != nil {
		t.Fatalf("snapshot pull: %v", err)
	}
	if !result.RevisionFloating || result.ResolvedRevision != fixtureCommit {
		t.Fatalf("empty revision should float and record commit, got %+v", result)
	}

	want := []string{
		"config.json",
		"model-00001-of-00002.safetensors",
		"model-00002-of-00002.safetensors",
		"model.safetensors.index.json",
		"tokenizer.json",
		"tokenizer_config.json",
		"video_preprocessor_config.json",
		"vocab.json",
	}
	for _, name := range want {
		if _, err := os.Stat(filepath.Join(root, "qwen-snapshot", "content", name)); err != nil {
			t.Fatalf("snapshot missing %s: %v", name, err)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "qwen-snapshot", "content", "README.md")); !os.IsNotExist(err) {
		t.Fatal("snapshot must not pull README")
	}
	onDisk := readOnDisk(t, root, "qwen-snapshot")
	if onDisk.RequestedRevision != "" || onDisk.ResolvedRevision != fixtureCommit {
		t.Fatalf("revision fields: %+v", onDisk)
	}
}

func TestAdapterPull_MultiGGUFEmptyFiles(t *testing.T) {
	fixture := newHubFixture("org/multi-gguf", map[string][]byte{
		"a.gguf": []byte("a"),
		"b.gguf": []byte("b"),
	})
	_, reg := startHub(t, fixture)
	root := t.TempDir()
	_, err := (&Adapter{}).Pull(context.Background(), modelpull.Request{
		Name:       "multi",
		Repo:       fixture.repo,
		Registry:   reg,
		ModelsRoot: root,
	})
	if err == nil || !strings.Contains(err.Error(), "explicit spec.files") {
		t.Fatalf("expected multi-gguf error, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "multi", "manifest.json")); !os.IsNotExist(err) {
		t.Fatal("failed pull must not write manifest.json")
	}
}

func TestAdapterPull_EmptyGlob(t *testing.T) {
	fixture := newHubFixture("org/repo", map[string][]byte{"model.gguf": []byte("g")})
	_, reg := startHub(t, fixture)
	_, err := (&Adapter{}).Pull(context.Background(), modelpull.Request{
		Name:       "glob-miss",
		Repo:       fixture.repo,
		Registry:   reg,
		Files:      []string{"*.bin"},
		ModelsRoot: t.TempDir(),
	})
	if err == nil || !strings.Contains(err.Error(), "matched no files") {
		t.Fatalf("expected empty glob error, got %v", err)
	}
}

func TestAdapterPull_BearerToken(t *testing.T) {
	fixture := newHubFixture("org/private", map[string][]byte{"model.gguf": []byte("g")})
	fixture.token = "hf_test_token"
	_, reg := startHub(t, fixture)
	_, err := (&Adapter{}).Pull(context.Background(), modelpull.Request{
		Name:       "private",
		Repo:       fixture.repo,
		Registry:   reg,
		Files:      []string{"model.gguf"},
		ModelsRoot: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("private pull: %v", err)
	}
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	if fixture.sawAuth != "Bearer hf_test_token" {
		t.Fatalf("expected bearer token, got %q", fixture.sawAuth)
	}
}

func TestAdapterPull_ResumeInterruptedFile(t *testing.T) {
	payload := bytes.Repeat([]byte("W"), 32*1024)
	fixture := newHubFixture("org/resume", map[string][]byte{"model.gguf": payload})
	fixture.failAfter["model.gguf"] = 4096
	_, reg := startHub(t, fixture)
	root := t.TempDir()
	req := modelpull.Request{
		Name:       "resume",
		Repo:       fixture.repo,
		Registry:   reg,
		Files:      []string{"model.gguf"},
		ModelsRoot: root,
	}
	_, err := (&Adapter{}).Pull(context.Background(), req)
	if err == nil {
		t.Fatal("expected first pull to fail on interrupted download")
	}
	partial := filepath.Join(root, "resume", "content.tmp", "model.gguf"+modelpull.IncompleteExt)
	if fi, statErr := os.Stat(partial); statErr != nil || fi.Size() == 0 {
		t.Fatalf("expected partial download to be retained, err=%v", statErr)
	}
	if _, err := os.Stat(filepath.Join(root, "resume", "manifest.json")); !os.IsNotExist(err) {
		t.Fatal("manifest.json must not be written for a failed pull")
	}

	result, err := (&Adapter{}).Pull(context.Background(), req)
	if err != nil {
		t.Fatalf("resume pull: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(root, "resume", "content", "model.gguf"))
	if err != nil {
		t.Fatalf("read resumed content: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("resumed content mismatch (len=%d)", len(got))
	}
	if result.TotalBytes != int64(len(payload)) {
		t.Fatalf("total bytes: got %d", result.TotalBytes)
	}
}

func TestAdapterPull_DatasetUsesDatasetsAPI(t *testing.T) {
	fixture := newHubFixture("acme/product-manuals", map[string][]byte{
		"data/guide.jsonl":  []byte(`{"q":"hi"}`),
		"index/faiss.index": []byte("idx"),
		"README.md":         []byte("docs"),
		"data/extra.jsonl":  []byte(`{"q":"bye"}`),
		"skip/notes.txt":    []byte("nope"),
	})
	fixture.requireDatasetResolve = true
	_, reg := startHub(t, fixture)
	root := t.TempDir()
	result, err := (&Adapter{RepoClass: RepoClassDataset}).Pull(context.Background(), modelpull.Request{
		Name:       "product-docs",
		Repo:       fixture.repo,
		Revision:   fixtureCommit,
		Registry:   reg,
		Files:      []string{"data/**/*.jsonl", "index/faiss.index"},
		ModelsRoot: root,
		FormatHint: models.KnowledgeFormatJSONL,
	})
	if err != nil {
		t.Fatalf("dataset pull: %v", err)
	}
	if result.Format != models.KnowledgeFormatJSONL {
		t.Fatalf("format: got %q", result.Format)
	}
	for _, name := range []string{"data/guide.jsonl", "data/extra.jsonl", "index/faiss.index"} {
		if _, err := os.Stat(filepath.Join(root, "product-docs", "content", name)); err != nil {
			t.Fatalf("missing %s: %v", name, err)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "product-docs", "content", "README.md")); !os.IsNotExist(err) {
		t.Fatal("glob must not pull README.md")
	}
	if _, err := os.Stat(filepath.Join(root, "product-docs", "content", "skip/notes.txt")); !os.IsNotExist(err) {
		t.Fatal("glob must not pull unmatched paths")
	}
	fixture.mu.Lock()
	paths := append([]string(nil), fixture.apiPaths...)
	resolvePaths := append([]string(nil), fixture.resolvePaths...)
	fixture.mu.Unlock()
	if len(paths) == 0 {
		t.Fatal("expected hub dataset API request")
	}
	for _, p := range paths {
		if !strings.Contains(p, "/api/datasets/") {
			t.Fatalf("knowledge pull must use /api/datasets/, got %q", p)
		}
		if strings.Contains(p, "/api/models/") {
			t.Fatalf("knowledge pull must not use /api/models/, got %q", p)
		}
	}
	if len(resolvePaths) == 0 {
		t.Fatal("expected hub dataset file download")
	}
	for _, p := range resolvePaths {
		if !strings.Contains(p, "/datasets/") || !strings.Contains(p, "/resolve/") {
			t.Fatalf("knowledge file download must use /datasets/{repo}/resolve/, got %q", p)
		}
	}
}

func TestAdapterPull_ModelUsesModelsAPI(t *testing.T) {
	fixture := newHubFixture("org/weights", map[string][]byte{"model.gguf": []byte("g")})
	_, reg := startHub(t, fixture)
	_, err := (&Adapter{}).Pull(context.Background(), modelpull.Request{
		Name:       "llama-2-7b-q2k",
		Repo:       fixture.repo,
		Registry:   reg,
		Files:      []string{"model.gguf"},
		ModelsRoot: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("model pull: %v", err)
	}
	fixture.mu.Lock()
	paths := append([]string(nil), fixture.apiPaths...)
	resolvePaths := append([]string(nil), fixture.resolvePaths...)
	fixture.mu.Unlock()
	if len(paths) == 0 {
		t.Fatal("expected hub model API request")
	}
	for _, p := range paths {
		if !strings.Contains(p, "/api/models/") {
			t.Fatalf("model pull must use /api/models/, got %q", p)
		}
		if strings.Contains(p, "/api/datasets/") {
			t.Fatalf("model pull must not use /api/datasets/, got %q", p)
		}
	}
	if len(resolvePaths) == 0 {
		t.Fatal("expected hub model file download")
	}
	for _, p := range resolvePaths {
		if strings.Contains(p, "/datasets/") {
			t.Fatalf("model file download must not use /datasets/, got %q", p)
		}
		if !strings.Contains(p, "/resolve/") {
			t.Fatalf("model file download must use /{repo}/resolve/, got %q", p)
		}
	}
}

func TestAdapterPull_DatasetSnapshotEmptyFiles(t *testing.T) {
	fixture := newHubFixture("acme/corpus", map[string][]byte{
		"data/a.jsonl": []byte("a"),
		"data/b.jsonl": []byte("b"),
		"README.md":    []byte("all-of-it"),
	})
	fixture.requireDatasetResolve = true
	_, reg := startHub(t, fixture)
	root := t.TempDir()
	result, err := (&Adapter{RepoClass: RepoClassDataset}).Pull(context.Background(), modelpull.Request{
		Name:       "corpus",
		Repo:       fixture.repo,
		Revision:   "",
		Registry:   reg,
		Files:      []string{},
		ModelsRoot: root,
	})
	if err != nil {
		t.Fatalf("dataset snapshot: %v", err)
	}
	if !result.RevisionFloating || result.RevisionKind != modelpull.RevisionKindBranch {
		t.Fatalf("empty revision should float on main, got %+v", result)
	}
	if result.Format != models.KnowledgeFormatUnknown {
		t.Fatalf("empty format hint should be unknown, got %q", result.Format)
	}
	for _, name := range []string{"data/a.jsonl", "data/b.jsonl", "README.md"} {
		if _, err := os.Stat(filepath.Join(root, "corpus", "content", name)); err != nil {
			t.Fatalf("snapshot missing %s: %v", name, err)
		}
	}
}

func TestAdapterPull_DatasetResumeInterruptedFile(t *testing.T) {
	payload := bytes.Repeat([]byte("K"), 32*1024)
	fixture := newHubFixture("acme/resume-docs", map[string][]byte{"data/guide.jsonl": payload})
	fixture.requireDatasetResolve = true
	fixture.failAfter["data/guide.jsonl"] = 4096
	_, reg := startHub(t, fixture)
	root := t.TempDir()
	req := modelpull.Request{
		Name:       "resume-docs",
		Repo:       fixture.repo,
		Registry:   reg,
		Files:      []string{"data/guide.jsonl"},
		ModelsRoot: root,
	}
	adapter := &Adapter{RepoClass: RepoClassDataset}
	_, err := adapter.Pull(context.Background(), req)
	if err == nil {
		t.Fatal("expected first pull to fail on interrupted download")
	}
	partial := filepath.Join(root, "resume-docs", "content.tmp", "data/guide.jsonl"+modelpull.IncompleteExt)
	if fi, statErr := os.Stat(partial); statErr != nil || fi.Size() == 0 {
		t.Fatalf("expected partial download to be retained, err=%v", statErr)
	}
	if _, err := os.Stat(filepath.Join(root, "resume-docs", "manifest.json")); !os.IsNotExist(err) {
		t.Fatal("manifest.json must not be written for a failed pull")
	}

	result, err := adapter.Pull(context.Background(), req)
	if err != nil {
		t.Fatalf("resume pull: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(root, "resume-docs", "content", "data/guide.jsonl"))
	if err != nil {
		t.Fatalf("read resumed content: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("resumed content mismatch (len=%d)", len(got))
	}
	if result.TotalBytes != int64(len(payload)) {
		t.Fatalf("total bytes: got %d", result.TotalBytes)
	}
}

func TestAdapterPull_RejectsNonHFRegistry(t *testing.T) {
	oci := models.NewRegistryBuilder().SetID(1).SetURL("docker.io").SetType(models.RegistryTypeOCI).Build()
	_, err := (&Adapter{}).Pull(context.Background(), modelpull.Request{
		Name:       "x",
		Repo:       "org/repo",
		Registry:   oci,
		ModelsRoot: t.TempDir(),
	})
	if err == nil || !strings.Contains(err.Error(), "cannot pull Hugging Face") {
		t.Fatalf("expected oci registry rejection, got %v", err)
	}
}

func readOnDisk(t *testing.T, root, name string) modelpull.OnDiskManifest {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(root, name, "manifest.json"))
	if err != nil {
		t.Fatalf("read manifest.json: %v", err)
	}
	var onDisk modelpull.OnDiskManifest
	if err := json.Unmarshal(raw, &onDisk); err != nil {
		t.Fatalf("decode manifest.json: %v", err)
	}
	return onDisk
}
