package healthcheck

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/eclipse-iofog/edgelet/internal/config"
	"github.com/eclipse-iofog/edgelet/internal/containerexec"
	"github.com/eclipse-iofog/edgelet/internal/models"
	"github.com/eclipse-iofog/edgelet/internal/statusreporter"
	"github.com/eclipse-iofog/edgelet/internal/utils/logging"
	"github.com/eclipse-iofog/edgelet/internal/workloadmeta"
	"github.com/eclipse-iofog/edgelet/pkg/engine"
)

const moduleName = "HealthcheckRunner"

var log = logging.NewModuleLogger(moduleName)

// HealthcheckEngine is implemented by engines that support exec-based healthcheck
// (e.g. iofog). Docker/Podman use native healthcheck and do not implement this.
// HealthcheckEngine runs health checks against container engines.
//
//nolint:revive // exported API
type HealthcheckEngine interface {
	ExecWithExitCode(containerID string, cmd []string, timeout time.Duration) (int, string, error)
}

// MicroserviceProvider provides microservice lookup for healthcheck config.
type MicroserviceProvider interface {
	GetLatestMicroservices() []*models.Microservice
	FindLatestMicroserviceByUUID(uuid string) *models.Microservice
}

// Runner runs exec-based healthchecks for containers when using the iofog engine.
type Runner struct {
	engine              engine.ContainerEngine
	healthcheckEngine   HealthcheckEngine
	microserviceManager MicroserviceProvider
	statusReporter      *statusreporter.StatusReporter
	config              *config.Config
	ctx                 context.Context
	cancel              context.CancelFunc
	wg                  sync.WaitGroup
	// consecutiveFailures tracks failures per microservice for Retries logic
	consecutiveFailures map[string]int
	mu                  sync.Mutex
}

// NewRunner creates a healthcheck runner. healthcheckEngine may be nil if the
// engine does not support exec-based healthcheck (e.g. Docker).
func NewRunner(
	eng engine.ContainerEngine,
	healthcheckEngine HealthcheckEngine,
	microserviceManager MicroserviceProvider,
) *Runner {
	return &Runner{
		engine:              eng,
		healthcheckEngine:   healthcheckEngine,
		microserviceManager: microserviceManager,
		statusReporter:      statusreporter.GetInstance(),
		config:              config.GetInstance(),
		consecutiveFailures: make(map[string]int),
	}
}

// Start starts the healthcheck runner. No-op if healthcheckEngine is nil.
func (r *Runner) Start(ctx context.Context) error {
	if r.healthcheckEngine == nil {
		return nil
	}
	r.ctx, r.cancel = context.WithCancel(ctx)
	interval := r.config.HealthcheckIntervalSeconds
	if interval <= 0 {
		interval = 30
	}
	r.wg.Add(1)
	go r.run(time.Duration(interval) * time.Second)
	log.Info("Healthcheck runner started")
	return nil
}

// Stop stops the healthcheck runner.
func (r *Runner) Stop() error {
	if r.cancel != nil {
		r.cancel()
	}
	r.wg.Wait()
	return nil
}

func (r *Runner) run(interval time.Duration) {
	defer r.wg.Done()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-r.ctx.Done():
			return
		case <-ticker.C:
			r.runOnce()
		}
	}
}

func (r *Runner) runOnce() {
	containers, err := r.engine.GetRunningContainers()
	if err != nil {
		log.Warnf("Failed to get running containers for healthcheck: %v", err)
		return
	}
	log.Debugf("Healthcheck run: %d running containers (sandbox excluded)", len(containers))
	for _, c := range containers {
		msUUID := r.engine.GetContainerMicroserviceUUID(c)
		if msUUID == "" {
			log.Debugf("Healthcheck: skipping container %s (no microservice UUID)", c.ID)
			continue
		}
		ms := r.microserviceManager.FindLatestMicroserviceByUUID(msUUID)
		var hc *models.Healthcheck
		if ms != nil && ms.Healthcheck != nil {
			hc = ms.Healthcheck
		} else {
			hc = parseHealthcheckFromLabels(c.Labels)
		}
		if hc == nil || len(hc.Test) == 0 || (len(hc.Test) == 1 && (hc.Test[0] == "NONE" || hc.Test[0] == "")) {
			log.Debugf("Healthcheck %s: skipping (no healthcheck config)", msUUID)
			continue
		}
		cmd := buildHealthcheckCmd(hc)
		if len(cmd) == 0 {
			log.Debugf("Healthcheck %s: skipping (invalid healthcheck test)", msUUID)
			continue
		}
		timeout := 30 * time.Second
		if hc.Timeout != nil && *hc.Timeout > 0 {
			timeout = time.Duration(*hc.Timeout) * time.Second
		}
		startTime, _ := r.engine.GetContainerStartedAt(c.ID)
		startPeriod := int64(0)
		if hc.StartPeriod != nil {
			startPeriod = *hc.StartPeriod
		}
		elapsedSec := (time.Now().UnixMilli() - startTime) / 1000
		if elapsedSec < startPeriod {
			log.Debugf("Healthcheck %s: skipping (start period %ds, elapsed %ds)", msUUID, startPeriod, elapsedSec)
			continue // Still in start period, skip
		}
		log.Debugf("Healthcheck %s (container %s): exec cmd=%v", msUUID, c.ID, cmd)
		exitCode, output, err := r.healthcheckEngine.ExecWithExitCode(c.ID, cmd, timeout)
		healthy := err == nil && exitCode == 0
		if healthy {
			log.Debugf("Healthcheck %s: healthy (exit %d)", msUUID, exitCode)
		} else {
			log.Warnf("Healthcheck %s: exec failed (exit %d, err %v): %s", msUUID, exitCode, err, output)
		}
		r.mu.Lock()
		if healthy {
			r.consecutiveFailures[msUUID] = 0
		} else {
			r.consecutiveFailures[msUUID]++
			retries := 3
			if hc.Retries != nil && *hc.Retries > 0 {
				retries = *hc.Retries
			}
			if r.consecutiveFailures[msUUID] < retries {
				r.mu.Unlock()
				continue // Not enough failures yet
			}
		}
		r.mu.Unlock()
		healthStr := "healthy"
		if !healthy {
			healthStr = "unhealthy"
		}
		log.Infof("Healthcheck %s: %s", msUUID, healthStr)
		hs := healthStr
		r.statusReporter.UpdateProcessManagerStatus(func(pmStatus *models.ProcessManagerStatus) {
			pmStatus.SetMicroservicesHealthStatus(msUUID, &hs)
		})
	}
}

func buildHealthcheckCmd(hc *models.Healthcheck) []string {
	if len(hc.Test) < 2 {
		return nil
	}
	switch hc.Test[0] {
	case "CMD":
		return hc.Test[1:]
	case "CMD-SHELL":
		return containerexec.ShellCommandForScript(hc.Test[1])
	default:
		return hc.Test
	}
}

func parseHealthcheckFromLabel(label string) *models.Healthcheck {
	var hc models.Healthcheck
	if err := json.Unmarshal([]byte(label), &hc); err != nil {
		return nil
	}
	return &hc
}

func parseHealthcheckFromLabels(labels map[string]string) *models.Healthcheck {
	if labels == nil {
		return nil
	}
	label := labels[workloadmeta.LabelHealthcheck]
	if label == "" {
		return nil
	}
	return parseHealthcheckFromLabel(label)
}
