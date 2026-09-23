//go:build linux

package main

import (
	"bytes"
	_ "embed"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/eclipse-iofog/edgelet/pkg/containerd"
)

//go:embed runtime_drain.go
var runtimeDrainSource string

func TestParseRuntimeDrainTimeout(t *testing.T) {
	t.Parallel()

	if got := parseRuntimeDrainTimeout(nil); got != 90 {
		t.Fatalf("default=%d", got)
	}
	if got := parseRuntimeDrainTimeout([]string{"--timeout", "45"}); got != 45 {
		t.Fatalf("timeout=%d", got)
	}
	if got := parseRuntimeDrainTimeout([]string{"--timeout=15"}); got != 15 {
		t.Fatalf("timeout=%d", got)
	}
	if got := parseRuntimeDrainTimeout([]string{"--timeout", "0"}); got != 90 {
		t.Fatalf("non-positive timeout=%d", got)
	}
}

func TestExecuteRuntimeDrainCompleteWritesMarker(t *testing.T) {
	path := filepath.Join(t.TempDir(), "drain-verified")
	prev := containerd.DrainVerifiedMarkerPath()
	containerd.SetDrainVerifiedMarkerPath(path)
	t.Cleanup(func() { containerd.SetDrainVerifiedMarkerPath(prev) })

	stdout, stderr := captureDrainOutput(t, func() {
		if code := executeRuntimeDrain(true, containerd.WriteDrainVerifiedMarker); code != 0 {
			t.Fatalf("exit=%d", code)
		}
	})
	if !containerd.HasDrainVerifiedMarker() {
		t.Fatal("expected drain-verified marker after complete quiesce")
	}
	if !strings.Contains(stdout, "Data-plane drain complete.") {
		t.Fatalf("stdout=%q", stdout)
	}
	if stderr != "" {
		t.Fatalf("stderr=%q", stderr)
	}
}

func TestExecuteRuntimeDrainIncompleteLeavesMarkerUnset(t *testing.T) {
	path := filepath.Join(t.TempDir(), "drain-verified")
	prev := containerd.DrainVerifiedMarkerPath()
	containerd.SetDrainVerifiedMarkerPath(path)
	t.Cleanup(func() { containerd.SetDrainVerifiedMarkerPath(prev) })

	wrote := false
	_, stderr := captureDrainOutput(t, func() {
		code := executeRuntimeDrain(false, func() error {
			wrote = true
			return nil
		})
		if code != 1 {
			t.Fatalf("exit=%d", code)
		}
	})
	if wrote {
		t.Fatal("incomplete drain must not record verification")
	}
	if containerd.HasDrainVerifiedMarker() {
		t.Fatal("incomplete drain must not leave a drain-verified marker")
	}
	if !strings.Contains(stderr, "did not verify") {
		t.Fatalf("stderr=%q", stderr)
	}
}

func TestExecuteRuntimeDrainWriteFailure(t *testing.T) {
	code := 0
	captureDrainOutput(t, func() {
		code = executeRuntimeDrain(true, func() error { return errors.New("disk full") })
	})
	if code != 1 {
		t.Fatalf("exit=%d", code)
	}
}

func TestDiscardStaleDrainVerification(t *testing.T) {
	path := filepath.Join(t.TempDir(), "drain-verified")
	prev := containerd.DrainVerifiedMarkerPath()
	containerd.SetDrainVerifiedMarkerPath(path)
	t.Cleanup(func() { containerd.SetDrainVerifiedMarkerPath(prev) })
	if err := containerd.WriteDrainVerifiedMarker(); err != nil {
		t.Fatalf("write marker: %v", err)
	}
	if err := discardStaleDrainVerification(containerd.ClearDrainVerifiedMarker); err != nil {
		t.Fatalf("clear: %v", err)
	}
	if containerd.HasDrainVerifiedMarker() {
		t.Fatal("stale marker should be removed before quiesce")
	}
}

func TestRuntimeDrainDoesNotStopContainerdOrReapShims(t *testing.T) {
	t.Parallel()

	text := runtimeDrainSource
	for _, banned := range []string{
		"svc.Stop",
		".Stop()",
		"ReapManaged",
		"reap-orphans",
		"promoteCurrent",
		"EnsureExtracted",
		"stopEmbeddedContainerd",
	} {
		if strings.Contains(text, banned) {
			t.Fatalf("runtime drain must not reference %s", banned)
		}
	}
	clearAt := strings.Index(text, "ClearDrainVerifiedMarker")
	quiesceAt := strings.Index(text, "quiesceDataPlaneViaCRI")
	if clearAt < 0 || quiesceAt < 0 || clearAt > quiesceAt {
		t.Fatal("runtime drain must clear verification before quiesce")
	}
	if !strings.Contains(text, "executeRuntimeDrain") {
		t.Fatal("runtime drain must record verification only after quiesce")
	}
}

func captureDrainOutput(t *testing.T, fn func()) (string, string) {
	t.Helper()
	oldOut, oldErr := os.Stdout, os.Stderr
	rOut, wOut, err := os.Pipe()
	if err != nil {
		t.Fatalf("stdout pipe: %v", err)
	}
	rErr, wErr, err := os.Pipe()
	if err != nil {
		t.Fatalf("stderr pipe: %v", err)
	}
	os.Stdout, os.Stderr = wOut, wErr
	defer func() {
		os.Stdout, os.Stderr = oldOut, oldErr
		_ = wOut.Close()
		_ = wErr.Close()
	}()
	fn()
	_ = wOut.Close()
	_ = wErr.Close()
	var outBuf, errBuf bytes.Buffer
	if _, err := outBuf.ReadFrom(rOut); err != nil {
		t.Fatalf("read stdout: %v", err)
	}
	if _, err := errBuf.ReadFrom(rErr); err != nil {
		t.Fatalf("read stderr: %v", err)
	}
	return outBuf.String(), errBuf.String()
}
