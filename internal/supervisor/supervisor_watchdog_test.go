package supervisor

import (
	"path/filepath"
	"testing"

	"github.com/eclipse-iofog/edgelet/internal/processmanager"
)

func TestContainerdWatchdogShouldEscalateUnhealthy(t *testing.T) {
	processmanager.SetDataPlaneDrainHoldPath(filepath.Join(t.TempDir(), "drain-active"))
	processmanager.BeginQuiesceForDataPlaneDrain()
	t.Cleanup(func() { processmanager.SetQuiesced(false) })

	if containerdWatchdogShouldEscalateUnhealthy(3) {
		t.Fatal("expected no escalation while data-plane drain quiesce is active")
	}

	processmanager.SetQuiesced(false)
	if !containerdWatchdogShouldEscalateUnhealthy(3) {
		t.Fatal("expected escalation after failure threshold when not in data-plane drain")
	}
	if containerdWatchdogShouldEscalateUnhealthy(2) {
		t.Fatal("expected no escalation below failure threshold")
	}
}

func TestContainerdWatchdogShouldSkipEscalation(t *testing.T) {
	processmanager.SetDataPlaneDrainHoldPath(filepath.Join(t.TempDir(), "drain-active"))
	processmanager.BeginQuiesceForDataPlaneDrain()
	t.Cleanup(func() { processmanager.SetQuiesced(false) })

	if !containerdWatchdogShouldSkipEscalation(false) {
		t.Fatal("expected skip during data-plane quiesce")
	}
	if !containerdWatchdogShouldSkipEscalation(true) {
		t.Fatal("expected skip when attach-only during data-plane quiesce")
	}

	processmanager.SetQuiesced(false)
	if !containerdWatchdogShouldSkipEscalation(true) {
		t.Fatal("expected skip when attach-only")
	}
	if containerdWatchdogShouldSkipEscalation(false) {
		t.Fatal("expected no skip when monolithic and not quiesced")
	}
}

func TestContainerdWatchdogShouldSkipEscalation_HoldFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "drain-active")
	processmanager.SetDataPlaneDrainHoldPath(path)
	t.Cleanup(func() { processmanager.SetQuiesced(false) })
	if err := processmanager.BeginDataPlaneDrainHold(); err != nil {
		t.Fatalf("begin hold: %v", err)
	}
	if !containerdWatchdogShouldSkipEscalation(false) {
		t.Fatal("expected skip while the drain hold file exists")
	}
	if containerdWatchdogShouldEscalateUnhealthy(3) {
		t.Fatal("expected no escalation while the drain hold file exists")
	}
}
