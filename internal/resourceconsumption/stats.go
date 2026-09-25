package resourceconsumption

import (
	"context"
	"os"
	"runtime"
	"sync"
	"time"

	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/process"
)

const (
	// cpuSampleInterval is zero so collectUsageData does not sleep inside the sampler.
	cpuSampleInterval = time.Duration(0)
	cpuSmoothingSize  = 3
	collectTimeout    = 5 * time.Second
	diskWalkInterval  = 60 * time.Second
)

type cpuTimesSample struct {
	total float64
	at    time.Time
}

type hostCPUSample struct {
	total int64
	idle  int64
}

type processTimesReader interface {
	processCPUTimes(pid int32) (float64, bool)
}

type hostCPUReader func(ctx context.Context) float64
type runtimePIDReader func() []int

var getAgentPID = func() int32 {
	return int32(os.Getpid()) // #nosec G115 -- PID fits in int32 on supported platforms
}

type processMetricsReader interface {
	processCPUPercent(ctx context.Context, pid int32) float64
	processRSSBytes(pid int32) int64
}

type gopsutilProcessReader struct{}

func (gopsutilProcessReader) processCPUTimes(pid int32) (float64, bool) {
	proc, err := process.NewProcess(pid)
	if err != nil {
		return 0, false
	}
	times, err := proc.Times()
	if err != nil {
		return 0, false
	}
	return times.Total(), true
}

func (gopsutilProcessReader) processCPUPercent(ctx context.Context, pid int32) float64 {
	proc, err := process.NewProcessWithContext(ctx, pid)
	if err != nil {
		return 0
	}
	value, err := proc.PercentWithContext(ctx, cpuSampleInterval)
	if err != nil {
		return 0
	}
	return value
}

func (gopsutilProcessReader) processRSSBytes(pid int32) int64 {
	proc, err := process.NewProcess(pid)
	if err != nil {
		return 0
	}
	memInfo, err := proc.MemoryInfo()
	if err != nil {
		return 0
	}
	return int64(memInfo.RSS) // #nosec G115 -- RSS is below int64 max in practice
}

type edgeletUsageSample struct {
	agentCPU         float64
	runtimeCPU       float64
	hostCPU          float64
	agentRSS         int64
	runtimeRSS       int64
	runtimeAvailable bool
	runtimePIDCount  int
}

func (rcm *Manager) sampleEdgeletUsage(trackRuntime bool, runtimePIDs []int) edgeletUsageSample {
	baseCtx := rcm.ctx
	if baseCtx == nil {
		baseCtx = context.Background()
	}
	ctx, cancel := context.WithTimeout(baseCtx, collectTimeout)
	defer cancel()

	reader := rcm.processReader
	if reader == nil {
		reader = gopsutilProcessReader{}
	}

	var (
		wg     sync.WaitGroup
		mu     sync.Mutex
		sample edgeletUsageSample
	)

	wg.Add(1)
	go func() {
		defer wg.Done()
		agentPID := getAgentPID()
		agentCPU := rcm.sampleProcessCPU(ctx, reader, agentPID)
		agentRSS := reader.processRSSBytes(agentPID)
		mu.Lock()
		sample.agentCPU = agentCPU
		sample.agentRSS = agentRSS
		mu.Unlock()
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		hostCPU := rcm.sampleHostCPU(ctx)
		mu.Lock()
		sample.hostCPU = hostCPU
		mu.Unlock()
	}()

	if trackRuntime {
		sample.runtimePIDCount = len(runtimePIDs)
		sample.runtimeAvailable = len(runtimePIDs) > 0
		for _, pid := range runtimePIDs {
			pid := pid
			wg.Add(1)
			go func() {
				defer wg.Done()
				runtimeCPU := rcm.sampleProcessCPU(ctx, reader, int32(pid)) // #nosec G115 -- proc PIDs fit in int32
				runtimeRSS := reader.processRSSBytes(int32(pid))            // #nosec G115 -- proc PIDs fit in int32
				mu.Lock()
				sample.runtimeCPU += runtimeCPU
				sample.runtimeRSS += runtimeRSS
				mu.Unlock()
			}()
		}
	} else {
		sample.runtimeAvailable = true
	}

	wg.Wait()
	return sample
}

func (rcm *Manager) sampleProcessCPU(ctx context.Context, reader processMetricsReader, pid int32) float64 {
	if times, ok := reader.(processTimesReader); ok {
		return rcm.deltaProcessCPU(pid, times)
	}
	if reader == nil {
		return 0
	}
	return reader.processCPUPercent(ctx, pid)
}

func (rcm *Manager) deltaProcessCPU(pid int32, reader processTimesReader) float64 {
	total, ok := reader.processCPUTimes(pid)
	if !ok {
		return 0
	}
	now := time.Now()
	rcm.cpuHistoryMu.Lock()
	if rcm.procCPU == nil {
		rcm.procCPU = make(map[int32]cpuTimesSample)
	}
	prev, have := rcm.procCPU[pid]
	rcm.procCPU[pid] = cpuTimesSample{total: total, at: now}
	rcm.cpuHistoryMu.Unlock()
	if !have {
		return 0
	}
	elapsed := now.Sub(prev.at).Seconds()
	if elapsed <= 0 {
		return 0
	}
	delta := total - prev.total
	if delta < 0 {
		return 0
	}
	return (delta / elapsed) * 100
}

func (rcm *Manager) sampleHostCPU(ctx context.Context) float64 {
	if rcm.hostCPUReader != nil {
		return rcm.hostCPUReader(ctx)
	}
	if runtime.GOOS == "linux" {
		return rcm.getTotalCPULinux()
	}
	percentages, err := cpu.PercentWithContext(ctx, cpuSampleInterval, false)
	if err != nil || len(percentages) == 0 {
		return 0
	}
	return percentages[0]
}

func bytesToMiB(bytes int64) float64 {
	return float64(bytes) / (1024 * 1024)
}

func (rcm *Manager) smoothCPU(key string, value float64) float64 {
	rcm.cpuHistoryMu.Lock()
	defer rcm.cpuHistoryMu.Unlock()

	history := rcm.cpuHistory[key]
	history = append(history, value)
	if len(history) > cpuSmoothingSize {
		history = history[len(history)-cpuSmoothingSize:]
	}
	rcm.cpuHistory[key] = history

	sum := 0.0
	for _, item := range history {
		sum += item
	}
	return sum / float64(len(history))
}
