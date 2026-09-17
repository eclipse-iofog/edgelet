package runtimeapi

import (
	"bytes"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/eclipse-iofog/edgelet/internal/modelmanager"
	"github.com/eclipse-iofog/edgelet/internal/modelpull"
	"github.com/eclipse-iofog/edgelet/internal/models"
	"gopkg.in/yaml.v3"
)

// SetModelManager replaces the model manager used by deploy and model routes (tests).
func (f *Facade) SetModelManager(m *modelmanager.Manager) {
	if f == nil {
		return
	}
	f.modelsMu.Lock()
	defer f.modelsMu.Unlock()
	f.models = m
}

func (f *Facade) modelManager() *modelmanager.Manager {
	f.modelsMu.Lock()
	defer f.modelsMu.Unlock()
	disk, threshold := f.liveDiskPolicy()
	if f.models == nil {
		f.models = modelmanager.New(f.db, modelpull.Root(disk))
	}
	f.models.SetLiveConfig(f.cfg)
	f.models.SetDiskPolicy(disk, threshold, nil)
	return f.models
}

func (f *Facade) liveDiskPolicy() (disk string, threshold int64) {
	threshold = 20
	if f.cfg == nil {
		return "", threshold
	}
	disk = strings.TrimSpace(f.cfg.DiskDirectory)
	threshold = f.cfg.AvailableDiskThreshold
	if threshold < 0 {
		threshold = 0
	}
	return disk, threshold
}

// ParseAndValidateLocalModelManifests decodes one or more Model documents and validates each.
func (f *Facade) ParseAndValidateLocalModelManifests(manifest string) ([]*models.LocalModelManifest, error) {
	docs, err := decodeLocalModelManifests(manifest)
	if err != nil {
		return nil, err
	}
	for _, doc := range docs {
		if err := f.validateModelDocument(doc); err != nil {
			return nil, err
		}
	}
	return docs, nil
}

// ApplyLocalModelManifests upserts each Model document by metadata.name.
// Persist is synchronous. Artifact download is started by the apply handler
// (or POST /v1/models:pull) after every document is written.
func (f *Facade) ApplyLocalModelManifests(manifest string, dryRun bool) ([]*models.LocalModel, error) {
	if err := modelmanager.RefuseLocalModelApply(); err != nil {
		return nil, err
	}
	docs, err := f.ParseAndValidateLocalModelManifests(manifest)
	if err != nil {
		return nil, err
	}
	applied := make([]*models.LocalModel, 0, len(docs))
	for _, doc := range docs {
		row := doc.ToLocalModel()
		if raw, marshalErr := yaml.Marshal(doc); marshalErr == nil {
			row.ManifestYAML = string(raw)
		}
		if dryRun {
			applied = append(applied, row)
			continue
		}
		saved, applyErr := f.modelManager().ApplyManifest(doc)
		if applyErr != nil {
			return nil, applyErr
		}
		if saved != nil && strings.TrimSpace(row.ManifestYAML) != "" {
			saved.ManifestYAML = row.ManifestYAML
			if err := f.db.UpsertLocalModel(saved); err != nil {
				return nil, err
			}
			saved, err = f.db.GetLocalModel(saved.Name)
			if err != nil {
				return nil, err
			}
		}
		applied = append(applied, saved)
	}
	return applied, nil
}

// UpsertLocalModelSpec upserts one Model from pull-request fields using the
// same validation as deploy apply.
func (f *Facade) UpsertLocalModelSpec(name, repo, revision string, registryID int, files []string, format string) (*models.LocalModel, error) {
	doc := &models.LocalModelManifest{
		APIVersion: "edgelet.iofog.org/v1",
		Kind:       "Model",
		Spec: models.LocalModelSpec{
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
	rows, err := f.ApplyLocalModelManifests(string(raw), false)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 || rows[0] == nil {
		return nil, errors.New("model upsert returned no row")
	}
	return rows[0], nil
}

func (f *Facade) validateModelDocument(doc *models.LocalModelManifest) error {
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

func decodeLocalModelManifests(manifest string) ([]*models.LocalModelManifest, error) {
	dec := yaml.NewDecoder(bytes.NewReader([]byte(manifest)))
	dec.KnownFields(true)
	var docs []*models.LocalModelManifest
	for {
		doc := &models.LocalModelManifest{}
		err := dec.Decode(doc)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("invalid model manifest YAML: %w", err)
		}
		if strings.TrimSpace(doc.APIVersion) == "" && strings.TrimSpace(doc.Kind) == "" && strings.TrimSpace(doc.Metadata.Name) == "" {
			continue
		}
		docs = append(docs, doc)
	}
	if len(docs) == 0 {
		return nil, errors.New("model manifest is empty")
	}
	return docs, nil
}

// ListModels returns local Model rows as API maps.
func (f *Facade) ListModels() ([]map[string]any, error) {
	rows, err := f.db.ListLocalModels()
	if err != nil {
		return nil, err
	}
	items := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		items = append(items, localModelToAPI(row))
	}
	return items, nil
}

// GetModel returns one local Model by metadata.name.
func (f *Facade) GetModel(name string) (map[string]any, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, errors.New("model name is required")
	}
	row, err := f.db.GetLocalModel(name)
	if err != nil {
		return nil, err
	}
	item := localModelToAPI(row)
	if strings.EqualFold(strings.TrimSpace(row.Source), models.ModelSourceManaged) {
		if cm, lookupErr := f.db.GetControllerModelByName(name); lookupErr == nil && cm != nil {
			if uuid := strings.TrimSpace(cm.UUID); uuid != "" {
				item["uuid"] = uuid
			}
		}
	}
	if n, countErr := f.db.CountModelRefs(name); countErr == nil {
		item["bindRefCount"] = n
	}
	return item, nil
}

// RemoveModel deletes one local Model and its on-disk artifacts.
func (f *Facade) RemoveModel(name string) error {
	return f.modelManager().Remove(name)
}

// StartModelPull begins an async artifact pull for a deployed Model.
// Download is detached from the HTTP request so a 202 return cannot cancel it.
func (f *Facade) StartModelPull(name string) (*modelmanager.PullOperation, error) {
	return f.modelManager().StartPull(name)
}

// GetModelPull returns one async model pull operation.
func (f *Facade) GetModelPull(operationID string) (*modelmanager.PullOperation, bool) {
	return f.modelManager().GetPull(operationID)
}

// PruneModels removes unreferenced model artifacts.
func (f *Facade) PruneModels(mode string) (*modelmanager.PruneReport, error) {
	return f.modelManager().Prune(mode)
}

func localModelToAPI(m *models.LocalModel) map[string]any {
	if m == nil {
		return map[string]any{}
	}
	source := strings.TrimSpace(m.Source)
	if source == "" {
		source = models.ModelSourceLocal
	}
	item := map[string]any{
		"name":               m.Name,
		"source":             source,
		"repo":               m.Repo,
		"revision":           m.Revision,
		"registryId":         m.RegistryID,
		"files":              m.Files(),
		"format":             m.Format,
		"state":              m.State,
		"generation":         m.Generation,
		"observedGeneration": m.ObservedGeneration,
		"revisionFloating":   m.RevisionFloating,
		"totalBytes":         m.TotalBytes,
	}
	if strings.TrimSpace(m.LastError) != "" {
		item["lastError"] = m.LastError
	}
	if strings.TrimSpace(m.ResolvedRevision) != "" {
		item["resolvedRevision"] = m.ResolvedRevision
	}
	if strings.TrimSpace(m.Digest) != "" {
		item["digest"] = m.Digest
	}
	if strings.TrimSpace(m.ContentPath) != "" {
		item["contentPath"] = m.ContentPath
	}
	if strings.TrimSpace(m.ManifestPath) != "" {
		item["manifestPath"] = m.ManifestPath
	}
	if m.LastTransitionAt > 0 {
		item["lastTransitionAt"] = m.LastTransitionAt
	}
	if m.LastReconcileAt > 0 {
		item["lastReconcileAt"] = m.LastReconcileAt
	}
	if m.PulledAt > 0 {
		item["pulledAt"] = m.PulledAt
	}
	return item
}
