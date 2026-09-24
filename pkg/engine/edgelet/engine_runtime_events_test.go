//go:build linux

package edgelet

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	eventtypes "github.com/containerd/containerd/api/events"
	"github.com/containerd/containerd/v2/core/events"
	"github.com/containerd/typeurl/v2"
	"github.com/eclipse-iofog/edgelet/internal/processmanager"
	"github.com/eclipse-iofog/edgelet/internal/runtimeops"
)

func TestTaskExitContainerID_PrefersID(t *testing.T) {
	id := taskExitContainerID(&eventtypes.TaskExit{
		ID:          "container-abc",
		ContainerID: "other",
	})
	if id != "container-abc" {
		t.Fatalf("id=%q", id)
	}
}

func TestEmitContainerRuntimeWatchEvent_Fields(t *testing.T) {
	got := captureRuntimeEvents(t)
	e := &Engine{store: newStateStore()}
	e.store.set("cid-1", &containerState{sandboxID: "sandbox-1"})

	e.emitContainerRuntimeWatchEvent("cid-1", "ms-1", "exit", 137, "Error")

	if len(*got) != 1 {
		t.Fatalf("events=%d", len(*got))
	}
	ev := (*got)[0]
	if ev.Event != runtimeops.EventContainerRuntimeEvent {
		t.Fatalf("event=%s", ev.Event)
	}
	if ev.Level != runtimeops.LevelInfo {
		t.Fatalf("level=%s", ev.Level)
	}
	if ev.Source != runtimeops.SourceRuntimeWatch {
		t.Fatalf("source=%s", ev.Source)
	}
	if ev.Engine != edgeletEngineName {
		t.Fatalf("engine=%s", ev.Engine)
	}
	if ev.MsUUID != "ms-1" || ev.ContainerID != "cid-1" || ev.SandboxID != "sandbox-1" {
		t.Fatalf("msUUID=%q containerId=%q sandboxId=%q", ev.MsUUID, ev.ContainerID, ev.SandboxID)
	}
	if ev.Fields["runtimeStatus"] != "exit" {
		t.Fatalf("runtimeStatus=%v", ev.Fields["runtimeStatus"])
	}
	if ev.Fields["reason"] != "Error" {
		t.Fatalf("reason=%v", ev.Fields["reason"])
	}
	if ev.Fields["exitCode"] != int32(137) {
		t.Fatalf("exitCode=%v", ev.Fields["exitCode"])
	}
}

func TestEmitContainerRuntimeWatchEvent_OOM(t *testing.T) {
	got := captureRuntimeEvents(t)
	e := &Engine{store: newStateStore()}
	e.emitContainerRuntimeWatchEvent("cid-2", "ms-2", "oom", 0, "OOMKilled")

	ev := (*got)[0]
	if ev.Fields["runtimeStatus"] != "oom" {
		t.Fatalf("runtimeStatus=%v", ev.Fields["runtimeStatus"])
	}
	if _, has := ev.Fields["exitCode"]; has {
		t.Fatal("expected no exitCode for zero exit")
	}
}

func TestContainerEventEnvelope_WakesManagedWorkloadOnly(t *testing.T) {
	var lookups []string
	var wakes []string
	e := &Engine{
		store: newStateStore(),
		runtimeEventHooks: &runtimeEventHooks{
			lookup: func(_ context.Context, id string) (string, bool) {
				lookups = append(lookups, id)
				if id == "cid-managed" {
					return "ms-1", true
				}
				return "", false
			},
			wake: func(uuid, kind string) {
				wakes = append(wakes, kind+":"+uuid)
			},
		},
	}
	ctx := context.Background()
	e.handleContainerdEventEnvelope(ctx, mustEventEnvelope(t, &eventtypes.TaskExit{ID: "cid-managed", ContainerID: "cid-managed"}))
	e.handleContainerdEventEnvelope(ctx, mustEventEnvelope(t, &eventtypes.TaskExit{ID: "exec-1", ContainerID: "cid-managed"}))
	e.handleContainerdEventEnvelope(ctx, mustEventEnvelope(t, &eventtypes.TaskOOM{ContainerID: "cid-managed"}))
	e.handleContainerdEventEnvelope(ctx, mustEventEnvelope(t, &eventtypes.TaskDelete{ContainerID: "cid-managed"}))
	e.handleContainerdEventEnvelope(ctx, mustEventEnvelope(t, &eventtypes.TaskDelete{ID: "exec-9", ContainerID: "cid-managed"}))
	e.handleContainerdEventEnvelope(ctx, mustEventEnvelope(t, &eventtypes.TaskStart{ContainerID: "cid-managed"}))
	e.handleContainerdEventEnvelope(ctx, mustEventEnvelope(t, &eventtypes.TaskStart{ContainerID: "sandbox-1"}))
	e.handleContainerdEventEnvelope(ctx, mustEventEnvelope(t, &eventtypes.TaskOOM{ContainerID: "unmanaged"}))

	want := []string{"exit:ms-1", "oom:ms-1", "delete:ms-1", "start:ms-1"}
	if len(wakes) != len(want) {
		t.Fatalf("wakes=%v", wakes)
	}
	for i := range want {
		if wakes[i] != want[i] {
			t.Fatalf("wakes=%v", wakes)
		}
	}
	for _, id := range lookups {
		if id == "exec-1" || id == "exec-9" {
			t.Fatalf("exec event resolved %q; lookups=%v", id, lookups)
		}
	}
	if !containsString(lookups, "sandbox-1") || !containsString(lookups, "unmanaged") {
		t.Fatalf("sandbox and unmanaged containers must be checked and ignored, lookups=%v", lookups)
	}
}

func TestContainerEventStream_ErrorRestoresOnSubscribe(t *testing.T) {
	var mu sync.Mutex
	var logged []runtimeops.RuntimeEvent
	runtimeops.SetTestSink(func(ev runtimeops.RuntimeEvent) {
		mu.Lock()
		logged = append(logged, ev)
		mu.Unlock()
	})
	t.Cleanup(func() { runtimeops.SetTestSink(nil) })

	_ = processmanager.GetInstance()
	pm := &processmanager.ProcessManager{}
	restore := processmanager.SetInstanceForTest(pm)
	t.Cleanup(restore)

	var subs atomic.Int32
	var waits atomic.Int32
	resume := make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	e := &Engine{
		store: newStateStore(),
		runtimeEventHooks: &runtimeEventHooks{
			subscribe: func(ctx context.Context) (<-chan *events.Envelope, <-chan error) {
				n := subs.Add(1)
				if n < 3 {
					errCh := make(chan error, 1)
					errCh <- errors.New("subscribe failed")
					close(errCh)
					ch := make(chan *events.Envelope)
					close(ch)
					return ch, errCh
				}
				ch := make(chan *events.Envelope)
				errCh := make(chan error)
				go func() {
					<-ctx.Done()
					close(ch)
				}()
				return ch, errCh
			},
			wait: func(ctx context.Context) bool {
				if waits.Add(1) == 1 {
					return true
				}
				select {
				case <-ctx.Done():
					return false
				case <-resume:
					return true
				}
			},
		},
	}

	done := make(chan struct{})
	go func() {
		e.runContainerdRuntimeEventMonitor(ctx)
		close(done)
	}()

	waitUntil(t, func() bool {
		return pm.RuntimeEventStreamDegraded() && subs.Load() >= 2 && countWarnings(&mu, &logged, "event stream unavailable") == 1
	})
	close(resume)
	waitUntil(t, func() bool {
		return !pm.RuntimeEventStreamDegraded() && subs.Load() >= 3
	})
	if n := countWarnings(&mu, &logged, "event stream unavailable"); n != 1 {
		t.Fatalf("warnings=%d", n)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("event monitor did not stop")
	}
}

func mustEventEnvelope(t *testing.T, event any) *events.Envelope {
	t.Helper()
	anyEvent, err := typeurl.MarshalAny(event)
	if err != nil {
		t.Fatalf("marshal event: %v", err)
	}
	return &events.Envelope{Event: anyEvent}
}

func containsString(items []string, want string) bool {
	for _, item := range items {
		if item == want {
			return true
		}
	}
	return false
}

func countWarnings(mu *sync.Mutex, events *[]runtimeops.RuntimeEvent, message string) int {
	mu.Lock()
	defer mu.Unlock()
	n := 0
	for _, ev := range *events {
		if ev.Level == runtimeops.LevelWarn && ev.Message == message {
			n++
		}
	}
	return n
}

func waitUntil(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("condition was not met")
}
