package fieldagent

import (
	"fmt"

	"github.com/eclipse-iofog/edgelet/internal/store"
	"github.com/eclipse-iofog/edgelet/internal/utils/logging"
	"github.com/eclipse-iofog/edgelet/internal/volumemount"
	"github.com/eclipse-iofog/edgelet/internal/volumereclaim"
)

// applyDeprovisionVolumePolicy clears rebuildable volume-mount artifacts and
// optionally purges workload persistent VOLUME claims.
//
// Ledger rows keep unreferenced_at NULL after desired-state tables are emptied
// so the same microservice UUIDs remount the same host paths after re-provision.
// Orphan prune does not treat deprovision-only rows as a grace-clock start.
func (fa *FieldAgent) applyDeprovisionVolumePolicy(preserveLocal, purgeVolumes bool) {
	fa.clearSQLiteCacheTablesOnDeprovision(preserveLocal)
	if purgeVolumes {
		fa.purgePersistentVolumesOnDeprovision(preserveLocal)
	}
	fa.clearVolumeMountsOnDeprovision(preserveLocal, func() error {
		return volumemount.GetInstance().Clear()
	}, func() error {
		return volumemount.GetInstance().ClearControllerArtifacts()
	})
}

func (fa *FieldAgent) purgePersistentVolumesOnDeprovision(preserveLocal bool) {
	defer func() {
		if r := recover(); r != nil {
			logging.LogError(moduleName, "Error purging persistent volumes", fmt.Errorf("%v", r))
		}
	}()
	db := store.GetInstance()
	if db.Conn() == nil {
		return
	}
	disk := ""
	if fa.config != nil {
		disk = fa.config.DiskDirectory
	}
	uuids, err := persistentVolumePurgeUUIDs(db, preserveLocal)
	if err != nil {
		logging.LogError(moduleName, "Error listing persistent volumes to purge", err)
		return
	}
	if err := volumereclaim.New(db, disk).PurgeWorkloads(uuids); err != nil {
		logging.LogError(moduleName, "Error purging persistent volumes", err)
	}
}

// persistentVolumePurgeUUIDs returns the workload UUIDs this deprovision may
// destroy. An empty list means every workload claim (scope=all). When local
// workloads are preserved, only non-local consumers are purged so a shared
// name still used by a remaining local or controller microservice stays.
func persistentVolumePurgeUUIDs(db *store.DB, preserveLocal bool) ([]string, error) {
	if !preserveLocal {
		return nil, nil
	}
	localRows, err := db.ListLocalWorkloads()
	if err != nil {
		return nil, err
	}
	keep := make(map[string]struct{}, len(localRows))
	for _, row := range localRows {
		if row == nil || row.LocalUUID == "" {
			continue
		}
		keep[row.LocalUUID] = struct{}{}
	}
	rows, err := db.ListPersistentVolumes()
	if err != nil {
		return nil, err
	}
	seen := make(map[string]struct{})
	uuids := make([]string, 0)
	for _, rec := range rows {
		if rec.Kind != store.PersistentVolumeKindWorkload {
			continue
		}
		if _, ok := keep[rec.UUID]; ok {
			continue
		}
		if _, ok := seen[rec.UUID]; ok {
			continue
		}
		seen[rec.UUID] = struct{}{}
		uuids = append(uuids, rec.UUID)
	}
	return uuids, nil
}
