package processmanager

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/eclipse-iofog/edgelet/internal/config"
	"github.com/eclipse-iofog/edgelet/internal/models"
	"github.com/eclipse-iofog/edgelet/internal/statusreporter"
	"github.com/eclipse-iofog/edgelet/internal/store"
	"github.com/eclipse-iofog/edgelet/internal/utils/logging"
	"github.com/eclipse-iofog/edgelet/pkg/engine"
)

type listMicroserviceManager struct {
	items []*models.Microservice
}

func (m *listMicroserviceManager) GetLatestMicroservices() []*models.Microservice {
	return m.items
}

func (m *listMicroserviceManager) GetCurrentMicroservices() []*models.Microservice {
	return nil
}

func (m *listMicroserviceManager) FindLatestMicroserviceByUUID(uuid string) *models.Microservice {
	for _, ms := range m.items {
		if ms != nil && ms.MicroserviceUUID == uuid {
			return ms
		}
	}
	return nil
}

func (m *listMicroserviceManager) GetRegistry(int) *models.Registry { return &models.Registry{} }

func (m *listMicroserviceManager) SetCurrentMicroservices([]*models.Microservice) {}

type reconcileTrace struct {
	mu  sync.Mutex
	seq []string
}

func (t *reconcileTrace) add(step string) {
	t.mu.Lock()
	t.seq = append(t.seq, step)
	t.mu.Unlock()
}

func (t *reconcileTrace) snapshot() []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]string, len(t.seq))
	copy(out, t.seq)
	return out
}

func recordingEngineOf(t *testing.T, pm *ProcessManager) *recordingEngine {
	t.Helper()
	eng, ok := pm.engine.(*recordingEngine)
	if !ok {
		t.Fatal("expected the test container engine")
	}
	return eng
}

type recordingEngine struct {
	lifecycleTestEngine
	mu           sync.Mutex
	byUUID       map[string]*engine.Container
	statusByUUID map[string]*models.MicroserviceStatus
	trace        *reconcileTrace
	specN        int
	statsN       int
	statusN      int
	stats        *engine.ContainerStats
	specFor      []string
}

func (e *recordingEngine) GetContainer(msUUID string) (*engine.Container, error) {
	if e.trace != nil {
		e.trace.add("container:" + msUUID)
	}
	e.mu.Lock()
	c := e.byUUID[msUUID]
	e.mu.Unlock()
	if c == nil {
		return nil, nil
	}
	cp := *c
	return &cp, nil
}

func (e *recordingEngine) GetContainerStatus(id, msUUID string) (*models.MicroserviceStatus, error) {
	e.mu.Lock()
	e.statusN++
	st := e.statusByUUID[msUUID]
	if st == nil {
		st = e.status
	}
	e.mu.Unlock()
	if st == nil {
		st = newRunningStatus(id)
	} else {
		cp := *st
		st = &cp
	}
	if strings.TrimSpace(st.ContainerID) == "" {
		st.ContainerID = id
	}
	if st.IPAddress == nil || strings.TrimSpace(*st.IPAddress) == "" {
		ip := "10.0.0.2"
		st.IPAddress = &ip
	}
	return st, nil
}

func (e *recordingEngine) GetContainerStats(string) (*engine.ContainerStats, error) {
	e.mu.Lock()
	e.statsN++
	st := e.stats
	e.mu.Unlock()
	if st == nil {
		return &engine.ContainerStats{}, nil
	}
	cp := *st
	return &cp, nil
}

func (e *recordingEngine) AreMicroserviceAndContainerEqual(_ string, ms *models.Microservice, _ *models.Registry) bool {
	e.mu.Lock()
	e.specN++
	if ms != nil {
		e.specFor = append(e.specFor, ms.MicroserviceUUID)
	}
	drifted := e.configDrifted
	e.mu.Unlock()
	return !drifted
}

type manualAfter struct {
	mu    sync.Mutex
	now   func() time.Time
	items []manualAfterItem
}

type manualAfterItem struct {
	due time.Time
	fn  func()
}

func (m *manualAfter) AfterFunc(d time.Duration, fn func()) *time.Timer {
	m.mu.Lock()
	m.items = append(m.items, manualAfterItem{due: m.now().Add(d), fn: fn})
	m.mu.Unlock()
	timer := time.NewTimer(time.Hour)
	timer.Stop()
	return timer
}

func (m *manualAfter) fireDue() {
	now := m.now()
	m.mu.Lock()
	due := make([]func(), 0)
	pending := make([]manualAfterItem, 0, len(m.items))
	for _, item := range m.items {
		if now.Before(item.due) {
			pending = append(pending, item)
			continue
		}
		due = append(due, item.fn)
	}
	m.items = pending
	m.mu.Unlock()
	for _, fn := range due {
		fn()
	}
}

func disableWatchdogForTest(t *testing.T) {
	t.Helper()
	cfg := config.GetInstance()
	orig := cfg.WatchdogEnabled
	cfg.WatchdogEnabled = false
	t.Cleanup(func() { cfg.WatchdogEnabled = orig })
}

func TestReconcileDirty_OneMarkDoesNotTouchOthers(t *testing.T) {
	pm, trace := newReconcileSchedulePM(t)
	pm.markReconcile("ms-a")
	pm.markReconcile("ms-a")
	select {
	case <-pm.updateChan:
	default:
		t.Fatal("marking a workload must wake the monitor")
	}

	pm.reconcileScheduled(&reconcileCycleStats{})
	got := trace.snapshot()
	if len(got) != 1 || got[0] != "container:ms-a" {
		t.Fatalf("only the marked workload should be reconciled, got %v", got)
	}
}

func TestReconcileDirty_IdleEdgeletTickDoesNotInspect(t *testing.T) {
	pm, trace := newReconcileSchedulePM(t)
	if idle := pm.reconcileScheduled(&reconcileCycleStats{}); !idle {
		t.Fatal("healthy edgelet tick with nothing dirty should not inspect")
	}
	if got := trace.snapshot(); len(got) != 0 {
		t.Fatalf("idle tick loaded containers: %v", got)
	}
	eng := recordingEngineOf(t, pm)
	if eng.specN != 0 || eng.statsN != 0 {
		t.Fatal("idle tick compared the spec or read usage")
	}
}

func TestReconcileDirty_BackoffDeadlineMarksThatWorkload(t *testing.T) {
	openLocalReconcileTestDB(t)
	clk := withRestartClock(t)
	pm := &ProcessManager{
		engineName:          "docker",
		ctx:                 context.Background(),
		taskQueue:           NewTaskQueue(4),
		logger:              logging.NewModuleLogger(ProcessManagerModuleName),
		updateChan:          make(chan struct{}, 1),
		microserviceManager: &listMicroserviceManager{},
	}
	after := &manualAfter{now: clk.Now}
	pm.reconcileDeadlineAfter = after.AfterFunc

	task := NewContainerTask(TaskActionAdd, "ms-backoff")
	pm.retryTask(task)
	if pm.reconcileIsDirty("ms-backoff") {
		t.Fatal("backoff must not mark the workload before the delay elapses")
	}

	clk.Advance(9 * time.Second)
	after.fireDue()
	pm.reconcileScheduled(&reconcileCycleStats{})
	if pm.reconcileIsDirty("ms-backoff") || len(drainTasks(pm.taskQueue)) != 0 {
		t.Fatal("backoff must not mark or start the workload before the delay elapses")
	}

	clk.Advance(time.Second)
	after.fireDue()
	if !pm.reconcileIsDirty("ms-backoff") {
		t.Fatal("backoff must mark the workload when the delay elapses")
	}
	select {
	case <-pm.updateChan:
	default:
		t.Fatal("backoff deadline must wake the monitor")
	}
	pm.reconcileScheduled(&reconcileCycleStats{})
	tasks := drainTasks(pm.taskQueue)
	if len(tasks) != 1 || tasks[0] != task {
		t.Fatalf("expected one parked start, got %#v", tasks)
	}
	pm.reconcileScheduled(&reconcileCycleStats{})
	if extra := drainTasks(pm.taskQueue); len(extra) != 0 {
		t.Fatalf("deadline must not enqueue a second start, got %d", len(extra))
	}
}

func TestReconcileDirty_VolumeHoldDeadlineMarksWhileCreateStaysBlocked(t *testing.T) {
	openLocalReconcileTestDB(t)
	clk := withRestartClock(t)
	pm, ms, eng := newVolumeHoldPM(t, "ms-hold-deadline")
	eng.workload = nil
	ms.VolumeMappings = []*models.VolumeMapping{
		models.NewVolumeMapping("data", "/var/lib/data", "rw", models.VolumeMappingTypeVolume),
	}
	presetRuntimeState(ms.MicroserviceUUID, models.MicroserviceStateCreated)
	setVolumeHolders(t, func([]string) ([]int, error) {
		return []int{4242}, nil
	})
	pm.updateChan = make(chan struct{}, 1)
	after := &manualAfter{now: clk.Now}
	pm.reconcileDeadlineAfter = after.AfterFunc

	pm.handleLatestMicroservices(&reconcileCycleStats{})
	if len(drainTasks(pm.taskQueue)) != 0 {
		t.Fatal("create must stay blocked while a host process holds the volume")
	}
	if pm.reconcileIsDirty(ms.MicroserviceUUID) {
		t.Fatal("volume hold must not mark the workload before the retry delay")
	}

	delay := reconcileTickInterval()
	clk.Advance(delay - time.Second)
	after.fireDue()
	if pm.reconcileIsDirty(ms.MicroserviceUUID) {
		t.Fatal("volume hold must not mark the workload early")
	}

	clk.Advance(time.Second)
	after.fireDue()
	if !pm.reconcileIsDirty(ms.MicroserviceUUID) {
		t.Fatal("volume hold must mark the workload again when the retry delay elapses")
	}
	pm.reconcileScheduled(&reconcileCycleStats{})
	if len(drainTasks(pm.taskQueue)) != 0 {
		t.Fatal("create must stay blocked while the holder still exists")
	}
	assertVolumeWait(t, ms.MicroserviceUUID, models.MicroserviceStateCreated)
}

func newReconcileSchedulePM(t *testing.T) (*ProcessManager, *reconcileTrace) {
	t.Helper()
	openLocalReconcileTestDB(t)
	disableWatchdogForTest(t)
	t.Cleanup(func() { statusreporter.GetInstance().ResetProcessManagerStatus() })

	msA := models.NewMicroservice("ms-a", "nginx:latest")
	msA.Schedule = 1
	msB := models.NewMicroservice("ms-b", "redis:latest")
	msB.Schedule = 2
	msm := &listMicroserviceManager{items: []*models.Microservice{msB, msA}}

	trace := &reconcileTrace{}
	eng := &recordingEngine{
		byUUID: map[string]*engine.Container{
			"ms-a": labeledWorkload("cid-a", "ms-a"),
			"ms-b": labeledWorkload("cid-b", "ms-b"),
		},
		trace: trace,
	}
	eng.status = newRunningStatus("cid")

	if err := store.GetInstance().UpsertSystemControlPlane(&models.ControlPlaneDeployment{
		ControllerUUID: "cp-1",
		Namespace:      "default",
		Name:           "pot",
		ManifestYAML:   minimalControlPlaneManifestYAML(),
		DesiredState:   "running",
		Generation:     1,
	}); err != nil {
		t.Fatalf("upsert control plane: %v", err)
	}
	if err := store.GetInstance().UpsertLocalWorkload(&models.LocalDeployedMicroservice{
		LocalUUID:        "local-1",
		ApplicationName:  "edgelet",
		MicroserviceName: "local-ms",
		ManifestYAML:     minimalLocalManifestYAML(),
		ImageName:        "busybox:latest",
		DesiredState:     "running",
		Generation:       1,
	}); err != nil {
		t.Fatalf("upsert local workload: %v", err)
	}

	pm := &ProcessManager{
		engineName:          "edgelet",
		ctx:                 context.Background(),
		logger:              logging.NewModuleLogger(ProcessManagerModuleName),
		engine:              eng,
		microserviceManager: msm,
		containerManager:    NewContainerManager(eng, msm, "docker"),
		taskQueue:           NewTaskQueue(4),
		updateChan:          make(chan struct{}, 1),
		launchControlPlaneFn: func(item *models.ControlPlaneDeployment, _ int64) {
			trace.add("control-plane")
			_ = item
		},
		launchLocalDeploymentFn: func(item *models.LocalDeployedMicroservice, _ int64) {
			trace.add("local:" + item.LocalUUID)
		},
	}
	return pm, trace
}

func TestReloadMarksEveryWorkloadOnce(t *testing.T) {
	pm, trace := newReconcileSchedulePM(t)
	pm.Update()
	for _, uuid := range []string{"cp-1", "ms-a", "ms-b", "local-1"} {
		if !pm.reconcileIsDirty(uuid) {
			t.Fatalf("reload did not mark %s", uuid)
		}
	}
	if idle := pm.reconcileScheduled(&reconcileCycleStats{}); idle {
		t.Fatal("reload must reconcile the marked workloads")
	}
	first := len(trace.snapshot())
	if first == 0 {
		t.Fatal("reload reconcile did not run")
	}
	if idle := pm.reconcileScheduled(&reconcileCycleStats{}); !idle {
		t.Fatal("the following tick should not inspect a healthy idle fleet")
	}
	if len(trace.snapshot()) != first {
		t.Fatalf("idle tick inspected again: %v", trace.snapshot())
	}
}

func TestFullSweepReconcilesEveryWorkload(t *testing.T) {
	pm, trace := newReconcileSchedulePM(t)
	pm.lastFullSweep = time.Now().Add(-fullReconcileInterval)
	if idle := pm.reconcileScheduled(&reconcileCycleStats{}); idle {
		t.Fatal("sweep should reconcile")
	}
	eng := recordingEngineOf(t, pm)
	eng.mu.Lock()
	specFor := append([]string(nil), eng.specFor...)
	eng.mu.Unlock()
	if !containsAll(specFor, "ms-a", "ms-b") {
		t.Fatalf("sweep should compare specs, got %v", specFor)
	}
	if got := trace.snapshot(); !containsAll(got, "local:local-1", "control-plane") {
		t.Fatalf("sweep should list stored workloads, got %v", got)
	}
	before := len(trace.snapshot())
	if idle := pm.reconcileScheduled(&reconcileCycleStats{}); !idle {
		t.Fatal("the next tick should wait for the following sweep")
	}
	if len(trace.snapshot()) != before {
		t.Fatal("second tick inspected before the sweep interval")
	}
}

func TestDockerStatusTickSkipsSpecWhenRunning(t *testing.T) {
	for _, engineName := range []string{"docker", "podman"} {
		t.Run(engineName, func(t *testing.T) {
			pm, _ := newReconcileSchedulePM(t)
			pm.engineName = engineName
			eng := recordingEngineOf(t, pm)
			eng.statusByUUID = map[string]*models.MicroserviceStatus{
				"ms-b": exitingStatus("cid-b", "exitCode=1"),
			}
			if idle := pm.reconcileScheduled(&reconcileCycleStats{}); idle {
				t.Fatal("status tick should notice a stopped container")
			}
			eng.mu.Lock()
			specFor := append([]string(nil), eng.specFor...)
			statsN := eng.statsN
			eng.mu.Unlock()
			if containsAll(specFor, "ms-a") {
				t.Fatalf("running container was spec-compared: %v", specFor)
			}
			if !containsAll(specFor, "ms-b") {
				t.Fatalf("stopped container was not reconciled: %v", specFor)
			}
			if statsN != 0 {
				t.Fatalf("status tick read usage %d times", statsN)
			}
		})
	}
}

func TestCatalogTransitionMarksBoundWorkloads(t *testing.T) {
	pm, _ := newReconcileSchedulePM(t)
	msA := pm.microserviceManager.FindLatestMicroserviceByUUID("ms-a")
	msA.Models = &models.ModelCatalog{
		BindPath: "/models",
		Items:    []models.ModelCatalogItem{{Name: "gemma3"}},
	}
	msB := pm.microserviceManager.FindLatestMicroserviceByUUID("ms-b")
	msB.Knowledge = &models.KnowledgeCatalog{
		BindPath: "/knowledge",
		Items:    []models.KnowledgeCatalogItem{{Name: "product-docs"}},
	}
	pm.markWorkloadsUsingCatalogItem("gemma3", models.ModelSourceManaged)
	if !pm.reconcileIsDirty("ms-a") || pm.reconcileIsDirty("ms-b") {
		t.Fatal("ready model should mark only the bound controller workload")
	}
	pm.markWorkloadsUsingCatalogItem("gemma3", models.ModelSourceLocal)
	if pm.reconcileIsDirty("ms-b") {
		t.Fatal("a local model must not wake a controller workload")
	}

	localYAML := minimalLocalManifestYAML() + "  models:\n    bindPath: /models\n    items:\n      - name: gemma3\n"
	if err := store.GetInstance().UpsertLocalWorkload(&models.LocalDeployedMicroservice{
		LocalUUID:        "local-1",
		ApplicationName:  "edgelet",
		MicroserviceName: "local-ms",
		ManifestYAML:     localYAML,
		ImageName:        "busybox:latest",
		DesiredState:     "running",
		Generation:       2,
	}); err != nil {
		t.Fatalf("upsert local: %v", err)
	}
	pm.markWorkloadsUsingCatalogItem("gemma3", models.ModelSourceLocal)
	if !pm.reconcileIsDirty("local-1") {
		t.Fatal("local model should mark the local workload that names it")
	}
	pm.markWorkloadsUsingCatalogItem("product-docs", models.KnowledgeSourceManaged)
	if !pm.reconcileIsDirty("ms-b") {
		t.Fatal("failed or ready knowledge should mark the bound controller workload")
	}
}

func TestQuiesceResumeRunsOneFullSweep(t *testing.T) {
	pm, trace := newReconcileSchedulePM(t)
	_ = GetInstance()
	restore := SetInstanceForTest(pm)
	t.Cleanup(restore)
	t.Cleanup(func() { SetQuiesced(false) })

	BeginQuiesceForDataPlaneDrain()
	pm.markReconcile("ms-a")
	if !pm.reconcileIsDirty("ms-a") {
		t.Fatal("a mark during quiesce should be held")
	}
	TryResumeReconcileAfterDataPlaneEngineReady()
	if IsQuiesced() {
		t.Fatal("engine ready should resume reconcile")
	}
	if idle := pm.reconcileScheduled(&reconcileCycleStats{}); idle {
		t.Fatal("resume should run one full sweep")
	}
	if !containsAll(trace.snapshot(), "container:ms-a", "container:ms-b", "local:local-1") {
		t.Fatalf("sweep missed a held or stored workload: %v", trace.snapshot())
	}
	before := len(trace.snapshot())
	if idle := pm.reconcileScheduled(&reconcileCycleStats{}); !idle {
		t.Fatal("resume should sweep once")
	}
	if len(trace.snapshot()) != before {
		t.Fatal("second tick inspected after the resume sweep")
	}
}

func containsAll(got []string, want ...string) bool {
	have := map[string]struct{}{}
	for _, item := range got {
		have[item] = struct{}{}
	}
	for _, item := range want {
		if _, ok := have[item]; !ok {
			return false
		}
	}
	return true
}
