package processmanager

import (
	"strings"
	"time"

	"github.com/eclipse-iofog/edgelet/internal/models"
	"github.com/eclipse-iofog/edgelet/internal/statusreporter"
	"github.com/eclipse-iofog/edgelet/internal/store"
)

func (pm *ProcessManager) containerStatsLoop() {
	defer pm.wg.Done()
	ticker := time.NewTicker(containerStatsInterval)
	defer ticker.Stop()
	for {
		select {
		case <-pm.ctx.Done():
			return
		case <-ticker.C:
			pm.sampleRunningContainerStats()
			AdvanceRunningErrorClear()
		}
	}
}

// sampleRunningContainerStats writes CPU and memory onto the stored status of
// each running container. It does not compare the spec.
func (pm *ProcessManager) sampleRunningContainerStats() {
	if pm == nil || pm.engine == nil {
		return
	}
	type running struct {
		uuid string
		cid  string
	}
	var items []running
	statusreporter.GetInstance().UpdateProcessManagerStatus(func(s *models.ProcessManagerStatus) {
		if s == nil {
			return
		}
		for uuid, st := range s.MicroservicesStatus {
			if st == nil || st.Status != models.MicroserviceStateRunning {
				continue
			}
			cid := strings.TrimSpace(st.ContainerID)
			if cid == "" {
				continue
			}
			items = append(items, running{uuid: uuid, cid: cid})
		}
	})
	for _, item := range items {
		if !pm.shouldSampleContainerStats(item.uuid) {
			continue
		}
		stats, err := pm.engine.GetContainerStats(item.cid)
		if err != nil || stats == nil {
			continue
		}
		statusreporter.GetInstance().UpdateProcessManagerStatus(func(s *models.ProcessManagerStatus) {
			cur := s.LookupMicroserviceStatus(item.uuid)
			if cur == nil || cur.Status != models.MicroserviceStateRunning {
				return
			}
			cur.CPUUsage = stats.CPUUsage
			cur.MemoryUsage = stats.MemoryUsage
		})
	}
}

func (pm *ProcessManager) shouldSampleContainerStats(uuid string) bool {
	item, found, err := store.GetInstance().GetSystemControlPlane()
	if err != nil || !found || item == nil {
		return true
	}
	if strings.TrimSpace(item.ControllerUUID) != strings.TrimSpace(uuid) {
		return true
	}
	return item.ControllerRegistered
}
