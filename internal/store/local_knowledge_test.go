package store

import (
	"database/sql"
	"errors"
	"strings"
	"testing"

	"github.com/eclipse-iofog/edgelet/internal/models"
)

func TestLocalKnowledgeCRUD(t *testing.T) {
	db := openFreshStoreDB(t)

	row := &models.LocalKnowledge{
		Name:       "product-docs",
		Repo:       "acme/product-manuals",
		Revision:   "9f3c111122223333444455556666777788889999",
		RegistryID: 3,
		Format:     models.KnowledgeFormatJSONL,
	}
	row.SetFiles([]string{"data/guide.jsonl"})
	if err := db.UpsertLocalKnowledge(row); err != nil {
		t.Fatalf("upsert knowledge: %v", err)
	}

	got, err := db.GetLocalKnowledge("product-docs")
	if err != nil {
		t.Fatalf("get knowledge: %v", err)
	}
	if got.Repo != row.Repo || got.RegistryID != 3 || got.State != models.KnowledgeStatePending {
		t.Fatalf("unexpected knowledge: %+v", got)
	}
	if got.Source != models.KnowledgeSourceLocal {
		t.Fatalf("expected default source local, got %q", got.Source)
	}
	if got.Generation != 1 {
		t.Fatalf("expected generation 1, got %d", got.Generation)
	}
	files := got.Files()
	if len(files) != 1 || files[0] != "data/guide.jsonl" {
		t.Fatalf("unexpected files: %v", files)
	}

	got.Revision = "main"
	got.Generation = 2
	if err := db.UpsertLocalKnowledge(got); err != nil {
		t.Fatalf("upsert generation bump: %v", err)
	}
	updated, err := db.GetLocalKnowledge("product-docs")
	if err != nil {
		t.Fatalf("get after bump: %v", err)
	}
	if updated.Generation != 2 || updated.Revision != "main" {
		t.Fatalf("expected generation/revision update, got %+v", updated)
	}

	list, err := db.ListLocalKnowledge()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("expected 1 knowledge, got %d", len(list))
	}

	if err := db.DeleteLocalKnowledge("product-docs"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := db.GetLocalKnowledge("product-docs"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("expected ErrNoRows after delete, got %v", err)
	}
}

func TestLocalKnowledgeSourceDefaultAndManaged(t *testing.T) {
	db := openFreshStoreDB(t)
	row := &models.LocalKnowledge{
		Name:       "fleet-docs",
		Source:     models.KnowledgeSourceManaged,
		Repo:       "acme/docs",
		RegistryID: 1,
		State:      models.KnowledgeStateReady,
	}
	if err := db.UpsertLocalKnowledge(row); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	got, err := db.GetLocalKnowledge("fleet-docs")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Source != models.KnowledgeSourceManaged {
		t.Fatalf("expected managed source, got %q", got.Source)
	}
}

func TestControllerKnowledgeReplaceAllAndUpsert(t *testing.T) {
	db := openFreshStoreDB(t)

	first := &models.ControllerKnowledge{
		UUID:       "3f2c8a1e-2b64-4c0d-9f11-0a1b2c3d4e5f",
		Name:       "product-docs",
		Repo:       "acme/product-manuals",
		Revision:   "9f3c111122223333444455556666777788889999",
		RegistryID: 3,
		Format:     models.KnowledgeFormatJSONL,
	}
	first.SetFiles([]string{"data/guide.jsonl"})
	if err := db.SaveControllerKnowledge([]*models.ControllerKnowledge{first}); err != nil {
		t.Fatalf("save controller knowledge: %v", err)
	}
	got, err := db.LoadControllerKnowledge()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(got) != 1 || got[0].Name != first.Name || got[0].UUID != first.UUID {
		t.Fatalf("unexpected controller knowledge: %+v", got)
	}

	update := &models.ControllerKnowledge{
		UUID:       first.UUID,
		Name:       "product-docs",
		Repo:       "acme/product-manuals",
		Revision:   "main",
		RegistryID: 3,
		Format:     models.KnowledgeFormatPDF,
		State:      models.KnowledgeStateReady,
	}
	if err := db.UpsertControllerKnowledge(update); err != nil {
		t.Fatalf("upsert by uuid: %v", err)
	}
	byUUID, err := db.GetControllerKnowledgeByUUID(first.UUID)
	if err != nil {
		t.Fatalf("get by uuid: %v", err)
	}
	if byUUID.Revision != "main" || byUUID.Format != models.KnowledgeFormatPDF {
		t.Fatalf("expected upsert update, got %+v", byUUID)
	}

	replacement := &models.ControllerKnowledge{
		UUID:       "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee",
		Name:       "wiki-faiss",
		Repo:       "acme/wiki",
		RegistryID: 1,
	}
	if err := db.SaveControllerKnowledge([]*models.ControllerKnowledge{replacement}); err != nil {
		t.Fatalf("replace controller knowledge: %v", err)
	}
	got, err = db.LoadControllerKnowledge()
	if err != nil {
		t.Fatalf("load after replace: %v", err)
	}
	if len(got) != 1 || got[0].Name != "wiki-faiss" || got[0].UUID != replacement.UUID {
		t.Fatalf("expected replace-all, got %+v", got)
	}

	if err := db.ClearControllerKnowledge(); err != nil {
		t.Fatalf("clear: %v", err)
	}
	got, err = db.LoadControllerKnowledge()
	if err != nil {
		t.Fatalf("load after clear: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected empty controller knowledge, got %d", len(got))
	}
}

func TestControllerKnowledgeUUIDRequiredAndUniqueName(t *testing.T) {
	db := openFreshStoreDB(t)

	err := db.SaveControllerKnowledge([]*models.ControllerKnowledge{{
		Name:       "missing-uuid",
		Repo:       "org/repo",
		RegistryID: 1,
	}})
	if err == nil || !strings.Contains(err.Error(), "uuid is required") {
		t.Fatalf("expected uuid required, got %v", err)
	}

	first := &models.ControllerKnowledge{
		UUID:       "uuid-1",
		Name:       "shared-name",
		Repo:       "org/a",
		RegistryID: 1,
	}
	dupName := &models.ControllerKnowledge{
		UUID:       "uuid-2",
		Name:       "shared-name",
		Repo:       "org/b",
		RegistryID: 1,
	}
	err = db.SaveControllerKnowledge([]*models.ControllerKnowledge{first, dupName})
	if err == nil || !strings.Contains(err.Error(), "duplicate controller knowledge name") {
		t.Fatalf("expected unique name error, got %v", err)
	}
}

func TestKnowledgeRefsInsertListClear(t *testing.T) {
	db := openFreshStoreDB(t)
	if err := db.InsertKnowledgeRefs("ms-1", []string{"product-docs", "wiki-faiss", "product-docs"}); err != nil {
		t.Fatalf("insert: %v", err)
	}
	names, err := db.ListKnowledgeRefs("ms-1")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(names) != 2 || names[0] != "product-docs" || names[1] != "wiki-faiss" {
		t.Fatalf("unexpected refs: %v", names)
	}
	if err := db.ClearKnowledgeRefs("ms-1"); err != nil {
		t.Fatalf("clear: %v", err)
	}
	names, err = db.ListKnowledgeRefs("ms-1")
	if err != nil {
		t.Fatalf("list after clear: %v", err)
	}
	if len(names) != 0 {
		t.Fatalf("expected empty refs, got %v", names)
	}
}

func TestReplaceKnowledgeRefs(t *testing.T) {
	db := openFreshStoreDB(t)
	if err := db.ReplaceKnowledgeRefs("ms-1", []string{"a", "b", "a"}); err != nil {
		t.Fatalf("replace: %v", err)
	}
	n, err := db.CountKnowledgeRefs("a")
	if err != nil || n != 1 {
		t.Fatalf("count a after first replace: n=%d err=%v", n, err)
	}
	if err := db.ReplaceKnowledgeRefs("ms-1", []string{"b"}); err != nil {
		t.Fatalf("replace drop a: %v", err)
	}
	n, err = db.CountKnowledgeRefs("a")
	if err != nil || n != 0 {
		t.Fatalf("count a after drop: n=%d err=%v", n, err)
	}
	n, err = db.CountKnowledgeRefs("b")
	if err != nil || n != 1 {
		t.Fatalf("count b: n=%d err=%v", n, err)
	}
	if err := db.ReplaceKnowledgeRefs("ms-1", nil); err != nil {
		t.Fatalf("replace empty: %v", err)
	}
	n, err = db.CountKnowledgeRefs("b")
	if err != nil || n != 0 {
		t.Fatalf("count b after clear: n=%d err=%v", n, err)
	}
}

func TestModelAndKnowledgeSameNameCoexist(t *testing.T) {
	db := openFreshStoreDB(t)

	model := &models.LocalModel{
		Name:       "foo",
		Repo:       "org/weights",
		RegistryID: 1,
		Format:     models.ModelFormatGGUF,
		State:      models.ModelStateReady,
	}
	if err := db.UpsertLocalModel(model); err != nil {
		t.Fatalf("upsert model: %v", err)
	}
	knowledge := &models.LocalKnowledge{
		Name:       "foo",
		Repo:       "org/docs",
		RegistryID: 1,
		Format:     models.KnowledgeFormatMarkdown,
		State:      models.KnowledgeStateReady,
	}
	if err := db.UpsertLocalKnowledge(knowledge); err != nil {
		t.Fatalf("upsert knowledge: %v", err)
	}

	gotModel, err := db.GetLocalModel("foo")
	if err != nil {
		t.Fatalf("get model: %v", err)
	}
	gotKnowledge, err := db.GetLocalKnowledge("foo")
	if err != nil {
		t.Fatalf("get knowledge: %v", err)
	}
	if gotModel.Repo != "org/weights" || gotKnowledge.Repo != "org/docs" {
		t.Fatalf("expected separate rows, model=%+v knowledge=%+v", gotModel, gotKnowledge)
	}

	if err := db.SaveControllerModels([]*models.ControllerModel{{
		UUID:       "model-uuid",
		Name:       "foo",
		Repo:       "org/weights",
		RegistryID: 1,
	}}); err != nil {
		t.Fatalf("save controller model: %v", err)
	}
	if err := db.SaveControllerKnowledge([]*models.ControllerKnowledge{{
		UUID:       "knowledge-uuid",
		Name:       "foo",
		Repo:       "org/docs",
		RegistryID: 1,
	}}); err != nil {
		t.Fatalf("save controller knowledge: %v", err)
	}
	cm, err := db.GetControllerModelByName("foo")
	if err != nil {
		t.Fatalf("get controller model: %v", err)
	}
	ck, err := db.GetControllerKnowledgeByName("foo")
	if err != nil {
		t.Fatalf("get controller knowledge: %v", err)
	}
	if cm.UUID != "model-uuid" || ck.UUID != "knowledge-uuid" {
		t.Fatalf("expected separate controller rows, model=%+v knowledge=%+v", cm, ck)
	}
}

func TestControllerMicroserviceKnowledgeColumnRoundTrip(t *testing.T) {
	db := openFreshStoreDB(t)
	ms := models.NewMicroservice("ms-knowledge", "nginx:latest")
	ms.Knowledge = &models.KnowledgeCatalog{
		BindPath:    "/knowledge",
		Permissions: models.KnowledgeCatalogPermRO,
		Items:       []models.KnowledgeCatalogItem{{Name: "product-docs"}},
	}
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
	if loaded.Knowledge == nil || loaded.Knowledge.BindPath != "/knowledge" ||
		len(loaded.Knowledge.Items) != 1 || loaded.Knowledge.Items[0].Name != "product-docs" {
		t.Fatalf("knowledge json mismatch: %+v", loaded.Knowledge)
	}
}
