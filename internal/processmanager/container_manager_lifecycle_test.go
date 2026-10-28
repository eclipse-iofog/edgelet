package processmanager

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/eclipse-iofog/edgelet/internal/models"
	"github.com/eclipse-iofog/edgelet/internal/runtimeops"
	"github.com/eclipse-iofog/edgelet/internal/statusreporter"
	"github.com/eclipse-iofog/edgelet/internal/utils/logging"
	"github.com/eclipse-iofog/edgelet/internal/workloadmeta"
	"github.com/eclipse-iofog/edgelet/pkg/engine"
)

type lifecycleTestMSM struct {
	registry *models.Registry
}

func (m *lifecycleTestMSM) GetLatestMicroservices() []*models.Microservice           { return nil }
func (m *lifecycleTestMSM) GetCurrentMicroservices() []*models.Microservice          { return nil }
func (m *lifecycleTestMSM) FindLatestMicroserviceByUUID(string) *models.Microservice { return nil }
func (m *lifecycleTestMSM) GetRegistry(_ int) *models.Registry                       { return m.registry }
func (m *lifecycleTestMSM) SetCurrentMicroservices(_ []*models.Microservice)         {}

type lifecycleTestEngine struct {
	engine.ContainerEngine
	workload      *engine.Container
	createdID     string
	status        *models.MicroserviceStatus
	startErr      error
	createErr     error
	configDrifted bool
	startCalls    int
	pullErr       error
	missingImage  bool
	ops           []string
}

func (e *lifecycleTestEngine) GetContainer(msUUID string) (*engine.Container, error) {
	if e.workload == nil {
		return nil, nil
	}
	if workloadmeta.MicroserviceUIDFromLabels(e.workload.Labels) == msUUID {
		c := *e.workload
		return &c, nil
	}
	return nil, nil
}

func (e *lifecycleTestEngine) GetContainerByID(id string) (*engine.Container, error) {
	if e.workload != nil && e.workload.ID == id {
		c := *e.workload
		return &c, nil
	}
	return nil, nil
}

func (e *lifecycleTestEngine) GetContainerSandboxID(string) (string, error) { return "sandbox-1", nil }

func (e *lifecycleTestEngine) note(op string) {
	e.ops = append(e.ops, op)
}

func (e *lifecycleTestEngine) FindLocalImage(string, string, bool) (bool, error) {
	return !e.missingImage, nil
}

func (e *lifecycleTestEngine) PullImage(string, *models.Registry, *engine.PullImageOptions) error {
	e.note("pull")
	return e.pullErr
}

func (e *lifecycleTestEngine) CreateContainer(*models.Microservice, string) (string, error) {
	e.note("create")
	if e.createErr != nil {
		return "", e.createErr
	}
	e.createdID = "cid-new"
	e.workload = nil
	return e.createdID, nil
}

func (e *lifecycleTestEngine) GetContainerIPAddress(string) (string, error) { return "10.0.0.2", nil }

func (e *lifecycleTestEngine) GetContainerStatus(id, _ string) (*models.MicroserviceStatus, error) {
	st := e.status
	if st == nil {
		st = models.NewMicroserviceStatusWithState(models.MicroserviceStateRunning)
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

func (e *lifecycleTestEngine) GetContainerStats(string) (*engine.ContainerStats, error) {
	return &engine.ContainerStats{}, nil
}

func (e *lifecycleTestEngine) AreMicroserviceAndContainerEqual(string, *models.Microservice, *models.Registry) bool {
	return !e.configDrifted
}

func (e *lifecycleTestEngine) StartContainer(string) error {
	e.startCalls++
	return e.startErr
}

func (e *lifecycleTestEngine) StopContainer(string) error { return nil }

func (e *lifecycleTestEngine) RemoveContainer(string, bool) error {
	e.note("remove")
	e.workload = nil
	return nil
}

func (e *lifecycleTestEngine) RemoveImage(string) error { return nil }

func newLifecycleCM(eng *lifecycleTestEngine, reg *models.Registry) *ContainerManager {
	cm := NewContainerManager(eng, &lifecycleTestMSM{registry: reg}, "docker")
	cm.logger = logging.NewModuleLogger("test-container-manager")
	return cm
}

func captureEvents(t *testing.T) *[]runtimeops.RuntimeEvent {
	t.Helper()
	events := &[]runtimeops.RuntimeEvent{}
	var mu sync.Mutex
	runtimeops.SetTestSink(func(e runtimeops.RuntimeEvent) {
		mu.Lock()
		*events = append(*events, e)
		mu.Unlock()
	})
	t.Cleanup(func() { runtimeops.SetTestSink(nil) })
	return events
}

func eventNames(events []runtimeops.RuntimeEvent) []string {
	out := make([]string, len(events))
	for i := range events {
		out[i] = events[i].Event
	}
	return out
}

func containsSubsequence(got, want []string) bool {
	if len(want) == 0 {
		return true
	}
	i := 0
	for _, g := range got {
		if g == want[i] {
			i++
			if i == len(want) {
				return true
			}
		}
	}
	return false
}

func TestCreateContainer_EmitsOrderedLifecycleEvents(t *testing.T) {
	openLocalReconcileTestDB(t)
	t.Cleanup(func() { statusreporter.GetInstance().ResetProcessManagerStatus() })

	events := captureEvents(t)
	eng := &lifecycleTestEngine{}
	cm := newLifecycleCM(eng, &models.Registry{URL: "from_cache"})

	ms := models.NewMicroservice("ms-create", "nginx:latest")
	ms.RegistryID = 1
	ctx := runtimeops.WithOperation(context.Background(), "op-create", "docker", ms.MicroserviceUUID)

	if err := cm.createContainer(ctx, ms); err != nil {
		t.Fatalf("createContainer: %v", err)
	}

	want := []string{
		runtimeops.EventContainerPullCompleted,
		runtimeops.EventContainerCreating,
		runtimeops.EventContainerCreated,
		runtimeops.EventContainerStarting,
		runtimeops.EventContainerStarted,
	}
	if !containsSubsequence(eventNames(*events), want) {
		t.Fatalf("event sequence got %v want subsequence %v", eventNames(*events), want)
	}
	for _, e := range *events {
		if e.Event == runtimeops.EventContainerStarted && e.OperationID != "op-create" {
			t.Fatalf("operationId=%q on started", e.OperationID)
		}
		if e.Event == runtimeops.EventContainerStarted && e.ContainerID != "cid-new" {
			t.Fatalf("started event: containerId=%q", e.ContainerID)
		}
	}
}

func TestRemoveContainer_AlreadyGone_EmitsSkipped(t *testing.T) {
	openLocalReconcileTestDB(t)
	t.Cleanup(func() { statusreporter.GetInstance().ResetProcessManagerStatus() })

	events := captureEvents(t)
	cm := newLifecycleCM(&lifecycleTestEngine{}, &models.Registry{URL: "from_cache"})
	ctx := runtimeops.WithOperation(context.Background(), "op-rm", "docker", "ms-gone")

	if err := cm.RemoveContainerByMicroserviceUUID(ctx, "ms-gone", false, false); err != nil {
		t.Fatalf("remove: %v", err)
	}

	var removed *runtimeops.RuntimeEvent
	for i := range *events {
		if (*events)[i].Event == runtimeops.EventContainerRemoved {
			removed = &(*events)[i]
		}
		if (*events)[i].Event == runtimeops.EventContainerStopping {
			t.Fatal("unexpected stopping event when container already gone")
		}
	}
	if removed == nil || removed.Result != runtimeops.ResultSkipped {
		t.Fatalf("removed event=%+v", removed)
	}
}

func TestUpdateContainer_EmitsPhaseEvents(t *testing.T) {
	openLocalReconcileTestDB(t)
	t.Cleanup(func() { statusreporter.GetInstance().ResetProcessManagerStatus() })

	events := captureEvents(t)
	eng := &lifecycleTestEngine{
		workload: &engine.Container{
			ID:    "cid-old",
			Image: "nginx:old",
			Labels: map[string]string{
				workloadmeta.LabelAppManagedBy:    workloadmeta.ManagedByValue,
				workloadmeta.LabelMicroserviceUID: "ms-upd",
			},
		},
	}
	cm := newLifecycleCM(eng, &models.Registry{URL: "https://registry.example"})

	ms := models.NewMicroservice("ms-upd", "nginx:latest")
	ms.RegistryID = 1
	ctx := runtimeops.WithOperation(context.Background(), "op-upd", "docker", ms.MicroserviceUUID)

	if err := cm.UpdateContainer(ctx, ms, false); err != nil {
		t.Fatalf("update: %v", err)
	}

	want := []string{
		runtimeops.EventContainerUpdatePhase,
		runtimeops.EventContainerUpdatePhase,
		runtimeops.EventContainerUpdatePhase,
	}
	names := eventNames(*events)
	count := 0
	for _, n := range names {
		if n == runtimeops.EventContainerUpdatePhase {
			count++
		}
	}
	if count < 3 {
		t.Fatalf("expected 3 update.phase events, got %d in %v", count, names)
	}
	if !containsSubsequence(names, want) {
		t.Fatalf("missing update phase subsequence in %v", names)
	}
	assertPullOnceBeforeRemove(t, eng.ops)
}

func TestUpdateContainer_PullsOnceBeforeRemove(t *testing.T) {
	openLocalReconcileTestDB(t)
	t.Cleanup(func() { statusreporter.GetInstance().ResetProcessManagerStatus() })

	eng := &lifecycleTestEngine{
		workload: &engine.Container{
			ID:    "cid-old",
			Image: "nginx:old",
			Labels: map[string]string{
				workloadmeta.LabelAppManagedBy:    workloadmeta.ManagedByValue,
				workloadmeta.LabelMicroserviceUID: "ms-pull-once",
			},
		},
	}
	cm := newLifecycleCM(eng, &models.Registry{URL: "https://registry.example"})
	ms := models.NewMicroservice("ms-pull-once", "nginx:latest")
	ms.RegistryID = 1

	if err := cm.UpdateContainer(context.Background(), ms, false); err != nil {
		t.Fatalf("update: %v", err)
	}
	assertPullOnceBeforeRemove(t, eng.ops)
	if eng.createdID != "cid-new" {
		t.Fatalf("created id %q", eng.createdID)
	}
}

func TestUpdateContainer_FailedPullUsesCacheWithoutSecondPull(t *testing.T) {
	openLocalReconcileTestDB(t)
	t.Cleanup(func() { statusreporter.GetInstance().ResetProcessManagerStatus() })

	eng := &lifecycleTestEngine{
		pullErr: errors.New("registry down"),
		workload: &engine.Container{
			ID:    "cid-old",
			Image: "nginx:latest",
			Labels: map[string]string{
				workloadmeta.LabelAppManagedBy:    workloadmeta.ManagedByValue,
				workloadmeta.LabelMicroserviceUID: "ms-pull-fail",
			},
		},
	}
	cm := newLifecycleCM(eng, &models.Registry{URL: "https://registry.example"})
	ms := models.NewMicroservice("ms-pull-fail", "nginx:latest")
	ms.RegistryID = 1

	if err := cm.UpdateContainer(context.Background(), ms, false); err != nil {
		t.Fatalf("update: %v", err)
	}
	assertPullOnceBeforeRemove(t, eng.ops)
	if eng.createdID != "cid-new" {
		t.Fatalf("created id %q", eng.createdID)
	}
	got := statusreporter.GetInstance().GetProcessManagerStatus().LookupMicroserviceStatus("ms-pull-fail")
	if got != nil && got.ImagePullStatus != "" {
		t.Fatalf("cached image must omit imagePullStatus, got %q", got.ImagePullStatus)
	}
}

func TestUpdateContainer_MissingImageDoesNotStop(t *testing.T) {
	openLocalReconcileTestDB(t)
	t.Cleanup(func() { statusreporter.GetInstance().ResetProcessManagerStatus() })

	eng := &lifecycleTestEngine{
		pullErr:      errors.New("registry down"),
		missingImage: true,
		workload: &engine.Container{
			ID:    "cid-old",
			Image: "nginx:old",
			Labels: map[string]string{
				workloadmeta.LabelAppManagedBy:    workloadmeta.ManagedByValue,
				workloadmeta.LabelMicroserviceUID: "ms-missing",
			},
		},
	}
	cm := newLifecycleCM(eng, &models.Registry{URL: "https://registry.example"})
	ms := models.NewMicroservice("ms-missing", "nginx:new")
	ms.RegistryID = 1

	err := cm.UpdateContainer(context.Background(), ms, false)
	if err == nil {
		t.Fatal("expected update to fail when the image is not cached")
	}
	if !isImagePullMiss(err) || !strings.Contains(err.Error(), "registry down") {
		t.Fatalf("expected registry pull miss, got %v", err)
	}
	if countOp(eng.ops, "pull") != 1 {
		t.Fatalf("pulls=%v", eng.ops)
	}
	if countOp(eng.ops, "remove") != 0 || countOp(eng.ops, "create") != 0 {
		t.Fatalf("running container must stay, ops=%v", eng.ops)
	}
}

func TestAddContainer_StillPulls(t *testing.T) {
	openLocalReconcileTestDB(t)
	t.Cleanup(func() { statusreporter.GetInstance().ResetProcessManagerStatus() })

	eng := &lifecycleTestEngine{}
	cm := newLifecycleCM(eng, &models.Registry{URL: "https://registry.example"})
	ms := models.NewMicroservice("ms-add", "nginx:latest")
	ms.RegistryID = 1

	if err := cm.AddContainer(context.Background(), ms); err != nil {
		t.Fatalf("add: %v", err)
	}
	if countOp(eng.ops, "pull") != 1 || countOp(eng.ops, "create") != 1 {
		t.Fatalf("ops=%v", eng.ops)
	}
	if eng.createdID != "cid-new" {
		t.Fatalf("created id %q", eng.createdID)
	}
}

func assertPullOnceBeforeRemove(t *testing.T, ops []string) {
	t.Helper()
	if countOp(ops, "pull") != 1 {
		t.Fatalf("expected one pull, ops=%v", ops)
	}
	pullAt := opIndex(ops, "pull")
	removeAt := opIndex(ops, "remove")
	createAt := opIndex(ops, "create")
	if pullAt < 0 || removeAt < 0 || createAt < 0 || pullAt > removeAt || removeAt > createAt {
		t.Fatalf("expected pull, remove, create, ops=%v", ops)
	}
}

func opIndex(ops []string, name string) int {
	for i, op := range ops {
		if op == name {
			return i
		}
	}
	return -1
}

func countOp(ops []string, name string) int {
	n := 0
	for _, op := range ops {
		if op == name {
			n++
		}
	}
	return n
}
