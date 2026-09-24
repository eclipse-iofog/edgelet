package processmanager

import (
	"strings"
	"time"

	"github.com/eclipse-iofog/edgelet/internal/catalogwake"
	"github.com/eclipse-iofog/edgelet/internal/config"
	"github.com/eclipse-iofog/edgelet/internal/constants"
	"github.com/eclipse-iofog/edgelet/internal/models"
	"github.com/eclipse-iofog/edgelet/internal/store"
)

func init() {
	catalogwake.Mark = MarkWorkloadsUsingCatalogItem
}

// fullReconcileInterval is how often every workload is reconciled even when
// nothing has reported a change. It is a code constant, not configuration.
const fullReconcileInterval = 60 * time.Second

// containerStatsInterval is how often running-container CPU and memory are
// sampled. It matches the default controller status post (statusFrequency 10).
const containerStatsInterval = 10 * time.Second

// reconcileDeadline is one per-workload timer. generation drops a callback
// from a timer that was replaced or stopped.
type reconcileDeadline struct {
	generation uint64
	timer      *time.Timer
}

const (
	runtimeEventStart  = "start"
	runtimeEventExit   = "exit"
	runtimeEventOOM    = "oom"
	runtimeEventDelete = "delete"
)

// runtimeEventSignal is one container lifecycle wake for a single workload.
type runtimeEventSignal struct {
	uuid string
	kind string
}

// reconcileTickInterval is how soon a volume hold is retried. It matches the
// monitor period so a held create is noticed on the same cadence as today.
func reconcileTickInterval() time.Duration {
	const fallback = 5 * time.Second
	cfg := config.GetInstance()
	if cfg == nil || cfg.MonitorContainersStatusFreqSeconds <= 0 {
		return fallback
	}
	return time.Duration(cfg.MonitorContainersStatusFreqSeconds) * time.Second
}

// MarkReconcile records a workload for the next pass and wakes the monitor.
// Marking the same UUID again is safe.
func (pm *ProcessManager) MarkReconcile(uuid string) {
	pm.markReconcile(uuid)
}

// ReconcilePending reports whether a workload is waiting for the next pass.
func (pm *ProcessManager) ReconcilePending(uuid string) bool {
	return pm.reconcileIsDirty(uuid)
}

// MarkWorkloadsUsingCatalogItem marks workloads whose catalog lists name.
// A local item wakes local workloads. A fleet item wakes controller workloads.
func MarkWorkloadsUsingCatalogItem(name, source string) {
	pm := GetInstance()
	if pm == nil {
		return
	}
	pm.markWorkloadsUsingCatalogItem(name, source)
}

func (pm *ProcessManager) markWorkloadsUsingCatalogItem(name, source string) {
	name = strings.TrimSpace(name)
	if pm == nil || name == "" {
		return
	}
	source = strings.TrimSpace(source)
	wantLocal := source == "" || source == models.ModelSourceLocal
	wantManaged := source == "" || source == models.ModelSourceManaged
	if wantManaged && pm.microserviceManager != nil {
		for _, ms := range pm.microserviceManager.GetLatestMicroservices() {
			if catalogNames(ms, name) {
				pm.markReconcile(ms.MicroserviceUUID)
			}
		}
	}
	if !wantLocal {
		return
	}
	items, err := pm.listLocalWorkloadsForReconcile()
	if err != nil {
		return
	}
	for _, item := range items {
		if item == nil {
			continue
		}
		doc, decErr := decodeLocalDeployManifest(item.ManifestYAML)
		if decErr != nil || doc == nil {
			continue
		}
		ms := models.BuildMicroserviceFromLocalManifest(doc, item.LocalUUID, doc.ManifestImage())
		if catalogNames(ms, name) {
			pm.markReconcile(item.LocalUUID)
		}
	}
}

func catalogNames(ms *models.Microservice, name string) bool {
	if ms == nil || name == "" {
		return false
	}
	for _, item := range models.CatalogItemNames(ms.Models) {
		if item == name {
			return true
		}
	}
	for _, item := range models.KnowledgeCatalogItemNames(ms.Knowledge) {
		if item == name {
			return true
		}
	}
	return false
}

// markReconcile records a workload for the next pass and wakes the monitor.
// Marking the same UUID again is safe.
func (pm *ProcessManager) markReconcile(uuid string) {
	if !pm.addReconcileDirty(uuid) {
		return
	}
	pm.notifyMonitorThread()
}

func (pm *ProcessManager) addReconcileDirty(uuid string) bool {
	uuid = strings.TrimSpace(uuid)
	if pm == nil || uuid == "" {
		return false
	}
	pm.reconcileMu.Lock()
	defer pm.reconcileMu.Unlock()
	if pm.reconcileDirty == nil {
		pm.reconcileDirty = make(map[string]struct{})
	}
	if _, exists := pm.reconcileDirty[uuid]; exists {
		return false
	}
	pm.reconcileDirty[uuid] = struct{}{}
	return true
}

func (pm *ProcessManager) takeReconcileDirty() map[string]struct{} {
	if pm == nil {
		return nil
	}
	pm.reconcileMu.Lock()
	defer pm.reconcileMu.Unlock()
	marked := pm.reconcileDirty
	pm.reconcileDirty = nil
	return marked
}

func (pm *ProcessManager) reconcileIsDirty(uuid string) bool {
	if pm == nil {
		return false
	}
	pm.reconcileMu.Lock()
	defer pm.reconcileMu.Unlock()
	_, ok := pm.reconcileDirty[strings.TrimSpace(uuid)]
	return ok
}

// armReconcileDeadline marks the workload when delay elapses. It does not
// enqueue work; the monitor pass still does that, so a parked retry starts once.
func (pm *ProcessManager) armReconcileDeadline(uuid string, delay time.Duration) {
	uuid = strings.TrimSpace(uuid)
	if pm == nil || uuid == "" || delay <= 0 {
		return
	}
	pm.reconcileMu.Lock()
	if pm.reconcileDeadlines == nil {
		pm.reconcileDeadlines = make(map[string]*reconcileDeadline)
	}
	prev := pm.reconcileDeadlines[uuid]
	gen := uint64(1)
	if prev != nil {
		gen = prev.generation + 1
		if prev.timer != nil {
			prev.timer.Stop()
		}
	}
	slot := &reconcileDeadline{generation: gen}
	pm.reconcileDeadlines[uuid] = slot
	after := pm.reconcileDeadlineAfter
	pm.reconcileMu.Unlock()

	if after == nil {
		after = time.AfterFunc
	}
	timer := after(delay, func() {
		pm.reconcileMu.Lock()
		current := pm.reconcileDeadlines[uuid]
		if current == nil || current.generation != gen {
			pm.reconcileMu.Unlock()
			return
		}
		delete(pm.reconcileDeadlines, uuid)
		pm.reconcileMu.Unlock()
		pm.markReconcile(uuid)
	})
	pm.reconcileMu.Lock()
	current := pm.reconcileDeadlines[uuid]
	if current != nil && current.generation == gen {
		current.timer = timer
	} else if timer != nil {
		timer.Stop()
	}
	pm.reconcileMu.Unlock()
}

// drainReconcileWake folds a deadline wake into the pass already selected,
// including a periodic tick that is also waiting.
func (pm *ProcessManager) drainReconcileWake(ticker *time.Ticker) {
	if pm == nil || ticker == nil {
		return
	}
	if pm.updateChan == nil {
		select {
		case <-ticker.C:
		default:
		}
		return
	}
	select {
	case <-pm.updateChan:
	case <-ticker.C:
	default:
	}
}

// reconcileScheduled reconciles dirty workloads.
// A healthy edgelet event stream with nothing dirty and no sweep due returns
// without loading containers. A down stream, or the periodic full pass, reconciles
// every workload. Docker and Podman only read whether each container is running.
// idle reports that this pass did not inspect the runtime.
func (pm *ProcessManager) reconcileScheduled(stats *reconcileCycleStats) (idle bool) {
	if pm == nil {
		return true
	}
	sweep := pm.consumeFullSweepDue()
	degraded := pm.eventStreamDegraded()
	statusOnly := pm.statusOnlyReconcileTick()
	pm.setContainerSpecCompare(sweep || degraded)
	defer pm.setContainerSpecCompare(false)
	if !sweep && !degraded && !statusOnly && !pm.reconcileHasDirty() {
		pm.enqueueDueRetries()
		return true
	}

	cpUUID, _, _ := pm.lookupControlPlane()
	locals, localErr := pm.listLocalWorkloadsForReconcile()
	if sweep || degraded {
		pm.markAllWorkloads(cpUUID, locals, localErr)
	} else if statusOnly {
		pm.markStoppedWorkloads(cpUUID, locals, localErr)
	}
	marked := pm.takeReconcileDirty()
	if marked == nil {
		marked = map[string]struct{}{}
	}

	if cpUUID != "" {
		if _, ok := marked[cpUUID]; ok {
			pm.reconcileWorkload(cpUUID, stats)
		}
	} else if sweep || degraded {
		pm.reconcileControlPlane()
	}

	if pm.microserviceManager != nil {
		pm.reconcileControllerMicroservices(marked, stats)
	}
	pm.enqueueDueRetries()
	pm.reconcileMarkedLocal(marked, locals, localErr)
	return false
}

func (pm *ProcessManager) reconcileClock() time.Time {
	if pm != nil && pm.reconcileNow != nil {
		return pm.reconcileNow()
	}
	return time.Now()
}

// requestFullSweep asks the next pass to reconcile every workload.
func (pm *ProcessManager) requestFullSweep() {
	if pm == nil {
		return
	}
	pm.reconcileMu.Lock()
	pm.forceFullSweep = true
	pm.reconcileMu.Unlock()
}

func (pm *ProcessManager) consumeFullSweepDue() bool {
	if pm == nil {
		return false
	}
	now := pm.reconcileClock()
	pm.reconcileMu.Lock()
	defer pm.reconcileMu.Unlock()
	if pm.forceFullSweep {
		pm.forceFullSweep = false
		pm.lastFullSweep = now
		return true
	}
	if pm.lastFullSweep.IsZero() {
		pm.lastFullSweep = now
		return false
	}
	if now.Sub(pm.lastFullSweep) >= fullReconcileInterval {
		pm.lastFullSweep = now
		return true
	}
	return false
}

func (pm *ProcessManager) reconcileHasDirty() bool {
	if pm == nil {
		return false
	}
	pm.reconcileMu.Lock()
	defer pm.reconcileMu.Unlock()
	return len(pm.reconcileDirty) > 0
}

func (pm *ProcessManager) statusOnlyReconcileTick() bool {
	if pm == nil {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(pm.engineName)) {
	case constants.EngineDocker, constants.EnginePodman:
		return true
	default:
		return false
	}
}

// markStoppedWorkloads marks workloads whose container is not running so the
// existing restart path runs on this pass. A running container is left clean:
// this check does not compare the spec and does not read usage.
func (pm *ProcessManager) markStoppedWorkloads(cpUUID string, locals []*models.LocalDeployedMicroservice, localErr error) {
	if pm == nil {
		return
	}
	if cpUUID != "" && !pm.workloadAppearsRunning(cpUUID) {
		pm.addReconcileDirty(cpUUID)
	}
	if pm.microserviceManager != nil {
		for _, ms := range pm.microserviceManager.GetLatestMicroservices() {
			if ms == nil {
				continue
			}
			uuid := strings.TrimSpace(ms.MicroserviceUUID)
			if uuid == "" || pm.workloadAppearsRunning(uuid) {
				continue
			}
			pm.addReconcileDirty(uuid)
		}
	}
	if localErr != nil {
		return
	}
	for _, item := range locals {
		if item == nil {
			continue
		}
		uuid := strings.TrimSpace(item.LocalUUID)
		if uuid == "" || pm.workloadAppearsRunning(uuid) {
			continue
		}
		pm.addReconcileDirty(uuid)
	}
}

func (pm *ProcessManager) workloadAppearsRunning(uuid string) bool {
	if pm == nil || pm.engine == nil {
		return false
	}
	container, err := pm.engine.GetContainer(strings.TrimSpace(uuid))
	if err != nil || container == nil {
		return false
	}
	status, err := pm.engine.GetContainerStatus(container.ID, uuid)
	if err != nil || status == nil {
		return false
	}
	return status.Status == models.MicroserviceStateRunning
}

func (pm *ProcessManager) markAllWorkloads(cpUUID string, locals []*models.LocalDeployedMicroservice, localErr error) {
	if cpUUID != "" {
		pm.addReconcileDirty(cpUUID)
	}
	pm.markControllerWorkloads()
	if localErr != nil {
		return
	}
	for _, item := range locals {
		if item == nil {
			continue
		}
		pm.addReconcileDirty(item.LocalUUID)
	}
}

func (pm *ProcessManager) markControllerWorkloads() {
	if pm == nil || pm.microserviceManager == nil {
		return
	}
	for _, ms := range pm.microserviceManager.GetLatestMicroservices() {
		if ms == nil {
			continue
		}
		pm.addReconcileDirty(ms.MicroserviceUUID)
	}
}

func (pm *ProcessManager) lookupControlPlane() (uuid string, found bool, err error) {
	item, found, err := store.GetInstance().GetSystemControlPlane()
	if err != nil || !found || item == nil {
		return "", found, err
	}
	return strings.TrimSpace(item.ControllerUUID), true, nil
}

func (pm *ProcessManager) listLocalWorkloadsForReconcile() ([]*models.LocalDeployedMicroservice, error) {
	if LocalWorkloadsOutOfScope(config.GetInstance().WatchdogEnabled) {
		return nil, nil
	}
	return store.GetInstance().ListLocalWorkloads()
}

func (pm *ProcessManager) reconcileMarkedLocal(marked map[string]struct{}, items []*models.LocalDeployedMicroservice, err error) {
	if err != nil {
		if pm.logger != nil {
			pm.logger.Warnf("local reconcile list deployments failed: %v", err)
		}
		return
	}
	if len(marked) == 0 {
		return
	}
	for _, item := range items {
		if item == nil || strings.TrimSpace(item.LocalUUID) == "" {
			continue
		}
		if _, ok := marked[strings.TrimSpace(item.LocalUUID)]; !ok {
			continue
		}
		pm.reconcileWorkload(item.LocalUUID, nil)
	}
}

// reconcileWorkload reconciles one UUID through the existing function for its kind.
func (pm *ProcessManager) reconcileWorkload(uuid string, stats *reconcileCycleStats) {
	uuid = strings.TrimSpace(uuid)
	if pm == nil || uuid == "" {
		return
	}
	if cpUUID, _, _ := pm.lookupControlPlane(); cpUUID != "" && uuid == cpUUID {
		pm.reconcileControlPlane()
		return
	}
	if pm.microserviceManager != nil {
		if ms := pm.microserviceManager.FindLatestMicroserviceByUUID(uuid); ms != nil {
			pm.reconcileControllerMicroservices(map[string]struct{}{uuid: {}}, stats)
			return
		}
	}
	if item := loadLocalWorkload(uuid); item != nil {
		pm.reconcileOneLocalDeployment(item)
	}
}

func (pm *ProcessManager) setReconcileMonitorRunning(running bool) {
	if pm == nil {
		return
	}
	pm.reconcileMu.Lock()
	pm.reconcileMonitorRunning = running
	pm.reconcileMu.Unlock()
}

func (pm *ProcessManager) reconcileMonitorIsRunning() bool {
	if pm == nil {
		return false
	}
	pm.reconcileMu.Lock()
	defer pm.reconcileMu.Unlock()
	return pm.reconcileMonitorRunning
}

func (pm *ProcessManager) setContainerSpecCompare(allow bool) {
	type specGate interface {
		SetContainerSpecCompare(bool)
	}
	if pm == nil || pm.engine == nil {
		return
	}
	if g, ok := pm.engine.(specGate); ok {
		g.SetContainerSpecCompare(allow)
	}
}

func (pm *ProcessManager) eventStreamDegraded() bool {
	if pm == nil {
		return false
	}
	pm.reconcileMu.Lock()
	defer pm.reconcileMu.Unlock()
	return pm.runtimeEventStreamDegraded
}

// RuntimeEventStreamDegraded reports whether container events are currently down.
func (pm *ProcessManager) RuntimeEventStreamDegraded() bool {
	return pm.eventStreamDegraded()
}

// NoteRuntimeEventStreamUnavailable marks the stream down. It reports whether
// this is the start of the outage so the caller logs once, not on every tick.
func (pm *ProcessManager) NoteRuntimeEventStreamUnavailable() bool {
	if pm == nil {
		return false
	}
	pm.reconcileMu.Lock()
	defer pm.reconcileMu.Unlock()
	pm.runtimeEventStreamDegraded = true
	if pm.eventStreamWarned {
		return false
	}
	pm.eventStreamWarned = true
	return true
}

// NoteRuntimeEventStreamHealthy clears the down flag after a successful subscribe.
func (pm *ProcessManager) NoteRuntimeEventStreamHealthy() {
	if pm == nil {
		return
	}
	pm.reconcileMu.Lock()
	pm.runtimeEventStreamDegraded = false
	pm.reconcileMu.Unlock()
}

// NoteRuntimeEventStreamSettled allows the next outage to log again.
func (pm *ProcessManager) NoteRuntimeEventStreamSettled() {
	if pm == nil {
		return
	}
	pm.reconcileMu.Lock()
	pm.eventStreamWarned = false
	pm.reconcileMu.Unlock()
}

// ReconcileRuntimeEvent reconciles one workload from a container lifecycle event.
// Exit, OOM, and delete use the existing reconcile path. Start refreshes status
// and IP and does not compare the container spec. A restart wait that is already
// scheduled keeps its parked start; this wake does not enqueue that start.
func (pm *ProcessManager) ReconcileRuntimeEvent(uuid, kind string) {
	uuid = strings.TrimSpace(uuid)
	kind = strings.TrimSpace(kind)
	if pm == nil || uuid == "" || kind == "" {
		return
	}
	ev := runtimeEventSignal{uuid: uuid, kind: kind}
	if pm.runtimeEvents != nil && pm.reconcileMonitorIsRunning() && pm.ctx != nil {
		select {
		case pm.runtimeEvents <- ev:
		case <-pm.ctx.Done():
		}
		return
	}
	pm.deliverRuntimeEvent(ev)
}

func (pm *ProcessManager) deliverRuntimeEvent(ev runtimeEventSignal) {
	if pm == nil || strings.TrimSpace(ev.uuid) == "" {
		return
	}
	if IsQuiesced() {
		pm.addReconcileDirty(ev.uuid)
		return
	}
	switch ev.kind {
	case runtimeEventStart:
		pm.refreshStartedWorkload(ev.uuid)
	case runtimeEventExit, runtimeEventOOM, runtimeEventDelete:
		pm.reconcileWorkload(ev.uuid, nil)
	}
}

// refreshStartedWorkload reads status and IP once and clears the exit streak.
// It does not compare the container spec.
func (pm *ProcessManager) refreshStartedWorkload(uuid string) {
	if pm == nil || pm.engine == nil || pm.containerManager == nil {
		return
	}
	container, err := pm.containerManager.GetContainerForMicroservice(uuid)
	if err != nil || container == nil {
		return
	}
	status, err := pm.engine.GetContainerStatus(container.ID, uuid)
	if err != nil || status == nil {
		return
	}
	ip, ipErr := pm.engine.GetContainerIPAddress(container.ID)
	if ipErr != nil || strings.TrimSpace(ip) == "" {
		ip = "0.0.0.0"
	}
	if pm.microserviceManager != nil {
		if ms := pm.microserviceManager.FindLatestMicroserviceByUUID(uuid); ms != nil {
			ms.ContainerIPAddress = &ip
		}
	}
	status.IPAddress = &ip
	if status.Status == models.MicroserviceStateRunning {
		GetRestartStuckChecker().ObserveRunning(uuid)
	}
	pm.syncRuntimeStatus(uuid, status)
}
