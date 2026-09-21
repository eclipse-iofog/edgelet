package runtimeapi

import (
	"bytes"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/eclipse-iofog/edgelet/internal/knowledgemanager"
	"github.com/eclipse-iofog/edgelet/internal/modelpull"
	"github.com/eclipse-iofog/edgelet/internal/models"
	"gopkg.in/yaml.v3"
)

// SetKnowledgeManager replaces the Knowledge manager used by deploy and Knowledge routes (tests).
func (f *Facade) SetKnowledgeManager(m *knowledgemanager.Manager) {
	if f == nil {
		return
	}
	f.knowledgeMu.Lock()
	defer f.knowledgeMu.Unlock()
	f.knowledge = m
}

func (f *Facade) knowledgeManager() *knowledgemanager.Manager {
	f.knowledgeMu.Lock()
	defer f.knowledgeMu.Unlock()
	disk, threshold := f.liveDiskPolicy()
	if f.knowledge == nil {
		f.knowledge = knowledgemanager.New(f.db, modelpull.KnowledgeRoot(disk))
	}
	f.knowledge.SetLiveConfig(f.cfg)
	f.knowledge.SetDiskPolicy(disk, threshold, nil)
	return f.knowledge
}

// ParseAndValidateLocalKnowledgeManifests decodes one or more Knowledge documents and validates each.
func (f *Facade) ParseAndValidateLocalKnowledgeManifests(manifest string) ([]*models.LocalKnowledgeManifest, error) {
	docs, err := decodeLocalKnowledgeManifests(manifest)
	if err != nil {
		return nil, err
	}
	for _, doc := range docs {
		if err := f.validateKnowledgeDocument(doc); err != nil {
			return nil, err
		}
	}
	return docs, nil
}

// ApplyLocalKnowledgeManifests upserts each Knowledge document by metadata.name.
// Persist is synchronous. Artifact download is started by the apply handler
// (or POST /v1/knowledge:pull) after every document is written.
func (f *Facade) ApplyLocalKnowledgeManifests(manifest string, dryRun bool) ([]*models.LocalKnowledge, error) {
	if err := knowledgemanager.RefuseLocalKnowledgeApply(); err != nil {
		return nil, err
	}
	docs, err := f.ParseAndValidateLocalKnowledgeManifests(manifest)
	if err != nil {
		return nil, err
	}
	applied := make([]*models.LocalKnowledge, 0, len(docs))
	for _, doc := range docs {
		row := doc.ToLocalKnowledge()
		if dryRun {
			applied = append(applied, row)
			continue
		}
		saved, applyErr := f.knowledgeManager().ApplyManifest(doc)
		if applyErr != nil {
			return nil, applyErr
		}
		applied = append(applied, saved)
	}
	return applied, nil
}

// UpsertLocalKnowledgeSpec upserts one Knowledge from pull-request fields using the
// same validation as deploy apply.
func (f *Facade) UpsertLocalKnowledgeSpec(name, repo, revision string, registryID int, files []string, format string) (*models.LocalKnowledge, error) {
	doc := &models.LocalKnowledgeManifest{
		APIVersion: "edgelet.iofog.org/v1",
		Kind:       "Knowledge",
		Spec: models.LocalKnowledgeSpec{
			Repo:     strings.TrimSpace(repo),
			Revision: strings.TrimSpace(revision),
			Registry: registryID,
			Files:    files,
			Format:   strings.TrimSpace(format),
		},
	}
	doc.Metadata.Name = strings.TrimSpace(name)
	raw, err := yaml.Marshal(doc)
	if err != nil {
		return nil, err
	}
	rows, err := f.ApplyLocalKnowledgeManifests(string(raw), false)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 || rows[0] == nil {
		return nil, errors.New("knowledge upsert returned no row")
	}
	return rows[0], nil
}

func (f *Facade) validateKnowledgeDocument(doc *models.LocalKnowledgeManifest) error {
	if doc == nil {
		return errors.New("manifest is nil")
	}
	if err := doc.Validate(); err != nil {
		return err
	}
	reg, err := f.db.LookupRegistry(doc.Spec.Registry)
	if err != nil || reg == nil {
		if errors.Is(err, sql.ErrNoRows) || reg == nil {
			return fmt.Errorf("registry %d not found", doc.Spec.Registry)
		}
		return err
	}
	return doc.ValidateWithRegistry(reg)
}

func decodeLocalKnowledgeManifests(manifest string) ([]*models.LocalKnowledgeManifest, error) {
	dec := yaml.NewDecoder(bytes.NewReader([]byte(manifest)))
	dec.KnownFields(true)
	var docs []*models.LocalKnowledgeManifest
	for {
		doc := &models.LocalKnowledgeManifest{}
		err := dec.Decode(doc)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("invalid knowledge manifest YAML: %w", err)
		}
		if strings.TrimSpace(doc.APIVersion) == "" && strings.TrimSpace(doc.Kind) == "" && strings.TrimSpace(doc.Metadata.Name) == "" {
			continue
		}
		docs = append(docs, doc)
	}
	if len(docs) == 0 {
		return nil, errors.New("knowledge manifest is empty")
	}
	return docs, nil
}

// ListKnowledge returns local Knowledge rows as API maps.
func (f *Facade) ListKnowledge() ([]map[string]any, error) {
	rows, err := f.db.ListLocalKnowledge()
	if err != nil {
		return nil, err
	}
	items := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		items = append(items, localKnowledgeToAPI(row))
	}
	return items, nil
}

// GetKnowledge returns one local Knowledge by metadata.name.
func (f *Facade) GetKnowledge(name string) (map[string]any, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, errors.New("knowledge name is required")
	}
	row, err := f.db.GetLocalKnowledge(name)
	if err != nil {
		return nil, err
	}
	item := localKnowledgeToAPI(row)
	if strings.EqualFold(strings.TrimSpace(row.Source), models.KnowledgeSourceManaged) {
		if ck, lookupErr := f.db.GetControllerKnowledgeByName(name); lookupErr == nil && ck != nil {
			if uuid := strings.TrimSpace(ck.UUID); uuid != "" {
				item["uuid"] = uuid
			}
		}
	}
	if n, countErr := f.db.CountKnowledgeRefs(name); countErr == nil {
		item["bindRefCount"] = n
	}
	return item, nil
}

// RemoveKnowledge deletes one local Knowledge and its on-disk artifacts.
func (f *Facade) RemoveKnowledge(name string) error {
	return f.knowledgeManager().Remove(name)
}

// StartKnowledgePull begins an async artifact pull for a deployed Knowledge.
// Download is detached from the HTTP request so a 202 return cannot cancel it.
func (f *Facade) StartKnowledgePull(name string) (*knowledgemanager.PullOperation, error) {
	return f.knowledgeManager().StartPull(name)
}

// GetKnowledgePull returns one async Knowledge pull operation.
func (f *Facade) GetKnowledgePull(operationID string) (*knowledgemanager.PullOperation, bool) {
	return f.knowledgeManager().GetPull(operationID)
}

// PruneKnowledge removes unreferenced Knowledge artifacts.
func (f *Facade) PruneKnowledge(mode string) (*knowledgemanager.PruneReport, error) {
	return f.knowledgeManager().Prune(mode)
}

func localKnowledgeToAPI(k *models.LocalKnowledge) map[string]any {
	if k == nil {
		return map[string]any{}
	}
	source := strings.TrimSpace(k.Source)
	if source == "" {
		source = models.KnowledgeSourceLocal
	}
	item := map[string]any{
		"name":             k.Name,
		"source":           source,
		"repo":             k.Repo,
		"revision":         k.Revision,
		"registryId":       k.RegistryID,
		"files":            k.Files(),
		"format":           k.Format,
		"state":            k.State,
		"generation":       k.Generation,
		"revisionFloating": k.RevisionFloating,
		"totalBytes":       k.TotalBytes,
	}
	if strings.TrimSpace(k.LastError) != "" {
		item["lastError"] = k.LastError
	}
	if strings.TrimSpace(k.ResolvedRevision) != "" {
		item["resolvedRevision"] = k.ResolvedRevision
	}
	if strings.TrimSpace(k.Digest) != "" {
		item["digest"] = k.Digest
	}
	return item
}
