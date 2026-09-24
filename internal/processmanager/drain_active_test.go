package processmanager

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/eclipse-iofog/edgelet/internal/runtimestate"
)

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "edgelet-pm-drain-hold")
	if err != nil {
		panic(err)
	}
	SetDataPlaneDrainHoldPath(filepath.Join(dir, "drain-active"))
	m.Run()
	_ = os.RemoveAll(dir)
}

func TestDataPlaneDrainHold_PausesUntilSocketReturns(t *testing.T) {
	path := filepath.Join(t.TempDir(), "drain-active")
	prevPath := dataPlaneDrainHoldPath()
	prevReady := dataPlaneSocketReady
	SetDataPlaneDrainHoldPath(path)
	dataPlaneSocketReady = func() bool { return false }
	t.Cleanup(func() {
		SetDataPlaneDrainHoldPath(prevPath)
		dataPlaneSocketReady = prevReady
		SetQuiesced(false)
		runtimestate.GetState().SetEngineReady(false)
	})

	if err := BeginDataPlaneDrainHold(); err != nil {
		t.Fatalf("begin hold: %v", err)
	}
	ObserveDataPlaneDrainHold()
	if !IsQuiesced() || !IsQuiescedForDataPlaneDrain() {
		t.Fatal("expected reconcile paused while the drain hold file exists")
	}
	if runtimestate.GetState().EngineReady() {
		t.Fatal("expected engine not ready during the drain hold")
	}

	TryResumeReconcileAfterDataPlaneEngineReady()
	if !IsQuiescedForDataPlaneDrain() {
		t.Fatal("expected hold to keep reconcile paused while the file exists")
	}

	if err := EndDataPlaneDrainHold(); err != nil {
		t.Fatalf("end hold: %v", err)
	}
	ObserveDataPlaneDrainHold()
	if !IsQuiescedForDataPlaneDrain() {
		t.Fatal("expected pause to remain until the containerd socket is present")
	}

	dataPlaneSocketReady = func() bool { return true }
	runtimestate.GetState().SetEngineReady(false)
	ObserveDataPlaneDrainHold()
	if IsQuiesced() || IsQuiescedForDataPlaneDrain() {
		t.Fatal("expected reconcile to resume after the hold file is gone and the socket is present")
	}
	if !runtimestate.GetState().EngineReady() {
		t.Fatal("expected engine ready after resume")
	}
}
