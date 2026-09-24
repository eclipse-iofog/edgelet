//go:build linux && cgo

package main

import (
	"fmt"
	"os"

	"github.com/eclipse-iofog/edgelet/internal/config"
	"github.com/eclipse-iofog/edgelet/internal/processmanager"
	"github.com/eclipse-iofog/edgelet/internal/utils"
	"github.com/eclipse-iofog/edgelet/pkg/containerd"
)

// runRuntimeDrain quiesces labeled workloads and records verification.
// It leaves containerd running and does not reap shims.
func runRuntimeDrain(args []string) int {
	if err := discardStaleDrainVerification(containerd.ClearDrainVerifiedMarker); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "data-plane drain: %v\n", err)
		return 1
	}
	if err := config.LoadConfig(utils.ConfigYAMLPath); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "data-plane drain: using default data directory: %v\n", err)
	}
	timeout := parseRuntimeDrainTimeout(args)
	if err := processmanager.BeginDataPlaneDrainHold(); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "data-plane drain: %v\n", err)
		return 1
	}
	// This command leaves containerd running, so the hold lasts only for the quiesce.
	// A following data-plane stop records its own hold until the next ready start.
	defer func() { _ = processmanager.EndDataPlaneDrainHold() }()
	outcome := quiesceDataPlaneViaCRI(timeout)
	return executeRuntimeDrain(outcome.complete, containerd.WriteDrainVerifiedMarker)
}
