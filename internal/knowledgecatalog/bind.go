package knowledgecatalog

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/eclipse-iofog/edgelet/internal/modelcatalog"
	"github.com/eclipse-iofog/edgelet/internal/modelpull"
	"github.com/eclipse-iofog/edgelet/internal/models"
	"github.com/eclipse-iofog/edgelet/internal/store"
)

var (
	// ErrWaiting means create must not run until named Knowledge is Ready.
	ErrWaiting = modelcatalog.ErrWaiting
	// ErrFailed means a named Knowledge is missing or Failed.
	ErrFailed = modelcatalog.ErrFailed
)

// PrepareResult is the start-gate and projection outcome for one microservice.
type PrepareResult struct {
	Decision     models.CatalogGateDecision
	Message      string
	HostDir      string
	MountChanged bool
}

// LookupFromStore returns Knowledge status from local_knowledge.
func LookupFromStore(db *store.DB) models.KnowledgeStatusLookup {
	return func(name string) (models.KnowledgeStatusInfo, error) {
		if db == nil || db.Conn() == nil {
			return models.KnowledgeStatusInfo{}, errors.New("store is not initialized")
		}
		row, err := db.GetLocalKnowledge(name)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return models.KnowledgeStatusInfo{}, nil
			}
			return models.KnowledgeStatusInfo{}, err
		}
		return models.KnowledgeStatusInfo{
			Source:     row.Source,
			State:      row.State,
			Exists:     true,
			Generation: row.Generation,
			LastError:  row.LastError,
		}, nil
	}
}

// Prepare evaluates the start gate, records knowledge refs, and projects
// Ready content when create is allowed. forStart applies wait/fail; refresh
// keeps an existing tree when Knowledge is not Ready.
func Prepare(diskDirectory string, db *store.DB, ms *models.Microservice, requiredSource string, forStart bool) (*PrepareResult, error) {
	if ms == nil {
		return nil, errors.New("microservice is nil")
	}
	hostDir := HostDir(diskDirectory, ms.MicroserviceUUID)
	if db == nil || db.Conn() == nil {
		return nil, errors.New("store is not initialized")
	}

	if !ms.Knowledge.HasItems() {
		result := &PrepareResult{
			Decision:     models.CatalogGateAllow,
			HostDir:      hostDir,
			MountChanged: mountChangedFromDisk(hostDir, ms.Knowledge),
		}
		if err := db.ReplaceKnowledgeRefs(ms.MicroserviceUUID, nil); err != nil {
			return nil, err
		}
		// Recreate drops the catalog bind. Keep the previous tree until then so a
		// still-running container does not lose the mount mid-cycle.
		if forStart {
			if err := Cleanup(diskDirectory, ms.MicroserviceUUID); err != nil {
				return nil, err
			}
		}
		return result, nil
	}

	lookup := LookupFromStore(db)
	decision, message, err := models.EvaluateKnowledgeCatalogStartGate(ms.Knowledge, requiredSource, lookup)
	if err != nil {
		return nil, err
	}

	refNames, projectItems, err := collectBindItems(diskDirectory, ms.Knowledge, requiredSource, lookup)
	if err != nil {
		return nil, err
	}
	if err := db.ReplaceKnowledgeRefs(ms.MicroserviceUUID, refNames); err != nil {
		return nil, err
	}

	result := &PrepareResult{
		Decision: decision,
		Message:  message,
		HostDir:  hostDir,
	}

	if decision != models.CatalogGateAllow {
		result.MountChanged = mountChangedFromDisk(hostDir, ms.Knowledge)
		// First start waits before creating. A running workload keeps the previous
		// projection until every named item is Ready, then Project swings ..data.
		return result, nil
	}

	proj, err := Project(diskDirectory, ms.MicroserviceUUID, ms.Knowledge, projectItems)
	if err != nil {
		return nil, err
	}
	result.HostDir = proj.HostDir
	result.MountChanged = proj.MountChanged
	return result, nil
}

// Release clears knowledge refs and the on-disk catalog tree.
func Release(diskDirectory string, db *store.DB, msUUID string) error {
	if db != nil && db.Conn() != nil {
		if err := db.ReplaceKnowledgeRefs(msUUID, nil); err != nil {
			return err
		}
	}
	return Cleanup(diskDirectory, msUUID)
}

func collectBindItems(diskDirectory string, catalog *models.KnowledgeCatalog, requiredSource string, lookup models.KnowledgeStatusLookup) ([]string, []ProjectItem, error) {
	want := strings.ToLower(strings.TrimSpace(requiredSource))
	refNames := make([]string, 0, len(catalog.Items))
	projectItems := make([]ProjectItem, 0, len(catalog.Items))
	for _, item := range catalog.Items {
		name := strings.TrimSpace(item.Name)
		if name == "" {
			continue
		}
		info, err := lookup(name)
		if err != nil {
			return nil, nil, err
		}
		if !info.Exists {
			continue
		}
		if strings.ToLower(strings.TrimSpace(info.Source)) != want {
			continue
		}
		refNames = append(refNames, name)
		if strings.TrimSpace(info.State) != models.KnowledgeStateReady {
			continue
		}
		content := strings.TrimSpace(info.ContentPath)
		if content == "" {
			content = modelpull.ContentDir(modelpull.KnowledgeRoot(diskDirectory), name)
		}
		projectItems = append(projectItems, ProjectItem{
			Name:        name,
			ContentPath: content,
			Generation:  info.Generation,
		})
	}
	return refNames, projectItems, nil
}

// WrapDecision returns a sentinel error for wait/fail start-gate outcomes.
func WrapDecision(res *PrepareResult) error {
	if res == nil {
		return nil
	}
	switch res.Decision {
	case models.CatalogGateWait:
		if strings.TrimSpace(res.Message) == "" || res.Message == models.KnowledgeCatalogWaitingMessage {
			return ErrWaiting
		}
		return fmt.Errorf("%w: %s", ErrWaiting, res.Message)
	case models.CatalogGateFail:
		if strings.TrimSpace(res.Message) == "" {
			return ErrFailed
		}
		return fmt.Errorf("%w: %s", ErrFailed, res.Message)
	default:
		return nil
	}
}
