package fieldagent

import (
	"fmt"
	"runtime/debug"
	"sync"
	"time"

	"github.com/eclipse-iofog/edgelet/internal/models"
	"github.com/eclipse-iofog/edgelet/internal/utils/logging"
)

const initReloadDedupWindow = 120 * time.Second

type reconnectState struct {
	mu                      sync.Mutex
	connectGeneration       uint64
	lastReconcileGeneration uint64
	lastReconcileAt         time.Time
}

// noteControllerReachable increments connect generation when the controller transitions
// from NOT_CONNECTED or BROKEN_CERTIFICATE to reachable. Call before updating status to OK.
func (fa *FieldAgent) noteControllerReachable() bool {
	prior := fa.state.GetControllerStatus()
	if prior != models.ControllerStatusNotConnected && prior != models.ControllerStatusBrokenCertificate {
		return false
	}

	fa.reconnect.mu.Lock()
	fa.reconnect.connectGeneration++
	gen := fa.reconnect.connectGeneration
	fa.reconnect.mu.Unlock()

	logging.LogInfo(moduleName, fmt.Sprintf(
		"Controller reconnect detected (prior=%s); connect generation=%d",
		prior, gen,
	))
	return true
}

func (fa *FieldAgent) runControllerReconcileAsync() {
	go func() {
		defer func() {
			if r := recover(); r != nil {
				logging.LogError(moduleName, "Panic in controllerReconcile", fmt.Errorf("%v\n%s", r, debug.Stack()))
			}
		}()
		_ = fa.controllerReconcile()
	}()
}

// controllerReconcile live-refreshes registries, volume mounts, models, runtime classes, and microservices from Pot.
// Single-flight via reconcileMu; does not clear initialization.
func (fa *FieldAgent) controllerReconcile() error {
	if fa.controllerReconcileHook != nil {
		return fa.controllerReconcileHook()
	}

	fa.reconcileMu.Lock()
	defer fa.reconcileMu.Unlock()

	logging.LogInfo(moduleName, "Starting controller reconnect reconcile")

	if fa.NotProvisioned() || !fa.IsControllerConnected(false) {
		return nil
	}

	if err := fa.loadRegistries(false); err != nil {
		logging.LogWarn(moduleName, fmt.Sprintf("controller reconcile: load registries: %v", err))
	}

	func() {
		defer func() {
			if r := recover(); r != nil {
				logging.LogError(moduleName, fmt.Sprintf("Panic in loadVolumeMounts during reconcile: %v", r), fmt.Errorf("%v", r))
			}
		}()
		if err := fa.loadVolumeMounts(); err != nil {
			logging.LogWarn(moduleName, fmt.Sprintf("controller reconcile: load volume mounts: %v", err))
		}
	}()

	if err := fa.loadModels(false); err != nil {
		logging.LogWarn(moduleName, fmt.Sprintf("controller reconcile: load models: %v", err))
	}

	if err := fa.loadRuntimeClasses(false); err != nil {
		logging.LogWarn(moduleName, fmt.Sprintf("controller reconcile: load runtime classes: %v", err))
	}

	microservices, err := fa.loadMicroservices(false)
	if err != nil {
		logging.LogWarn(moduleName, fmt.Sprintf("controller reconcile: load microservices: %v", err))
	} else if err := fa.processMicroserviceConfig(microservices); err != nil {
		logging.LogWarn(moduleName, fmt.Sprintf("controller reconcile: process microservice config: %v", err))
	}

	fa.mu.RLock()
	pm := fa.processManager
	fa.mu.RUnlock()
	if pm != nil {
		pm.Update()
	}

	fa.reconnect.mu.Lock()
	fa.reconnect.lastReconcileGeneration = fa.reconnect.connectGeneration
	fa.reconnect.lastReconcileAt = time.Now()
	fa.reconnect.mu.Unlock()

	if fa.state.IsInitialization() {
		if err := fa.postFogConfig(); err != nil {
			logging.LogWarn(moduleName, fmt.Sprintf("controller reconcile: post fog config: %v", err))
		}
	}

	logging.LogInfo(moduleName, "Finished controller reconnect reconcile")
	return nil
}

// shouldSkipInitReload returns true when reconnect reconcile already refreshed init-path
// data for the current connect generation within initReloadDedupWindow.
func (fa *FieldAgent) shouldSkipInitReload() bool {
	if !fa.state.IsInitialization() {
		return false
	}

	fa.reconnect.mu.Lock()
	defer fa.reconnect.mu.Unlock()

	if fa.reconnect.lastReconcileGeneration != fa.reconnect.connectGeneration {
		return false
	}
	return time.Since(fa.reconnect.lastReconcileAt) < initReloadDedupWindow
}
