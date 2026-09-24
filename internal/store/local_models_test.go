package store

import (
	"database/sql"
	"errors"
	"strings"
	"testing"

	"github.com/eclipse-iofog/edgelet/internal/models"
)

func TestLocalRegistryUpsertBySpecID(t *testing.T) {
	db := openFreshStoreDB(t)

	reg := models.NewRegistryBuilder().
		SetID(5).
		SetURL("quay.io").
		SetIsPublic(false).
		SetUserName("john").
		SetPassword("s3cr3t").
		SetType(models.RegistryTypeOCI).
		Build()
	if err := db.UpsertLocalRegistry(reg); err != nil {
		t.Fatalf("upsert id 5: %v", err)
	}

	got, err := db.GetLocalRegistry(5)
	if err != nil {
		t.Fatalf("get id 5: %v", err)
	}
	if got.URL != "quay.io" || got.Type != models.RegistryTypeOCI || got.UserName != "john" {
		t.Fatalf("unexpected registry: %+v", got)
	}

	update := models.NewRegistryBuilder().
		SetID(5).
		SetURL("quay.io").
		SetIsPublic(false).
		SetUserName("john").
		SetPassword("rotated").
		SetType(models.RegistryTypeOCI).
		SetCAB64("Y2E=").
		SetInsecure(true).
		Build()
	if err := db.UpsertLocalRegistry(update); err != nil {
		t.Fatalf("upsert same id same identity: %v", err)
	}
	got, err = db.GetLocalRegistry(5)
	if err != nil {
		t.Fatalf("get after update: %v", err)
	}
	if got.Password != "rotated" || got.CAB64 != "Y2E=" || !got.Insecure {
		t.Fatalf("expected in-place credential/tls update, got %+v", got)
	}

	collide := models.NewRegistryBuilder().
		SetID(5).
		SetURL("ghcr.io").
		SetType(models.RegistryTypeOCI).
		Build()
	err = db.UpsertLocalRegistry(collide)
	if err == nil || !strings.Contains(err.Error(), "collision") {
		t.Fatalf("expected collision on different url, got: %v", err)
	}
}

func TestNextLocalRegistryID(t *testing.T) {
	db := openFreshStoreDB(t)
	if err := db.EnsureDefaultLocalRegistries(); err != nil {
		t.Fatalf("defaults: %v", err)
	}
	id, err := db.NextLocalRegistryID()
	if err != nil {
		t.Fatalf("next id: %v", err)
	}
	if id != 4 {
		t.Fatalf("expected next id 4 after built-in registries, got %d", id)
	}

	reg := models.NewRegistry(9, "registry.example.com", true, "", "", "")
	if err := db.UpsertLocalRegistry(reg); err != nil {
		t.Fatalf("upsert 9: %v", err)
	}
	id, err = db.NextLocalRegistryID()
	if err != nil {
		t.Fatalf("next id after 9: %v", err)
	}
	if id != 10 {
		t.Fatalf("expected next id 10, got %d", id)
	}
}

func TestControllerRegistryScanIncludesTypeCAInsecure(t *testing.T) {
	db := openFreshStoreDB(t)

	hf := models.NewRegistryBuilder().
		SetID(5).
		SetURL("https://huggingface.co").
		SetIsPublic(false).
		SetPassword("hf_token").
		SetType(models.RegistryTypeHF).
		SetInsecure(true).
		Build()
	if err := db.SaveControllerRegistries([]*models.Registry{hf}); err != nil {
		t.Fatalf("save controller registries: %v", err)
	}
	got, err := db.LoadControllerRegistries()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 controller registry, got %d", len(got))
	}
	if got[0].Type != models.RegistryTypeHF || !got[0].Insecure || got[0].URL != "https://huggingface.co" {
		t.Fatalf("unexpected controller registry: %+v", got[0])
	}
}

func TestLocalModelCRUD(t *testing.T) {
	db := openFreshStoreDB(t)

	row := &models.LocalModel{
		Name:         "llama-2-7b-q2k",
		Repo:         "second-state/Llama-2-7B-Chat-GGUF",
		Revision:     "064fe43ea8c1e1f93477ef4a170bdc2b244ef02c",
		RegistryID:   5,
		Format:       models.ModelFormatGGUF,
		ManifestYAML: "kind: Model",
		ManifestPath: "/var/lib/edgelet/models/llama-2-7b-q2k/manifest.json",
		ContentPath:  "/var/lib/edgelet/models/llama-2-7b-q2k/content",
	}
	row.SetFiles([]string{"llama-2-7b-chat.Q5_K_M.gguf"})
	if err := db.UpsertLocalModel(row); err != nil {
		t.Fatalf("upsert model: %v", err)
	}

	got, err := db.GetLocalModel("llama-2-7b-q2k")
	if err != nil {
		t.Fatalf("get model: %v", err)
	}
	if got.Repo != row.Repo || got.RegistryID != 5 || got.State != models.ModelStatePending {
		t.Fatalf("unexpected model: %+v", got)
	}
	if got.Source != models.ModelSourceLocal {
		t.Fatalf("expected default source local, got %q", got.Source)
	}
	if got.Generation != 1 {
		t.Fatalf("expected generation 1, got %d", got.Generation)
	}
	files := got.Files()
	if len(files) != 1 || files[0] != "llama-2-7b-chat.Q5_K_M.gguf" {
		t.Fatalf("unexpected files: %v", files)
	}

	got.Revision = "main"
	got.Generation = 2
	if err := db.UpsertLocalModel(got); err != nil {
		t.Fatalf("upsert generation bump: %v", err)
	}
	updated, err := db.GetLocalModel("llama-2-7b-q2k")
	if err != nil {
		t.Fatalf("get after bump: %v", err)
	}
	if updated.Generation != 2 || updated.Revision != "main" {
		t.Fatalf("expected generation/revision update, got %+v", updated)
	}

	list, err := db.ListLocalModels()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("expected 1 model, got %d", len(list))
	}

	if err := db.DeleteLocalModel("llama-2-7b-q2k"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := db.GetLocalModel("llama-2-7b-q2k"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("expected ErrNoRows after delete, got %v", err)
	}
}

func TestControllerModelReplaceAll(t *testing.T) {
	db := openFreshStoreDB(t)

	first := &models.ControllerModel{
		UUID:       "3f2c8a1e-2b64-4c0d-9f11-0a1b2c3d4e5f",
		Name:       "llama-2-7b-q2k",
		Repo:       "second-state/Llama-2-7B-Chat-GGUF",
		Revision:   "064fe43ea8c1e1f93477ef4a170bdc2b244ef02c",
		RegistryID: 5,
		Format:     models.ModelFormatGGUF,
	}
	first.SetFiles([]string{"llama-2-7b-chat.Q5_K_M.gguf"})
	if err := db.SaveControllerModels([]*models.ControllerModel{first}); err != nil {
		t.Fatalf("save controller models: %v", err)
	}
	got, err := db.LoadControllerModels()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(got) != 1 || got[0].Name != first.Name || got[0].RegistryID != 5 {
		t.Fatalf("unexpected controller models: %+v", got)
	}
	if got[0].UUID != first.UUID {
		t.Fatalf("expected uuid persisted, got %+v", got[0])
	}

	replacement := &models.ControllerModel{
		UUID:       "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee",
		Name:       "gemma3",
		Repo:       "ai/gemma3",
		RegistryID: 1,
	}
	if err := db.SaveControllerModels([]*models.ControllerModel{replacement}); err != nil {
		t.Fatalf("replace controller models: %v", err)
	}
	got, err = db.LoadControllerModels()
	if err != nil {
		t.Fatalf("load after replace: %v", err)
	}
	if len(got) != 1 || got[0].Name != "gemma3" || got[0].UUID != replacement.UUID {
		t.Fatalf("expected replace-all, got %+v", got)
	}

	if err := db.ClearControllerModels(); err != nil {
		t.Fatalf("clear: %v", err)
	}
	got, err = db.LoadControllerModels()
	if err != nil {
		t.Fatalf("load after clear: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected empty controller models, got %d", len(got))
	}
}

func TestListReferencedModelNames_UnionsControllerAndRefs(t *testing.T) {
	db := openFreshStoreDB(t)
	row := &models.LocalModel{
		Name:       "unbound-local",
		Repo:       "org/repo",
		RegistryID: 1,
		State:      models.ModelStateReady,
	}
	row.NormalizeDefaults()
	if err := db.UpsertLocalModel(row); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if err := db.SaveControllerModels([]*models.ControllerModel{{
		UUID:       "3f2c8a1e-2b64-4c0d-9f11-0a1b2c3d4e5f",
		Name:       "fleet-model",
		Repo:       "org/fleet",
		RegistryID: 5,
	}}); err != nil {
		t.Fatalf("save controller model: %v", err)
	}
	if _, err := db.Conn().Exec(
		`INSERT INTO model_refs (model_name, kind, ref_id) VALUES ('bound-only', 'workload', 'ms-1')`,
	); err != nil {
		t.Fatalf("insert ref: %v", err)
	}
	names, err := db.ListReferencedModelNames()
	if err != nil {
		t.Fatalf("list refs: %v", err)
	}
	seen := map[string]bool{}
	for _, name := range names {
		seen[name] = true
	}
	if !seen["fleet-model"] || !seen["bound-only"] {
		t.Fatalf("expected controller model and explicit ref, got %v", names)
	}
	if seen["unbound-local"] {
		t.Fatalf("unbound local row must not be kept just because it exists, got %v", names)
	}

	withoutRefs, err := db.ListPruneKeepModelNames(false)
	if err != nil {
		t.Fatalf("list keep without refs: %v", err)
	}
	keepSeen := map[string]bool{}
	for _, name := range withoutRefs {
		keepSeen[name] = true
	}
	if !keepSeen["fleet-model"] {
		t.Fatalf("expected fleet model kept without refs, got %v", withoutRefs)
	}
	if keepSeen["bound-only"] {
		t.Fatalf("workload binds must be omitted when includeWorkloadRefs is false, got %v", withoutRefs)
	}
}

func TestReplaceWorkloadModelRefs(t *testing.T) {
	db := openFreshStoreDB(t)
	if err := db.ReplaceWorkloadModelRefs("ms-1", []string{"a", "b", "a"}); err != nil {
		t.Fatalf("replace: %v", err)
	}
	n, err := db.CountModelRefs("a")
	if err != nil || n != 1 {
		t.Fatalf("count a: n=%d err=%v", n, err)
	}
	if err := db.ReplaceWorkloadModelRefs("ms-1", []string{"b"}); err != nil {
		t.Fatalf("replace drop a: %v", err)
	}
	n, err = db.CountModelRefs("a")
	if err != nil || n != 0 {
		t.Fatalf("expected a cleared, n=%d err=%v", n, err)
	}
	if err := db.DeleteWorkloadModelRefs("ms-1"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	n, err = db.CountModelRefs("b")
	if err != nil || n != 0 {
		t.Fatalf("expected b cleared, n=%d err=%v", n, err)
	}
}

func TestControllerModelUUIDRequiredAndUniqueName(t *testing.T) {
	db := openFreshStoreDB(t)

	err := db.SaveControllerModels([]*models.ControllerModel{{
		Name:       "missing-uuid",
		Repo:       "org/repo",
		RegistryID: 1,
	}})
	if err == nil || !strings.Contains(err.Error(), "uuid is required") {
		t.Fatalf("expected uuid required, got %v", err)
	}

	first := &models.ControllerModel{
		UUID:       "uuid-1",
		Name:       "shared-name",
		Repo:       "org/a",
		RegistryID: 1,
	}
	dupName := &models.ControllerModel{
		UUID:       "uuid-2",
		Name:       "shared-name",
		Repo:       "org/b",
		RegistryID: 1,
	}
	err = db.SaveControllerModels([]*models.ControllerModel{first, dupName})
	if err == nil || !strings.Contains(err.Error(), "duplicate controller model name") {
		t.Fatalf("expected unique name error, got %v", err)
	}

	dupUUID := &models.ControllerModel{
		UUID:       "uuid-1",
		Name:       "other",
		Repo:       "org/c",
		RegistryID: 1,
	}
	err = db.SaveControllerModels([]*models.ControllerModel{first, dupUUID})
	if err == nil || !strings.Contains(err.Error(), "duplicate controller model uuid") {
		t.Fatalf("expected unique uuid error, got %v", err)
	}
}

func TestGetControllerModelByName(t *testing.T) {
	db := openFreshStoreDB(t)
	row := &models.ControllerModel{
		UUID:       "3f2c8a1e-2b64-4c0d-9f11-0a1b2c3d4e5f",
		Name:       "test-model",
		Repo:       "org/repo",
		RegistryID: 5,
	}
	if err := db.SaveControllerModels([]*models.ControllerModel{row}); err != nil {
		t.Fatalf("save: %v", err)
	}
	got, err := db.GetControllerModelByName("test-model")
	if err != nil {
		t.Fatalf("get by name: %v", err)
	}
	if got.UUID != row.UUID || got.Name != row.Name {
		t.Fatalf("unexpected row: %+v", got)
	}
	if _, err := db.GetControllerModelByName("missing"); err == nil {
		t.Fatal("expected missing name to fail")
	}
}

func TestLocalModelSourceRoundTrip(t *testing.T) {
	db := openFreshStoreDB(t)
	row := &models.LocalModel{
		Name:       "fleet-model",
		Source:     models.ModelSourceManaged,
		Repo:       "org/repo",
		RegistryID: 1,
		State:      models.ModelStateReady,
	}
	if err := db.UpsertLocalModel(row); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	got, err := db.GetLocalModel("fleet-model")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Source != models.ModelSourceManaged {
		t.Fatalf("expected managed source, got %q", got.Source)
	}
}

func TestControllerMicroserviceContainerColumnsRoundTrip(t *testing.T) {
	db := openFreshStoreDB(t)
	ms := models.NewMicroservice("ms-json", "nginx:latest")
	ms.Models = &models.ModelCatalog{
		BindPath:    "/models",
		Permissions: models.ModelCatalogPermRO,
		Items:       []models.ModelCatalogItem{{Name: "test-model"}},
	}
	cpus := 2.5
	ms.Cpus = &cpus
	empty := []string{}
	ms.Entrypoint = &empty
	cmds := []string{"--serve"}
	ms.Commands = &cmds
	ms.Args = append(ms.Args, cmds...)
	group := "0"
	ms.RunAsGroup = &group
	ms.ReadOnlyRootFilesystem = true
	ms.Sysctls = map[string]string{"kernel.shm_rmid_forced": "1"}
	ms.Ulimits = map[string]models.Ulimit{"nofile": {Soft: 65536, Hard: 65536}}
	ms.Devices = []models.DeviceMapping{{
		HostPath:      "/dev/null",
		ContainerPath: "/dev/null",
		Permissions:   "rwm",
	}}
	size := int64(64)
	ms.Tmpfs = []models.TmpfsMount{{ContainerPath: "/tmp", Size: &size, Mode: "1777"}}
	wd := "/app"
	ms.WorkingDir = &wd
	ms.SetMemoryReservationMB(int64Ptr(128))
	ms.SetMemorySwapMB(int64Ptr(1024))
	ms.SetShmSizeMB(int64Ptr(64))

	if err := db.SaveControllerMicroservices([]*models.Microservice{ms}); err != nil {
		t.Fatalf("save: %v", err)
	}
	got, err := db.LoadControllerMicroservices()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 microservice, got %d", len(got))
	}
	loaded := got[0]
	if loaded.Models == nil || loaded.Models.BindPath != "/models" || len(loaded.Models.Items) != 1 || loaded.Models.Items[0].Name != "test-model" {
		t.Fatalf("models json mismatch: %+v", loaded.Models)
	}
	if loaded.Cpus == nil || *loaded.Cpus != 2.5 {
		t.Fatalf("cpus mismatch: %+v", loaded.Cpus)
	}
	if loaded.Entrypoint == nil || len(*loaded.Entrypoint) != 0 {
		t.Fatalf("expected explicit empty entrypoint, got %+v", loaded.Entrypoint)
	}
	if loaded.Commands == nil || len(*loaded.Commands) != 1 || (*loaded.Commands)[0] != "--serve" {
		t.Fatalf("commands mismatch: %+v", loaded.Commands)
	}
	if loaded.RunAsGroup == nil || *loaded.RunAsGroup != "0" || !loaded.ReadOnlyRootFilesystem {
		t.Fatal("runAsGroup/readonly mismatch")
	}
	if loaded.Sysctls["kernel.shm_rmid_forced"] != "1" {
		t.Fatalf("sysctls mismatch: %+v", loaded.Sysctls)
	}
	if loaded.Ulimits["nofile"].Soft != 65536 || loaded.Ulimits["nofile"].Hard != 65536 {
		t.Fatalf("ulimits mismatch: %+v", loaded.Ulimits)
	}
	if len(loaded.Devices) != 1 || loaded.Devices[0].HostPath != "/dev/null" {
		t.Fatalf("devices mismatch: %+v", loaded.Devices)
	}
	if len(loaded.Tmpfs) != 1 || loaded.Tmpfs[0].ContainerPath != "/tmp" || loaded.Tmpfs[0].Size == nil || *loaded.Tmpfs[0].Size != 64 {
		t.Fatalf("tmpfs mismatch: %+v", loaded.Tmpfs)
	}
	if loaded.WorkingDir == nil || *loaded.WorkingDir != "/app" {
		t.Fatalf("workingDir mismatch: %+v", loaded.WorkingDir)
	}
	if loaded.GetMemoryReservationMB() == nil || *loaded.GetMemoryReservationMB() != 128 {
		t.Fatal("memoryReservation mismatch")
	}
	if loaded.GetMemorySwapMB() == nil || *loaded.GetMemorySwapMB() != 1024 {
		t.Fatal("memorySwap mismatch")
	}
	if loaded.GetShmSizeMB() == nil || *loaded.GetShmSizeMB() != 64 {
		t.Fatal("shmSize mismatch")
	}
}

func int64Ptr(v int64) *int64 { return &v }
