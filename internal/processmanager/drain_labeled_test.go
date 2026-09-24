package processmanager

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type labeledDrainFake struct {
	mu                sync.Mutex
	running           map[string]struct{}
	stopRemoves       bool
	stopDelay         time.Duration
	stopCalls         int64
	activeStops       int64
	maxConcurrent     int64
	stopErrByID       map[string]error
	listErr           error
	recordStopTimeout *int64
}

func newLabeledDrainFake(ids ...string) *labeledDrainFake {
	running := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		running[id] = struct{}{}
	}
	return &labeledDrainFake{
		running:     running,
		stopRemoves: true,
		stopErrByID: make(map[string]error),
	}
}

func (f *labeledDrainFake) ListLabeledRunning(context.Context) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.listErr != nil {
		return nil, f.listErr
	}
	ids := make([]string, 0, len(f.running))
	for id := range f.running {
		ids = append(ids, id)
	}
	return ids, nil
}

func (f *labeledDrainFake) StopContainer(_ context.Context, id string, timeoutSec int64) error {
	if f.recordStopTimeout != nil {
		atomic.StoreInt64(f.recordStopTimeout, timeoutSec)
	}
	atomic.AddInt64(&f.stopCalls, 1)
	active := atomic.AddInt64(&f.activeStops, 1)
	defer atomic.AddInt64(&f.activeStops, -1)
	for {
		current := atomic.LoadInt64(&f.maxConcurrent)
		if active <= current {
			break
		}
		if atomic.CompareAndSwapInt64(&f.maxConcurrent, current, active) {
			break
		}
	}
	if f.stopDelay > 0 {
		time.Sleep(f.stopDelay)
	}
	if err := f.stopErrByID[id]; err != nil {
		return err
	}
	if f.stopRemoves {
		f.mu.Lock()
		delete(f.running, id)
		f.mu.Unlock()
	}
	return nil
}

func TestDrainLabeledWorkloads_EmptyRunningSetCompletes(t *testing.T) {
	fake := newLabeledDrainFake()
	start := time.Now()
	if err := DrainLabeledWorkloads(context.Background(), fake, time.Second, 10); err != nil {
		t.Fatalf("expected empty-set drain to complete, got: %v", err)
	}
	if took := time.Since(start); took > 200*time.Millisecond {
		t.Fatalf("expected empty-set drain under 200ms, got %s", took)
	}
	if got := atomic.LoadInt64(&fake.stopCalls); got != 0 {
		t.Fatalf("expected no StopContainer on empty set, got %d", got)
	}
}

func TestDrainLabeledWorkloads_StopsAllLabeledContainers(t *testing.T) {
	fake := newLabeledDrainFake("c1", "c2", "c3")
	if err := DrainLabeledWorkloads(context.Background(), fake, 0, 10); err != nil {
		t.Fatalf("expected labeled drain success, got: %v", err)
	}
	if got := atomic.LoadInt64(&fake.stopCalls); got < 3 {
		t.Fatalf("expected StopContainer for each labeled container, got %d", got)
	}
	remaining, err := fake.ListLabeledRunning(context.Background())
	if err != nil {
		t.Fatalf("list after drain: %v", err)
	}
	if len(remaining) != 0 {
		t.Fatalf("expected no remaining labeled containers, got %v", remaining)
	}
}

func TestDrainLabeledWorkloads_StopsConcurrentlyWithinBudget(t *testing.T) {
	ids := make([]string, 0, 8)
	for i := 0; i < 8; i++ {
		ids = append(ids, fmt.Sprintf("c%02d", i))
	}
	fake := newLabeledDrainFake(ids...)
	fake.stopDelay = 80 * time.Millisecond

	start := time.Now()
	if err := DrainLabeledWorkloads(context.Background(), fake, 2*time.Second, 10); err != nil {
		t.Fatalf("expected concurrent labeled drain success, got: %v", err)
	}
	elapsed := time.Since(start)
	serialFloor := time.Duration(len(ids)) * fake.stopDelay
	if elapsed >= serialFloor {
		t.Fatalf("expected concurrent stops under %s serial floor, took %s", serialFloor, elapsed)
	}
	if peak := atomic.LoadInt64(&fake.maxConcurrent); peak <= 1 {
		t.Fatalf("expected concurrent StopContainer workers, maxConcurrent=%d", peak)
	}
	if got := atomic.LoadInt64(&fake.stopCalls); got < int64(len(ids)) {
		t.Fatalf("expected StopContainer for all %d containers, got %d", len(ids), got)
	}
}

func TestDrainLabeledWorkloads_TimeoutWhenContainersRemain(t *testing.T) {
	fake := newLabeledDrainFake("c1", "c2")
	fake.stopRemoves = false
	err := DrainLabeledWorkloads(context.Background(), fake, 20*time.Millisecond, 10)
	if err == nil {
		t.Fatal("expected timeout when labeled containers remain")
	}
	if !IsLabeledWorkloadDrainTimeout(err) {
		t.Fatalf("expected drain timeout error, got: %v", err)
	}
	if got := atomic.LoadInt64(&fake.stopCalls); got == 0 {
		t.Fatal("expected StopContainer to be issued before timeout")
	}
}

func TestDrainLabeledWorkloads_NilRuntimeNoOp(t *testing.T) {
	if err := DrainLabeledWorkloads(context.Background(), nil, time.Second, 10); err != nil {
		t.Fatalf("expected nil runtime no-op, got: %v", err)
	}
}
