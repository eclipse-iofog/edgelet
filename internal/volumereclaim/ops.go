package volumereclaim

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/eclipse-iofog/edgelet/internal/models"
	"github.com/eclipse-iofog/edgelet/internal/runtimeops"
	"github.com/eclipse-iofog/edgelet/internal/store"
)

// RemovePrivate destroys a private persistent VOLUME under volumes/data/{uuid}.
// An empty name destroys every private workload claim for the UUID.
func (r *Reclaimer) RemovePrivate(uuid, name string, opts RemoveOptions) error {
	start := time.Now()
	uuid = strings.TrimSpace(uuid)
	name = strings.TrimSpace(name)
	if uuid == "" {
		return errors.New("microservice uuid is required")
	}

	recs, err := r.privateWorkloadRecords(uuid, name)
	if err != nil {
		return err
	}
	keep, err := r.keepSet()
	if err != nil {
		return err
	}
	owned := privateUUIDKept(keep, uuid)
	for i := range recs {
		if err := r.removePrivateRecord(&recs[i], opts, keep, owned, start); err != nil {
			return err
		}
	}
	return nil
}

func (r *Reclaimer) removePrivateRecord(rec *store.PersistentVolumeRecord, opts RemoveOptions, keep store.PersistentVolumeKeepSet, owned bool, start time.Time) error {
	fields := privateEventFields(rec, keep, owned, opts.Force)
	hostPath := strings.TrimSpace(rec.HostPath)
	if hostPath == "" {
		hostPath = store.PersistentVolumeHostPath(r.diskDirectory, rec.UUID, rec.VolumeName, models.VolumeScopePrivate)
	}
	if _, err := jailedUnder(hostPath, r.dataRoot()); err != nil {
		r.emitAbort(runtimeops.ReasonVolumePathJail, "persistent volume path is outside volumes/data", triggerVolumeRM, err, fields, start)
		return err
	}
	if r.pathMounted(hostPath) {
		err := fmt.Errorf("%w: %s", ErrInUse, hostPath)
		r.emitAbort(runtimeops.ReasonVolumeInUse, "persistent volume is still mounted", triggerVolumeRM, err, fields, start)
		return err
	}
	if owned && !opts.Force {
		err := fmt.Errorf("%w: %s", ErrKeepSet, rec.UUID)
		r.emitAbort(runtimeops.ReasonVolumeKeepSet, "persistent volume is still in the keep-set", triggerVolumeRM, err, fields, start)
		return err
	}

	bytes, err := r.destroyJailed(hostPath, r.dataRoot())
	if err != nil {
		r.emitAbort(runtimeops.ReasonRemoveFailed, "failed to destroy persistent volume", triggerVolumeRM, err, fields, start)
		return err
	}
	if err := r.db.DeletePersistentVolume(rec.UUID, rec.VolumeName); err != nil {
		return err
	}
	fields["path"] = hostPath
	fields["bytes"] = bytes
	r.emitDeleted("persistent volume removed", triggerVolumeRM, fields, start)
	return nil
}

func (r *Reclaimer) privateWorkloadRecords(uuid, name string) ([]store.PersistentVolumeRecord, error) {
	if name != "" {
		rec, err := r.db.GetPersistentVolume(uuid, name)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return nil, fmt.Errorf("%w: %s/%s", ErrNotFound, uuid, name)
			}
			return nil, err
		}
		if rec.Kind == store.PersistentVolumeKindControlPlane {
			return nil, fmt.Errorf("%w: %s/%s", ErrControlPlane, uuid, name)
		}
		if rec.Scope == models.VolumeScopeShared {
			return nil, fmt.Errorf("volume %s is shared; use shared reclaim", name)
		}
		return []store.PersistentVolumeRecord{*rec}, nil
	}

	all, err := r.db.ListPersistentVolumesForUUID(uuid)
	if err != nil {
		return nil, err
	}
	out := make([]store.PersistentVolumeRecord, 0, len(all))
	sawControlPlane := false
	for _, rec := range all {
		if rec.Scope != models.VolumeScopePrivate {
			continue
		}
		if rec.Kind == store.PersistentVolumeKindControlPlane {
			sawControlPlane = true
			continue
		}
		out = append(out, rec)
	}
	if len(out) == 0 {
		if sawControlPlane {
			return nil, fmt.Errorf("%w: %s", ErrControlPlane, uuid)
		}
		return nil, fmt.Errorf("%w: %s", ErrNotFound, uuid)
	}
	return out, nil
}

// RemoveShared destroys a shared persistent VOLUME under volumes/shared/{name}.
func (r *Reclaimer) RemoveShared(name string, opts RemoveOptions) error {
	start := time.Now()
	name = strings.TrimSpace(name)
	if name == "" {
		return errors.New("volume name is required")
	}
	consumers, err := r.db.ListSharedPersistentVolumeConsumers(name)
	if err != nil {
		return err
	}
	if len(consumers) == 0 {
		return fmt.Errorf("%w: %s", ErrNotFound, name)
	}

	keep, err := r.keepSet()
	if err != nil {
		return err
	}
	owned := sharedNameKept(keep, name)
	hostPath := strings.TrimSpace(consumers[0].HostPath)
	if hostPath == "" {
		hostPath = store.PersistentVolumeHostPath(r.diskDirectory, "", name, models.VolumeScopeShared)
	}
	fields := sharedEventFields(name, hostPath, len(consumers), keep, owned, opts.Force)

	if _, err := jailedUnder(hostPath, r.sharedRoot()); err != nil {
		r.emitAbort(runtimeops.ReasonVolumePathJail, "shared volume path is outside volumes/shared", triggerVolumeRM, err, fields, start)
		return err
	}
	if r.pathMounted(hostPath) {
		err := fmt.Errorf("%w: %s", ErrInUse, hostPath)
		r.emitAbort(runtimeops.ReasonVolumeInUse, "shared volume is still mounted", triggerVolumeRM, err, fields, start)
		return err
	}
	if owned && !opts.Force {
		err := fmt.Errorf("%w: %s", ErrKeepSet, name)
		r.emitAbort(runtimeops.ReasonVolumeKeepSet, "shared volume still has consumers in the keep-set", triggerVolumeRM, err, fields, start)
		return err
	}

	bytes, err := r.destroyJailed(hostPath, r.sharedRoot())
	if err != nil {
		r.emitAbort(runtimeops.ReasonRemoveFailed, "failed to destroy shared volume", triggerVolumeRM, err, fields, start)
		return err
	}
	if err := r.db.DeleteSharedPersistentVolumes(name); err != nil {
		return err
	}
	fields["bytes"] = bytes
	r.emitDeleted("shared volume removed", triggerVolumeRM, fields, start)
	return nil
}

// PruneOrphans lists or destroys unreferenced workload volumes.
// Confirm=false is a dry-run and never deletes.
func (r *Reclaimer) PruneOrphans(opts PruneOrphansOptions) (*PruneOrphansResult, error) {
	start := time.Now()
	orphans, err := r.db.ListUnreferencedWorkloadVolumes()
	if err != nil {
		return nil, err
	}
	keep, err := r.keepSet()
	if err != nil {
		return nil, err
	}
	cleanup := cleanupSet(opts.CleanupUUIDs)
	now := r.currentTime()
	result := &PruneOrphansResult{
		Candidates: make([]Candidate, 0),
		Deleted:    make([]Candidate, 0),
		Skipped:    make([]Candidate, 0),
	}

	for _, orphan := range orphans {
		cand := candidateFromOrphan(orphan)
		if orphan.Scope == models.VolumeScopeShared {
			if sharedNameKept(keep, orphan.VolumeName) {
				continue
			}
		} else if privateUUIDKept(keep, orphan.UUID) {
			continue
		}
		result.Candidates = append(result.Candidates, cand)
		if !opts.Confirm {
			continue
		}

		hostPath := strings.TrimSpace(orphan.HostPath)
		root := r.dataRoot()
		if orphan.Scope == models.VolumeScopeShared {
			root = r.sharedRoot()
			if hostPath == "" {
				hostPath = store.PersistentVolumeHostPath(r.diskDirectory, "", orphan.VolumeName, models.VolumeScopeShared)
			}
		} else if hostPath == "" {
			hostPath = store.PersistentVolumeHostPath(r.diskDirectory, orphan.UUID, orphan.VolumeName, models.VolumeScopePrivate)
		}
		fields := pruneEventFields(orphan, keep)

		if _, jailErr := jailedUnder(hostPath, root); jailErr != nil {
			cand.Reason = "path-jail"
			result.Skipped = append(result.Skipped, cand)
			r.emitAbort(runtimeops.ReasonVolumePathJail, "orphan volume path is outside the allowed directories", triggerVolumePrune, jailErr, fields, start)
			continue
		}
		if r.pathMounted(hostPath) {
			cand.Reason = "mounted"
			result.Skipped = append(result.Skipped, cand)
			err := fmt.Errorf("%w: %s", ErrInUse, hostPath)
			r.emitAbort(runtimeops.ReasonVolumeInUse, "orphan volume is still mounted", triggerVolumePrune, err, fields, start)
			continue
		}

		bypassGrace := opts.Force
		if !bypassGrace {
			if orphan.Scope == models.VolumeScopeShared {
				_, ok := cleanup[strings.TrimSpace(orphan.LastConsumerUUID)]
				bypassGrace = ok
			} else {
				_, ok := cleanup[strings.TrimSpace(orphan.UUID)]
				bypassGrace = ok
			}
		}
		deadline := time.Unix(orphan.UnreferencedAt, 0).Add(orphanGrace)
		if !bypassGrace && now.Before(deadline) {
			cand.Reason = "grace"
			result.Skipped = append(result.Skipped, cand)
			continue
		}

		bytes, destroyErr := r.destroyJailed(hostPath, root)
		if destroyErr != nil {
			cand.Reason = "destroy"
			result.Skipped = append(result.Skipped, cand)
			r.emitAbort(runtimeops.ReasonRemoveFailed, "failed to destroy orphan volume", triggerVolumePrune, destroyErr, fields, start)
			continue
		}
		if orphan.Scope == models.VolumeScopeShared {
			if err := r.db.DeleteSharedPersistentVolumes(orphan.VolumeName); err != nil {
				return result, err
			}
		} else if err := r.db.DeletePersistentVolume(orphan.UUID, orphan.VolumeName); err != nil {
			return result, err
		}
		fields["path"] = hostPath
		fields["bytes"] = bytes
		r.emitDeleted("orphan volume pruned", triggerVolumePrune, fields, start)
		result.Deleted = append(result.Deleted, cand)
	}
	return result, nil
}

// PurgeWorkloads destroys workload persistent volumes for the given UUIDs.
// An empty UUID list purges every workload claim. Shared names are destroyed
// only when no consumer remains after those UUIDs are dropped.
func (r *Reclaimer) PurgeWorkloads(uuids []string) error {
	start := time.Now()
	rows, err := r.db.ListPersistentVolumes()
	if err != nil {
		return err
	}
	target := cleanupSet(uuids)
	if len(target) == 0 {
		for _, rec := range rows {
			if rec.Kind != store.PersistentVolumeKindWorkload {
				continue
			}
			target[rec.UUID] = struct{}{}
		}
	}

	keep, err := r.keepSet()
	if err != nil {
		return err
	}

	var firstErr error
	setFirst := func(err error) {
		if firstErr == nil {
			firstErr = err
		}
	}

	for i := range rows {
		rec := rows[i]
		if rec.Kind != store.PersistentVolumeKindWorkload || rec.Scope != models.VolumeScopePrivate {
			continue
		}
		if _, ok := target[rec.UUID]; !ok {
			continue
		}
		if err := r.purgePrivate(&rec, keep, start); err != nil {
			setFirst(err)
		}
	}

	sharedConsumers := make(map[string][]store.PersistentVolumeRecord)
	for _, rec := range rows {
		if rec.Kind != store.PersistentVolumeKindWorkload || rec.Scope != models.VolumeScopeShared {
			continue
		}
		sharedConsumers[rec.VolumeName] = append(sharedConsumers[rec.VolumeName], rec)
	}
	for name, consumers := range sharedConsumers {
		remaining := 0
		for _, rec := range consumers {
			if _, ok := target[rec.UUID]; !ok {
				remaining++
			}
		}
		if remaining > 0 {
			for _, rec := range consumers {
				if _, ok := target[rec.UUID]; !ok {
					continue
				}
				if err := r.db.DeletePersistentVolume(rec.UUID, rec.VolumeName); err != nil {
					setFirst(err)
				}
			}
			continue
		}
		if err := r.purgeShared(name, consumers, keep, start); err != nil {
			setFirst(err)
		}
	}
	return firstErr
}

func (r *Reclaimer) purgePrivate(rec *store.PersistentVolumeRecord, keep store.PersistentVolumeKeepSet, start time.Time) error {
	owned := privateUUIDKept(keep, rec.UUID)
	fields := privateEventFields(rec, keep, owned, true)
	fields["trigger"] = triggerPurgeVolumes
	hostPath := strings.TrimSpace(rec.HostPath)
	if hostPath == "" {
		hostPath = store.PersistentVolumeHostPath(r.diskDirectory, rec.UUID, rec.VolumeName, models.VolumeScopePrivate)
	}
	if _, err := jailedUnder(hostPath, r.dataRoot()); err != nil {
		r.emitAbort(runtimeops.ReasonVolumePathJail, "persistent volume path is outside volumes/data", triggerPurgeVolumes, err, fields, start)
		return err
	}
	if r.pathMounted(hostPath) {
		err := fmt.Errorf("%w: %s", ErrInUse, hostPath)
		r.emitAbort(runtimeops.ReasonVolumeInUse, "persistent volume is still mounted", triggerPurgeVolumes, err, fields, start)
		return err
	}
	bytes, err := r.destroyJailed(hostPath, r.dataRoot())
	if err != nil {
		r.emitAbort(runtimeops.ReasonRemoveFailed, "failed to destroy persistent volume", triggerPurgeVolumes, err, fields, start)
		return err
	}
	if err := r.db.DeletePersistentVolume(rec.UUID, rec.VolumeName); err != nil {
		return err
	}
	fields["path"] = hostPath
	fields["bytes"] = bytes
	r.emitDeleted("workload volume purged", triggerPurgeVolumes, fields, start)
	return nil
}

func (r *Reclaimer) purgeShared(name string, consumers []store.PersistentVolumeRecord, keep store.PersistentVolumeKeepSet, start time.Time) error {
	hostPath := ""
	if len(consumers) > 0 {
		hostPath = strings.TrimSpace(consumers[0].HostPath)
	}
	if hostPath == "" {
		hostPath = store.PersistentVolumeHostPath(r.diskDirectory, "", name, models.VolumeScopeShared)
	}
	owned := sharedNameKept(keep, name)
	fields := sharedEventFields(name, hostPath, 0, keep, owned, true)
	if _, err := jailedUnder(hostPath, r.sharedRoot()); err != nil {
		r.emitAbort(runtimeops.ReasonVolumePathJail, "shared volume path is outside volumes/shared", triggerPurgeVolumes, err, fields, start)
		return err
	}
	if r.pathMounted(hostPath) {
		err := fmt.Errorf("%w: %s", ErrInUse, hostPath)
		r.emitAbort(runtimeops.ReasonVolumeInUse, "shared volume is still mounted", triggerPurgeVolumes, err, fields, start)
		return err
	}
	bytes, err := r.destroyJailed(hostPath, r.sharedRoot())
	if err != nil {
		r.emitAbort(runtimeops.ReasonRemoveFailed, "failed to destroy shared volume", triggerPurgeVolumes, err, fields, start)
		return err
	}
	if err := r.db.DeleteSharedPersistentVolumes(name); err != nil {
		return err
	}
	fields["bytes"] = bytes
	r.emitDeleted("shared volume purged", triggerPurgeVolumes, fields, start)
	return nil
}

// DeleteControlPlane destroys control-plane volumes for the given UUID only.
func (r *Reclaimer) DeleteControlPlane(uuid string) error {
	start := time.Now()
	uuid = strings.TrimSpace(uuid)
	if uuid == "" {
		return errors.New("control-plane uuid is required")
	}
	rows, err := r.db.ListPersistentVolumesForUUID(uuid)
	if err != nil {
		return err
	}
	keep, err := r.keepSet()
	if err != nil {
		return err
	}
	owned := privateUUIDKept(keep, uuid)
	var firstErr error
	for i := range rows {
		rec := rows[i]
		if rec.Kind != store.PersistentVolumeKindControlPlane {
			continue
		}
		if err := r.deleteControlPlaneRecord(&rec, keep, owned, start); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func (r *Reclaimer) deleteControlPlaneRecord(rec *store.PersistentVolumeRecord, keep store.PersistentVolumeKeepSet, owned bool, start time.Time) error {
	fields := privateEventFields(rec, keep, owned, true)
	hostPath := strings.TrimSpace(rec.HostPath)
	if hostPath == "" {
		hostPath = store.PersistentVolumeHostPath(r.diskDirectory, rec.UUID, rec.VolumeName, models.VolumeScopePrivate)
	}
	if _, err := jailedUnder(hostPath, r.dataRoot()); err != nil {
		r.emitAbort(runtimeops.ReasonVolumePathJail, "control-plane volume path is outside volumes/data", triggerControlPlaneDelete, err, fields, start)
		return err
	}
	if r.pathMounted(hostPath) {
		err := fmt.Errorf("%w: %s", ErrInUse, hostPath)
		r.emitAbort(runtimeops.ReasonVolumeInUse, "control-plane volume is still mounted", triggerControlPlaneDelete, err, fields, start)
		return err
	}
	bytes, err := r.destroyJailed(hostPath, r.dataRoot())
	if err != nil {
		r.emitAbort(runtimeops.ReasonRemoveFailed, "failed to destroy control-plane volume", triggerControlPlaneDelete, err, fields, start)
		return err
	}
	if err := r.db.DeletePersistentVolume(rec.UUID, rec.VolumeName); err != nil {
		return err
	}
	fields["path"] = hostPath
	fields["bytes"] = bytes
	r.emitDeleted("control-plane volume deleted", triggerControlPlaneDelete, fields, start)
	return nil
}

func privateEventFields(rec *store.PersistentVolumeRecord, keep store.PersistentVolumeKeepSet, owned, force bool) map[string]any {
	return map[string]any{
		"uuid":              rec.UUID,
		"name":              rec.VolumeName,
		"scope":             rec.Scope,
		"kind":              rec.Kind,
		"path":              rec.HostPath,
		"keepSetSize":       len(keep.PrivateUUIDs) + len(keep.SharedNames),
		"desiredStillOwned": owned,
		"force":             force,
	}
}

func sharedEventFields(name, hostPath string, consumers int, keep store.PersistentVolumeKeepSet, owned, force bool) map[string]any {
	return map[string]any{
		"name":              name,
		"scope":             models.VolumeScopeShared,
		"path":              hostPath,
		"consumerCount":     consumers,
		"keepSetSize":       len(keep.PrivateUUIDs) + len(keep.SharedNames),
		"desiredStillOwned": owned,
		"force":             force,
	}
}

func pruneEventFields(orphan store.UnreferencedPersistentVolume, keep store.PersistentVolumeKeepSet) map[string]any {
	fields := map[string]any{
		"name":        orphan.VolumeName,
		"scope":       orphan.Scope,
		"path":        orphan.HostPath,
		"keepSetSize": len(keep.PrivateUUIDs) + len(keep.SharedNames),
	}
	if orphan.Scope == models.VolumeScopeShared {
		fields["consumerCount"] = 0
	} else {
		fields["uuid"] = orphan.UUID
	}
	return fields
}

func candidateFromOrphan(orphan store.UnreferencedPersistentVolume) Candidate {
	return Candidate{
		UUID:           orphan.UUID,
		Name:           orphan.VolumeName,
		Scope:          orphan.Scope,
		Kind:           orphan.Kind,
		HostPath:       orphan.HostPath,
		UnreferencedAt: time.Unix(orphan.UnreferencedAt, 0).UTC(),
	}
}
