package runtimeapi

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/eclipse-iofog/edgelet/internal/models"
	"github.com/eclipse-iofog/edgelet/internal/processmanager"
	"github.com/eclipse-iofog/edgelet/internal/store"
	"github.com/eclipse-iofog/edgelet/internal/volumereclaim"
)

// ErrSystemPruneVolumes is returned when system prune volumes is asked to
// destroy persistent VOLUME data. Callers should use volume prune instead.
var ErrSystemPruneVolumes = errors.New("system prune volumes does not destroy persistent VOLUME data; use edgelet volume prune")

// VolumePruneRequest controls explicit orphan prune of persistent VOLUME claims.
type VolumePruneRequest struct {
	Orphans bool
	Yes     bool
	Force   bool
	DryRun  bool
}

type volumeClaim struct {
	Name           string
	Scope          string
	Kind           string
	UUID           string
	Consumers      []string
	HostPath       string
	Desired        bool
	UnreferencedAt *int64
}

func (f *Facade) volumeReclaimer() *volumereclaim.Reclaimer {
	disk := ""
	if f != nil && f.cfg != nil {
		disk = strings.TrimSpace(f.cfg.DiskDirectory)
	}
	db := (*store.DB)(nil)
	if f != nil {
		db = f.db
	}
	r := volumereclaim.New(db, disk)
	r.SetUsageProvider(runtimeVolumeUsage{diskDirectory: disk})
	return r
}

// ListVolumeClaims returns one row per persistent VOLUME claim.
// Private claims set uuid; shared claims list every local and controller consumer.
func (f *Facade) ListVolumeClaims() ([]map[string]any, error) {
	if f == nil || f.db == nil {
		return nil, errors.New("runtime facade is not initialized")
	}
	rows, err := f.db.ListPersistentVolumes()
	if err != nil {
		return nil, err
	}
	keep, err := f.db.PersistentVolumeKeepSet(nil, nil)
	if err != nil {
		return nil, err
	}
	claims := groupVolumeClaims(rows, keep)
	out := make([]map[string]any, 0, len(claims))
	for _, claim := range claims {
		out = append(out, volumeClaimToAPI(claim))
	}
	return out, nil
}

// GetSharedVolumeClaim inspects one shared persistent VOLUME claim by name.
func (f *Facade) GetSharedVolumeClaim(name string) (map[string]any, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, errors.New("volume name is required")
	}
	claims, err := f.ListVolumeClaims()
	if err != nil {
		return nil, err
	}
	for _, claim := range claims {
		if fmt.Sprintf("%v", claim["scope"]) != models.VolumeScopeShared {
			continue
		}
		if strings.TrimSpace(fmt.Sprintf("%v", claim["name"])) == name {
			return claim, nil
		}
	}
	return nil, fmt.Errorf("%w: %s", volumereclaim.ErrNotFound, name)
}

// RemovePrivateVolume destroys private persistent VOLUME data under volumes/data/{uuid}.
func (f *Facade) RemovePrivateVolume(uuid, name string, force bool) error {
	if f == nil {
		return errors.New("runtime facade is not initialized")
	}
	return f.volumeReclaimer().RemovePrivate(uuid, name, volumereclaim.RemoveOptions{Force: force})
}

// RemoveSharedVolume destroys a shared persistent VOLUME under volumes/shared/{name}.
func (f *Facade) RemoveSharedVolume(name string, force bool) error {
	if f == nil {
		return errors.New("runtime facade is not initialized")
	}
	return f.volumeReclaimer().RemoveShared(name, volumereclaim.RemoveOptions{Force: force})
}

// PruneVolumeOrphans lists or destroys unreferenced persistent VOLUME claims.
// Dry-run is the default; Yes is required to destroy.
func (f *Facade) PruneVolumeOrphans(req VolumePruneRequest) (map[string]any, error) {
	if f == nil {
		return nil, errors.New("runtime facade is not initialized")
	}
	confirm := req.Yes && !req.DryRun
	opts := volumereclaim.PruneOrphansOptions{
		Confirm: confirm,
		Force:   req.Force,
	}
	if confirm && f.db != nil {
		cleanup, err := f.db.ListPersistentVolumeCleanupUUIDs()
		if err != nil {
			return nil, err
		}
		opts.CleanupUUIDs = cleanup
	}
	result, err := f.volumeReclaimer().PruneOrphans(opts)
	if err != nil {
		return nil, err
	}
	if result == nil {
		result = &volumereclaim.PruneOrphansResult{}
	}
	return map[string]any{
		"status":     "ok",
		"orphans":    true,
		"yes":        req.Yes,
		"force":      req.Force,
		"dryRun":     !confirm,
		"candidates": volumeCandidatesToAPI(result.Candidates),
		"deleted":    volumeCandidatesToAPI(result.Deleted),
		"skipped":    volumeCandidatesToAPI(result.Skipped),
	}, nil
}

func groupVolumeClaims(rows []store.PersistentVolumeRecord, keep store.PersistentVolumeKeepSet) []volumeClaim {
	private := make([]volumeClaim, 0)
	sharedRows := make(map[string][]store.PersistentVolumeRecord)
	for _, rec := range rows {
		if rec.Scope == models.VolumeScopeShared {
			sharedRows[rec.VolumeName] = append(sharedRows[rec.VolumeName], rec)
			continue
		}
		claim := volumeClaim{
			Name:           rec.VolumeName,
			Scope:          rec.Scope,
			Kind:           rec.Kind,
			UUID:           rec.UUID,
			Consumers:      []string{rec.UUID},
			HostPath:       rec.HostPath,
			Desired:        privateUUIDInKeepSet(keep, rec.UUID),
			UnreferencedAt: rec.UnreferencedAt,
		}
		private = append(private, claim)
	}
	slices.SortFunc(private, func(a, b volumeClaim) int {
		if a.UUID != b.UUID {
			return cmp.Compare(a.UUID, b.UUID)
		}
		return cmp.Compare(a.Name, b.Name)
	})

	sharedNames := make([]string, 0, len(sharedRows))
	for name := range sharedRows {
		sharedNames = append(sharedNames, name)
	}
	slices.Sort(sharedNames)
	shared := make([]volumeClaim, 0, len(sharedNames))
	for _, name := range sharedNames {
		recs := sharedRows[name]
		consumers := make([]string, 0, len(recs))
		hostPath := ""
		kind := store.PersistentVolumeKindWorkload
		var maxUnref *int64
		allUnreferenced := true
		for _, rec := range recs {
			consumers = append(consumers, rec.UUID)
			if hostPath == "" {
				hostPath = rec.HostPath
			}
			if rec.Kind != "" {
				kind = rec.Kind
			}
			if rec.UnreferencedAt == nil {
				allUnreferenced = false
				continue
			}
			if maxUnref == nil || *rec.UnreferencedAt > *maxUnref {
				v := *rec.UnreferencedAt
				maxUnref = &v
			}
		}
		slices.Sort(consumers)
		claim := volumeClaim{
			Name:      name,
			Scope:     models.VolumeScopeShared,
			Kind:      kind,
			Consumers: consumers,
			HostPath:  hostPath,
			Desired:   sharedNameInKeepSet(keep, name),
		}
		if allUnreferenced {
			claim.UnreferencedAt = maxUnref
		}
		shared = append(shared, claim)
	}
	return append(private, shared...)
}

func volumeClaimToAPI(claim volumeClaim) map[string]any {
	item := map[string]any{
		"name":      claim.Name,
		"scope":     claim.Scope,
		"kind":      claim.Kind,
		"consumers": claim.Consumers,
		"hostPath":  claim.HostPath,
		"desired":   claim.Desired,
	}
	if claim.Scope == models.VolumeScopeShared {
		item["uuid"] = nil
	} else {
		item["uuid"] = claim.UUID
	}
	if claim.UnreferencedAt != nil {
		item["unreferencedAt"] = time.Unix(*claim.UnreferencedAt, 0).UTC().Format(time.RFC3339)
	} else {
		item["unreferencedAt"] = nil
	}
	return item
}

func volumeCandidatesToAPI(cands []volumereclaim.Candidate) []map[string]any {
	out := make([]map[string]any, 0, len(cands))
	for _, cand := range cands {
		item := map[string]any{
			"name":     cand.Name,
			"scope":    cand.Scope,
			"kind":     cand.Kind,
			"hostPath": cand.HostPath,
		}
		if cand.Scope == models.VolumeScopeShared {
			item["uuid"] = nil
		} else {
			item["uuid"] = cand.UUID
		}
		if !cand.UnreferencedAt.IsZero() {
			item["unreferencedAt"] = cand.UnreferencedAt.UTC().Format(time.RFC3339)
		}
		if strings.TrimSpace(cand.Reason) != "" {
			item["reason"] = cand.Reason
		}
		out = append(out, item)
	}
	return out
}

func privateUUIDInKeepSet(keep store.PersistentVolumeKeepSet, uuid string) bool {
	uuid = strings.TrimSpace(uuid)
	for _, kept := range keep.PrivateUUIDs {
		if kept == uuid {
			return true
		}
	}
	return false
}

func sharedNameInKeepSet(keep store.PersistentVolumeKeepSet, name string) bool {
	name = strings.TrimSpace(name)
	for _, kept := range keep.SharedNames {
		if kept == name {
			return true
		}
	}
	return false
}

type runtimeVolumeUsage struct {
	diskDirectory string
}

func (u runtimeVolumeUsage) ListedPrivateUUIDs() []string {
	pm := processmanager.GetInstance()
	if pm == nil {
		return nil
	}
	containers, err := pm.ListAllContainers()
	if err != nil {
		return nil
	}
	seen := make(map[string]struct{})
	for _, cont := range containers {
		uuid := strings.TrimSpace(pm.GetMicroserviceUUIDForContainer(cont))
		if uuid == "" {
			continue
		}
		seen[uuid] = struct{}{}
	}
	out := make([]string, 0, len(seen))
	for uuid := range seen {
		out = append(out, uuid)
	}
	slices.Sort(out)
	return out
}

func (u runtimeVolumeUsage) ListedSharedNames() []string {
	seen := make(map[string]struct{})
	u.forEachInspect(func(inspect map[string]any) {
		for _, name := range sharedNamesFromInspect(inspect, u.diskDirectory) {
			seen[name] = struct{}{}
		}
	})
	out := make([]string, 0, len(seen))
	for name := range seen {
		out = append(out, name)
	}
	slices.Sort(out)
	return out
}

func (u runtimeVolumeUsage) PathMounted(hostPath string) bool {
	hostPath = filepath.Clean(strings.TrimSpace(hostPath))
	if hostPath == "" {
		return false
	}
	mounted := false
	u.forEachInspect(func(inspect map[string]any) {
		if mounted {
			return
		}
		if inspectContainsPath(inspect, hostPath) {
			mounted = true
		}
	})
	return mounted
}

func (u runtimeVolumeUsage) forEachInspect(fn func(map[string]any)) {
	if fn == nil {
		return
	}
	pm := processmanager.GetInstance()
	if pm == nil {
		return
	}
	containers, err := pm.ListAllContainers()
	if err != nil {
		return
	}
	for _, cont := range containers {
		inspect, inspectErr := pm.InspectContainerRaw(cont.ID)
		if inspectErr != nil || inspect == nil {
			continue
		}
		fn(inspect)
	}
}

func sharedNamesFromInspect(inspect map[string]any, diskDirectory string) []string {
	marker := filepath.Join("volumes", "shared")
	if strings.TrimSpace(diskDirectory) != "" {
		marker = filepath.Join(filepath.Clean(diskDirectory), "volumes", "shared")
	}
	found := make(map[string]struct{})
	walkStrings(inspect, func(value string) {
		cleaned := filepath.Clean(value)
		idx := strings.Index(cleaned, marker)
		if idx < 0 {
			return
		}
		rest := strings.TrimPrefix(cleaned[idx+len(marker):], string(filepath.Separator))
		rest = strings.TrimPrefix(rest, "/")
		name, _, _ := strings.Cut(rest, string(filepath.Separator))
		name = strings.TrimSpace(name)
		if name != "" {
			found[name] = struct{}{}
		}
	})
	out := make([]string, 0, len(found))
	for name := range found {
		out = append(out, name)
	}
	return out
}

func inspectContainsPath(inspect map[string]any, hostPath string) bool {
	found := false
	walkStrings(inspect, func(value string) {
		if found {
			return
		}
		if filepath.Clean(value) == hostPath {
			found = true
		}
	})
	return found
}

func walkStrings(value any, fn func(string)) {
	if fn == nil {
		return
	}
	switch typed := value.(type) {
	case string:
		fn(typed)
	case map[string]any:
		for _, child := range typed {
			walkStrings(child, fn)
		}
	case []any:
		for _, child := range typed {
			walkStrings(child, fn)
		}
	case json.Number:
		fn(typed.String())
	}
}
