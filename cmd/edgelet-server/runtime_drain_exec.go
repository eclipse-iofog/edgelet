//go:build linux

package main

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
)

const defaultRuntimeDrainTimeoutSec = 90

func parseRuntimeDrainTimeout(args []string) int {
	timeout := defaultRuntimeDrainTimeoutSec
	for i := 0; i < len(args); i++ {
		arg := args[i]
		var raw string
		switch {
		case arg == "--timeout":
			if i+1 >= len(args) {
				return defaultRuntimeDrainTimeoutSec
			}
			raw = args[i+1]
			i++
		case strings.HasPrefix(arg, "--timeout="):
			raw = strings.TrimPrefix(arg, "--timeout=")
		default:
			continue
		}
		n, err := strconv.Atoi(raw)
		if err != nil || n <= 0 {
			return defaultRuntimeDrainTimeoutSec
		}
		timeout = n
	}
	return timeout
}

func discardStaleDrainVerification(reset func() error) error {
	if reset == nil {
		return fmt.Errorf("clear data-plane drain verification: %w", errDrainVerifyClear)
	}
	if err := reset(); err != nil {
		return fmt.Errorf("clear data-plane drain verification: %w", err)
	}
	return nil
}

var errDrainVerifyClear = errors.New("verification reset is unavailable")

// executeRuntimeDrain records a completed quiesce. It does not stop containerd
// and does not reap shims.
func executeRuntimeDrain(complete bool, write func() error) int {
	if !complete {
		_, _ = fmt.Fprint(os.Stderr, "data-plane drain did not verify\n")
		return 1
	}
	if write == nil {
		_, _ = fmt.Fprint(os.Stderr, "data-plane drain did not verify\n")
		return 1
	}
	if err := write(); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "data-plane drain: failed to record verification: %v\n", err)
		return 1
	}
	_, _ = fmt.Fprintln(os.Stdout, "Data-plane drain complete.")
	return 0
}
