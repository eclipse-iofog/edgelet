package resourceconsumption

import (
	"context"
	"testing"
	"time"

	"github.com/eclipse-iofog/edgelet/internal/buildmeta"
	"github.com/eclipse-iofog/edgelet/internal/config"
	"github.com/eclipse-iofog/edgelet/internal/constants"
	"github.com/eclipse-iofog/edgelet/internal/models"
	"github.com/eclipse-iofog/edgelet/internal/statusreporter"
)

type fakeProcessReader struct {
	cpuByPID map[int32]float64
	rssByPID map[int32]int64
}

func (f fakeProcessReader) processCPUPercent(_ context.Context, pid int32) float64 {
	return f.cpuByPID[pid]
}

func (f fakeProcessReader) processRSSBytes(pid int32) int64 {
	return f.rssByPID[pid]
}

func TestCollectUsageData_EmbeddedStackTotals(t *testing.T) {
	t.Cleanup(resetResourceConsumptionTestState())

	embedded := true
	buildmeta.SetHasEmbeddedEngineForTest(&embedded)
	t.Cleanup(func() { buildmeta.SetHasEmbeddedEngineForTest(nil) })

	cfg := config.GetInstance()
	cfg.ContainerEngine = constants.EngineEdgelet
	cfg.MemoryLimit = 4096
	cfg.CPULimit = 80

	agentPID := int32(1000)
	runtimePID := int32(2000)

	rcm := &Manager{
		config: cfg,
		processReader: fakeProcessReader{
			cpuByPID: map[int32]float64{agentPID: 3, runtimePID: 2},
			rssByPID: map[int32]int64{
				agentPID:   80 * 1024 * 1024,
				runtimePID: 70 * 1024 * 1024,
			},
		},
		hostCPUReader:    func(context.Context) float64 { return 1.25 },
		runtimePIDReader: func() []int { return []int{int(runtimePID)} },
		cpuHistory:       make(map[string][]float64),
	}
	rcm.ctx = context.Background()
	rcm.statusReporter = statusreporter.GetInstance()
	rcm.InstanceConfigUpdated()

	oldGetpid := getAgentPID
	getAgentPID = func() int32 { return agentPID }
	t.Cleanup(func() { getAgentPID = oldGetpid })

	rcm.collectUsageData()

	status := statusreporter.GetInstance().GetResourceConsumptionManagerStatus()
	if status.AgentCPUPercent != 3 {
		t.Fatalf("agent cpu: got %.2f want 3", status.AgentCPUPercent)
	}
	if status.RuntimeCPUPercent != 2 {
		t.Fatalf("runtime cpu: got %.2f want 2", status.RuntimeCPUPercent)
	}
	if status.CPUUsage != 5 {
		t.Fatalf("total cpu: got %.2f want 5", status.CPUUsage)
	}
	if status.AgentMemoryMiB < 79.9 || status.AgentMemoryMiB > 80.1 {
		t.Fatalf("agent memory: got %.2f want ~80", status.AgentMemoryMiB)
	}
	if status.RuntimeMemoryMiB < 69.9 || status.RuntimeMemoryMiB > 70.1 {
		t.Fatalf("runtime memory: got %.2f want ~70", status.RuntimeMemoryMiB)
	}
	if status.MemoryUsage < 149.9 || status.MemoryUsage > 150.1 {
		t.Fatalf("total memory: got %.2f want ~150", status.MemoryUsage)
	}
	if !status.RuntimeAvailable || status.RuntimeDegraded {
		t.Fatalf("runtime availability: available=%v degraded=%v", status.RuntimeAvailable, status.RuntimeDegraded)
	}
	if status.TotalCPU != 1.25 {
		t.Fatalf("host cpu: got %.2f want 1.25", status.TotalCPU)
	}
}

func TestCollectUsageData_ExternalEngineAgentOnly(t *testing.T) {
	t.Cleanup(resetResourceConsumptionTestState())

	embedded := true
	buildmeta.SetHasEmbeddedEngineForTest(&embedded)
	t.Cleanup(func() { buildmeta.SetHasEmbeddedEngineForTest(nil) })

	cfg := config.GetInstance()
	cfg.ContainerEngine = constants.EngineDocker
	cfg.MemoryLimit = 4096
	cfg.CPULimit = 80

	agentPID := int32(1000)

	rcm := &Manager{
		config: cfg,
		processReader: fakeProcessReader{
			cpuByPID: map[int32]float64{agentPID: 4},
			rssByPID: map[int32]int64{agentPID: 50 * 1024 * 1024},
		},
		hostCPUReader: func(context.Context) float64 { return 0.5 },
		runtimePIDReader: func() []int {
			t.Fatal("runtime PID lookup should not run for external engine")
			return nil
		},
		cpuHistory: make(map[string][]float64),
	}
	rcm.ctx = context.Background()
	rcm.statusReporter = statusreporter.GetInstance()
	rcm.InstanceConfigUpdated()

	oldGetpid := getAgentPID
	getAgentPID = func() int32 { return agentPID }
	t.Cleanup(func() { getAgentPID = oldGetpid })

	rcm.collectUsageData()

	status := statusreporter.GetInstance().GetResourceConsumptionManagerStatus()
	if status.RuntimeTracked {
		t.Fatal("expected runtime tracking disabled for docker engine")
	}
	if status.CPUUsage != 4 || status.MemoryUsage < 49.9 || status.MemoryUsage > 50.1 {
		t.Fatalf("unexpected totals: cpu=%.2f mem=%.2f", status.CPUUsage, status.MemoryUsage)
	}
	if status.RuntimeCPUPercent != 0 || status.RuntimeMemoryMiB != 0 {
		t.Fatalf("expected zero runtime metrics, got cpu=%.2f mem=%.2f", status.RuntimeCPUPercent, status.RuntimeMemoryMiB)
	}
}

func TestCollectUsageData_EmbeddedRuntimeMissingDegraded(t *testing.T) {
	t.Cleanup(resetResourceConsumptionTestState())

	embedded := true
	buildmeta.SetHasEmbeddedEngineForTest(&embedded)
	t.Cleanup(func() { buildmeta.SetHasEmbeddedEngineForTest(nil) })

	cfg := config.GetInstance()
	cfg.ContainerEngine = constants.EngineEdgelet

	agentPID := int32(1000)
	rcm := &Manager{
		config: cfg,
		processReader: fakeProcessReader{
			cpuByPID: map[int32]float64{agentPID: 2},
			rssByPID: map[int32]int64{agentPID: 40 * 1024 * 1024},
		},
		hostCPUReader:    func(context.Context) float64 { return 0.2 },
		runtimePIDReader: func() []int { return nil },
		cpuHistory:       make(map[string][]float64),
	}
	rcm.ctx = context.Background()
	rcm.statusReporter = statusreporter.GetInstance()
	rcm.InstanceConfigUpdated()

	oldGetpid := getAgentPID
	getAgentPID = func() int32 { return agentPID }
	t.Cleanup(func() { getAgentPID = oldGetpid })

	rcm.collectUsageData()

	status := statusreporter.GetInstance().GetResourceConsumptionManagerStatus()
	if !status.RuntimeDegraded || status.RuntimeAvailable {
		t.Fatalf("expected degraded missing runtime, got available=%v degraded=%v", status.RuntimeAvailable, status.RuntimeDegraded)
	}
	if status.CPUUsage != 2 {
		t.Fatalf("total cpu should equal agent only, got %.2f", status.CPUUsage)
	}
}

func TestCollectUsageData_DiskWalkCachedAndCPUDoesNotSleep(t *testing.T) {
	t.Cleanup(resetResourceConsumptionTestState())

	if cpuSampleInterval != 0 {
		t.Fatalf("cpu sample interval = %s; sampler must not sleep", cpuSampleInterval)
	}
	if diskWalkInterval != 60*time.Second {
		t.Fatalf("disk walk interval = %s want 60s", diskWalkInterval)
	}

	walks := 0
	agentPID := int32(1000)
	cfg := config.GetInstance()
	rcm := &Manager{
		config: cfg,
		processReader: fakeProcessReader{
			cpuByPID: map[int32]float64{agentPID: 1},
			rssByPID: map[int32]int64{agentPID: 10 * 1024 * 1024},
		},
		hostCPUReader:   func(context.Context) float64 { return 0.1 },
		cpuHistory:      make(map[string][]float64),
		directorySizeFn: func(string) int64 { walks++; return 42 },
	}
	rcm.ctx = context.Background()
	rcm.statusReporter = statusreporter.GetInstance()
	rcm.InstanceConfigUpdated()

	oldGetpid := getAgentPID
	getAgentPID = func() int32 { return agentPID }
	t.Cleanup(func() { getAgentPID = oldGetpid })

	rcm.collectUsageData()
	rcm.collectUsageData()
	if walks != 1 {
		t.Fatalf("directory walk ran %d times on the usage cadence, want 1", walks)
	}
}

func TestSmoothCPURollingAverage(t *testing.T) {
	rcm := &Manager{cpuHistory: make(map[string][]float64)}

	first := rcm.smoothCPU("total", 3)
	second := rcm.smoothCPU("total", 9)
	third := rcm.smoothCPU("total", 6)
	fourth := rcm.smoothCPU("total", 0)

	if first != 3 {
		t.Fatalf("first average: got %.2f want 3", first)
	}
	if second != 6 {
		t.Fatalf("second average: got %.2f want 6", second)
	}
	if third != 6 {
		t.Fatalf("third average: got %.2f want 6", third)
	}
	if fourth != 5 {
		t.Fatalf("fourth average: got %.2f want 5", fourth)
	}
}

func resetResourceConsumptionTestState() func() {
	statusreporter.GetInstance().UpdateResourceConsumptionManagerStatus(func(status *models.ResourceConsumptionManagerStatus) {
		*status = *models.NewResourceConsumptionManagerStatus()
	})
	return func() {}
}
