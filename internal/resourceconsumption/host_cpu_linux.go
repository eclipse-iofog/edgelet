//go:build linux

package resourceconsumption

import (
	"os"
	"strconv"
	"strings"

	"github.com/eclipse-iofog/edgelet/internal/utils/logging"
)

func (rcm *Manager) getTotalCPULinux() float64 {
	data, err := os.ReadFile("/proc/stat")
	if err != nil {
		logging.LogError(moduleName, "Error reading /proc/stat", err)
		return 0.0
	}

	lines := strings.Split(string(data), "\n")
	if len(lines) == 0 {
		return 0.0
	}

	firstLine := strings.TrimSpace(lines[0])
	if !strings.HasPrefix(firstLine, "cpu ") {
		return 0.0
	}

	parts := strings.Fields(firstLine)
	if len(parts) < 8 {
		return 0.0
	}

	user, _ := strconv.ParseInt(parts[1], 10, 64)
	nice, _ := strconv.ParseInt(parts[2], 10, 64)
	system, _ := strconv.ParseInt(parts[3], 10, 64)
	idle, _ := strconv.ParseInt(parts[4], 10, 64)
	iowait, _ := strconv.ParseInt(parts[5], 10, 64)
	irq, _ := strconv.ParseInt(parts[6], 10, 64)
	softirq, _ := strconv.ParseInt(parts[7], 10, 64)
	steal := int64(0)
	if len(parts) >= 9 {
		steal, _ = strconv.ParseInt(parts[8], 10, 64)
	}

	totalTime := user + nice + system + idle + iowait + irq + softirq + steal
	idleTime := idle + iowait
	if totalTime <= 0 {
		return 0.0
	}
	prev := rcm.lastHost
	have := rcm.haveHost
	rcm.lastHost = hostCPUSample{total: totalTime, idle: idleTime}
	rcm.haveHost = true
	if !have {
		return 0.0
	}
	dTotal := totalTime - prev.total
	dIdle := idleTime - prev.idle
	if dTotal <= 0 {
		return 0.0
	}
	return float64(dTotal-dIdle) / float64(dTotal) * 100.0
}
