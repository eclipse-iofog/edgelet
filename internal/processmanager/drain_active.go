package processmanager

import (
	"os"
	"path/filepath"
	"sync"

	"github.com/eclipse-iofog/edgelet/internal/constants"
	"github.com/eclipse-iofog/edgelet/internal/runtimestate"
	"github.com/eclipse-iofog/edgelet/internal/utils/logging"
)

const drainActiveMarkerContents = "active\n"

var (
	drainActiveMarkerMu   sync.RWMutex
	drainActiveMarkerPath = filepath.Join(constants.EdgeletRunDir, "drain-active")
	dataPlaneSocketReady  = defaultDataPlaneSocketReady
)

// SetDataPlaneDrainHoldPath overrides the hold file (tests only).
func SetDataPlaneDrainHoldPath(path string) {
	drainActiveMarkerMu.Lock()
	defer drainActiveMarkerMu.Unlock()
	drainActiveMarkerPath = path
}

func dataPlaneDrainHoldPath() string {
	drainActiveMarkerMu.RLock()
	defer drainActiveMarkerMu.RUnlock()
	return drainActiveMarkerPath
}

// BeginDataPlaneDrainHold tells the control plane to pause reconcile until the
// data-plane socket is back. The file is the signal; the API socket is not required.
func BeginDataPlaneDrainHold() error {
	path := dataPlaneDrainHoldPath()
	if path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil { // #nosec G301 -- hold file dir under /run/edgelet must be traversable
		return err
	}
	return os.WriteFile(path, []byte(drainActiveMarkerContents), 0o600)
}

// EndDataPlaneDrainHold clears the reconcile pause. Call it when drain did not
// stop containerd, or after the data plane is ready again.
func EndDataPlaneDrainHold() error {
	path := dataPlaneDrainHoldPath()
	if path == "" {
		return nil
	}
	err := os.Remove(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// DataPlaneDrainHoldActive reports whether a data-plane stop is in progress.
func DataPlaneDrainHoldActive() bool {
	path := dataPlaneDrainHoldPath()
	if path == "" {
		return false
	}
	_, err := os.Stat(path)
	return err == nil
}

func defaultDataPlaneSocketReady() bool {
	_, err := os.Stat(constants.EdgeletContainerdSocket)
	return err == nil
}

// ObserveDataPlaneDrainHold pauses reconcile while the hold file exists and
// resumes only after that file is gone and the containerd socket is present.
func ObserveDataPlaneDrainHold() {
	if DataPlaneDrainHoldActive() {
		if !IsQuiescedForDataPlaneDrain() {
			logging.LogInfo(ProcessManagerModuleName, "data-plane drain hold")
		}
		BeginQuiesceForDataPlaneDrain()
		runtimestate.GetState().SetEngineReady(false)
		return
	}
	if !IsQuiescedForDataPlaneDrain() {
		return
	}
	if !dataPlaneSocketReady() {
		return
	}
	TryResumeReconcileAfterDataPlaneEngineReady()
}
