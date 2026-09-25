package version

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/eclipse-iofog/edgelet/internal/models"
	"github.com/eclipse-iofog/edgelet/internal/statusreporter"
)

func TestParseVersionCommand(t *testing.T) {
	cmd, err := ParseVersionCommand(map[string]any{"command": "upgrade"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cmd != VersionCommandUpgrade {
		t.Fatalf("expected UPGRADE, got %q", cmd)
	}

	if _, err := ParseVersionCommand(map[string]any{"command": "invalid"}); err == nil {
		t.Fatal("expected error for unknown command")
	}
}

func TestIsReadyToUpgrade_RequiresInstallScriptAndHealthyDaemon(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "install.sh")
	receipt := filepath.Join(dir, "install-receipt")

	writeFile(t, script, "#!/bin/sh\n")
	writeKV(t, receipt, map[string]string{
		"installed_version": "1.0.0",
		"os":                "linux",
		"arch":              "amd64",
	})

	rm := NewReleaseManager(
		WithPaths(script, receipt, filepath.Join(dir, "previous-release"), filepath.Join(dir, "cache")),
		WithRunningVersion(func() string { return "1.0.0" }),
	)
	h := NewHandler(rm)
	h.isContainer = func() bool { return false }
	h.isDaemonHealthy = func() bool { return false }

	if h.IsReadyToUpgradeWithAction(map[string]any{"version": "2.0.0"}) {
		t.Fatal("expected not ready when daemon unhealthy")
	}

	h.isDaemonHealthy = func() bool { return true }
	if !h.IsReadyToUpgradeWithAction(map[string]any{"version": "2.0.0"}) {
		t.Fatal("expected ready when installed != target and daemon healthy")
	}

	if h.IsReadyToUpgradeWithAction(map[string]any{"version": "1.0.0"}) {
		t.Fatal("expected not ready when versions match")
	}

	_ = os.Remove(script)
	if h.IsReadyToUpgradeWithAction(map[string]any{"version": "2.0.0"}) {
		t.Fatal("expected not ready when install.sh missing")
	}
}

func TestDefaultDaemonHealthy_AcceptsWarning(t *testing.T) {
	sr := statusreporter.GetInstance()
	sr.UpdateSupervisorStatus(func(s *models.SupervisorStatus) {
		s.SetDaemonStatus(models.ModuleStatusWarning)
	})
	t.Cleanup(func() {
		sr.UpdateSupervisorStatus(func(s *models.SupervisorStatus) {
			s.SetDaemonStatus(models.ModuleStatusRunning)
		})
	})

	if !defaultDaemonHealthy() {
		t.Fatal("expected WARNING daemon to be operational for upgrade readiness")
	}
}

func TestIsReadyToUpgrade_BlocksDuringOTA(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "install.sh")
	receipt := filepath.Join(dir, "install-receipt")
	writeFile(t, script, "#!/bin/sh\n")
	writeKV(t, receipt, map[string]string{"installed_version": "1.0.0"})

	h := NewHandler(NewReleaseManager(
		WithPaths(script, receipt, filepath.Join(dir, "previous"), filepath.Join(dir, "cache")),
		WithRunningVersion(func() string { return "1.0.0" }),
	))
	h.isContainer = func() bool { return false }
	h.isDaemonHealthy = func() bool { return true }
	h.markOTAInProgress()

	if h.IsReadyToUpgradeWithAction(map[string]any{"version": "2.0.0"}) {
		t.Fatal("expected not ready during OTA")
	}
}

func TestIsReadyToRollback_RequiresCacheOrReachableURL(t *testing.T) {
	dir := t.TempDir()
	cacheDir := filepath.Join(dir, "cache")
	previous := filepath.Join(dir, "previous-release")
	_ = os.MkdirAll(cacheDir, 0o755)

	writeKV(t, previous, map[string]string{
		"previous_version":      "1.0.0",
		"previous_os":           "linux",
		"previous_arch":         "amd64",
		"previous_download_url": "",
	})

	rm := NewReleaseManager(
		WithPaths(filepath.Join(dir, "install.sh"), filepath.Join(dir, "receipt"), previous, cacheDir),
	)
	h := NewHandler(rm)
	h.isContainer = func() bool { return false }

	if h.IsReadyToRollback() {
		t.Fatal("expected not ready without cache or reachable url")
	}

	cached := filepath.Join(cacheDir, "edgelet-1.0.0-linux-amd64")
	writeFile(t, cached, "binary")
	if !h.IsReadyToRollback() {
		t.Fatal("expected ready when cached binary exists")
	}

	_ = os.Remove(cached)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)

	writeKV(t, previous, map[string]string{
		"previous_version":      "1.0.0",
		"previous_os":           "linux",
		"previous_arch":         "amd64",
		"previous_download_url": server.URL + "/edgelet-linux-amd64",
	})
	rm = NewReleaseManager(
		WithPaths(filepath.Join(dir, "install.sh"), filepath.Join(dir, "receipt"), previous, cacheDir),
		WithHTTPClient(server.Client()),
	)
	h = NewHandler(rm)
	h.isContainer = func() bool { return false }

	if !h.IsReadyToRollback() {
		t.Fatal("expected ready when previous_download_url is reachable")
	}
}

func TestIsReadyToUpgrade_ContainerComparesImageTag(t *testing.T) {
	h := NewHandler(NewReleaseManager(
		WithRunningVersion(func() string { return "1.2.3" }),
	))
	h.isContainer = func() bool { return true }

	if !h.IsReadyToUpgradeWithAction(map[string]any{"version": "2.0.0"}) {
		t.Fatal("expected container ready when running != target")
	}
	if h.IsReadyToUpgradeWithAction(map[string]any{"version": "1.2.3"}) {
		t.Fatal("expected container not ready when versions match")
	}
}

func TestExecuteChangeVersionScript_LaunchesDetachedInstallSh(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "install.sh")
	logFile := filepath.Join(dir, "invocation.log")
	pendingFile := filepath.Join(dir, "ota-reprovision-pending")
	writeFile(t, script, "#!/bin/sh\nprintf '%s\\n' \"$@\" >> \""+logFile+"\"\n")
	_ = os.Chmod(script, 0o755)
	SetOTAReprovisionPendingPath(pendingFile)
	t.Cleanup(func() { SetOTAReprovisionPendingPath("") })

	var mu sync.Mutex
	var launched []string
	setOTAInstallLogPath(filepath.Join(dir, "ota-install.log"))
	t.Cleanup(func() { setOTAInstallLogPath("") })

	prevReport := reportDetachedInstallExit
	exitDone := make(chan error, 1)
	reportDetachedInstallExit = func(err error) {
		exitDone <- err
	}
	t.Cleanup(func() { reportDetachedInstallExit = prevReport })

	h := NewHandler(NewReleaseManager(WithPaths(script, filepath.Join(dir, "receipt"), filepath.Join(dir, "prev"), filepath.Join(dir, "cache"))))
	h.isContainer = func() bool { return false }
	h.isDaemonHealthy = func() bool { return true }
	h.startDetached = func(path string, args ...string) error {
		mu.Lock()
		launched = append(launched, path+":"+strings.Join(args, ","))
		mu.Unlock()
		return defaultStartDetached(path, args...)
	}

	writeKV(t, filepath.Join(dir, "receipt"), map[string]string{"installed_version": "1.0.0"})
	expiryMilli := time.Now().Add(20 * time.Minute).UnixMilli()
	err := h.executeChangeVersionScript(
		VersionCommandUpgrade,
		map[string]any{
			"version":        "v2.0.0",
			"semver":         "2.0.0",
			"provisionKey":   "audit-key",
			"expirationTime": expiryMilli,
			"command":        "UPGRADE",
		},
		"audit-key",
	)
	if err != nil {
		t.Fatalf("executeChangeVersionScript failed: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(launched) != 1 {
		t.Fatalf("expected one detached launch, got %#v", launched)
	}
	if !strings.Contains(launched[0], "--upgrade") || !strings.Contains(launched[0], "--version=v2.0.0") {
		t.Fatalf("unexpected launch args: %q", launched[0])
	}

	pending, err := ReadOTAReprovisionPending()
	if err != nil {
		t.Fatalf("read pending: %v", err)
	}
	if pending == nil || pending.ProvisionKey != "audit-key" {
		t.Fatalf("expected pending file written, got %+v", pending)
	}

	select {
	case err := <-exitDone:
		if err != nil {
			t.Fatalf("detached install.sh exit: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for detached install.sh")
	}
}

func TestChangeVersion_ContainerSkipsInstallScript(t *testing.T) {
	var launched bool
	h := NewHandler(NewReleaseManager())
	h.isContainer = func() bool { return true }
	h.startDetached = func(string, ...string) error {
		launched = true
		return nil
	}

	if err := h.ChangeVersion(map[string]any{"command": "UPGRADE", "version": "2.0.0"}); err != nil {
		t.Fatalf("ChangeVersion failed: %v", err)
	}
	if launched {
		t.Fatal("expected container mode to skip install.sh")
	}
}

func TestNormalizeVersion(t *testing.T) {
	if normalizeVersion("v1.2.3") != "1.2.3" {
		t.Fatal("unexpected normalize result")
	}
}

func TestExecuteChangeVersionScript_NoPendingWithoutProvisionKey(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "install.sh")
	pendingFile := filepath.Join(dir, "ota-reprovision-pending")
	writeFile(t, script, "#!/bin/sh\n")
	_ = os.Chmod(script, 0o755)
	SetOTAReprovisionPendingPath(pendingFile)
	t.Cleanup(func() { SetOTAReprovisionPendingPath("") })

	h := NewHandler(NewReleaseManager(WithPaths(script, filepath.Join(dir, "receipt"), filepath.Join(dir, "prev"), filepath.Join(dir, "cache"))))
	h.startDetached = func(string, ...string) error { return nil }

	err := h.executeChangeVersionScript(
		VersionCommandUpgrade,
		map[string]any{"command": "UPGRADE", "version": "2.0.0"},
		"",
	)
	if err != nil {
		t.Fatalf("executeChangeVersionScript failed: %v", err)
	}
	if pending, _ := ReadOTAReprovisionPending(); pending != nil {
		t.Fatalf("expected no pending for manual-style upgrade, got %+v", pending)
	}
}

func TestExecuteChangeVersionScript_PreflightRefresh(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "install.sh")
	pendingFile := filepath.Join(dir, "ota-reprovision-pending")
	writeFile(t, script, "#!/bin/sh\n")
	_ = os.Chmod(script, 0o755)
	SetOTAReprovisionPendingPath(pendingFile)
	t.Cleanup(func() { SetOTAReprovisionPendingPath("") })

	var launched []string
	h := NewHandler(NewReleaseManager(WithPaths(script, filepath.Join(dir, "receipt"), filepath.Join(dir, "prev"), filepath.Join(dir, "cache"))))
	h.startDetached = func(_ string, args ...string) error {
		launched = append(launched, strings.Join(args, ","))
		return nil
	}
	h.refreshVersion = func() (map[string]any, error) {
		return map[string]any{
			"versionCommand": "upgrade",
			"provisionKey":   "fresh-key",
			"expirationTime": time.Now().Add(15 * time.Minute).UnixMilli(),
			"semver":         "2.0.0",
		}, nil
	}

	nearExpiryMilli := time.Now().Add(2 * time.Minute).UnixMilli()
	err := h.executeChangeVersionScript(
		VersionCommandUpgrade,
		map[string]any{
			"command":        "UPGRADE",
			"provisionKey":   "stale-key",
			"expirationTime": nearExpiryMilli,
			"semver":         "2.0.0",
		},
		"stale-key",
	)
	if err != nil {
		t.Fatalf("executeChangeVersionScript failed: %v", err)
	}
	pending, err := ReadOTAReprovisionPending()
	if err != nil {
		t.Fatalf("read pending: %v", err)
	}
	if pending == nil || pending.ProvisionKey != "fresh-key" {
		t.Fatalf("expected refreshed pending key, got %+v", pending)
	}
	if len(launched) != 1 || !strings.Contains(launched[0], "--version=v2.0.0") {
		t.Fatalf("unexpected launch args: %#v", launched)
	}
}

func TestIsReadyToRollbackWithAction_SemverMustMatchPrevious(t *testing.T) {
	dir := t.TempDir()
	cacheDir := filepath.Join(dir, "cache")
	previous := filepath.Join(dir, "previous-release")
	_ = os.MkdirAll(cacheDir, 0o755)
	writeKV(t, previous, map[string]string{
		"previous_version": "1.0.0",
		"previous_os":      "linux",
		"previous_arch":    "amd64",
	})
	writeFile(t, filepath.Join(cacheDir, "edgelet-1.0.0-linux-amd64"), "binary")

	rm := NewReleaseManager(WithPaths(filepath.Join(dir, "install.sh"), filepath.Join(dir, "receipt"), previous, cacheDir))
	h := NewHandler(rm)
	h.isContainer = func() bool { return false }

	if !h.IsReadyToRollbackWithAction(map[string]any{"semver": "1.0.0"}) {
		t.Fatal("expected ready when semver matches previous version")
	}
	if h.IsReadyToRollbackWithAction(map[string]any{"semver": "9.9.9"}) {
		t.Fatal("expected not ready when semver mismatches previous version")
	}
}

func TestDetachedInstallLogTruncatesEachAttempt(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "ota-install.log")
	setOTAInstallLogPath(logPath)
	t.Cleanup(func() { setOTAInstallLogPath("") })

	prev := reportDetachedInstallExit
	type result struct{ err error }
	ch := make(chan result, 1)
	reportDetachedInstallExit = func(err error) {
		ch <- result{err: err}
	}
	t.Cleanup(func() { reportDetachedInstallExit = prev })

	okScript := filepath.Join(dir, "ok.sh")
	badScript := filepath.Join(dir, "bad.sh")
	writeFile(t, okScript, "#!/bin/sh\nprintf 'attempt-one\\n'\n")
	writeFile(t, badScript, "#!/bin/sh\nprintf 'attempt-two\\n'\nexit 3\n")
	if err := os.Chmod(okScript, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(badScript, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(logPath, []byte("stale\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	waitExit := func() error {
		t.Helper()
		select {
		case got := <-ch:
			return got.err
		case <-time.After(5 * time.Second):
			t.Fatal("timed out waiting for install.sh")
		}
		return nil
	}

	if err := defaultStartDetached(okScript); err != nil {
		t.Fatalf("start: %v", err)
	}
	if err := waitExit(); err != nil {
		t.Fatalf("success exit: %v", err)
	}
	body, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "attempt-one\n" {
		t.Fatalf("log=%q", body)
	}

	if err := defaultStartDetached(badScript); err != nil {
		t.Fatalf("start: %v", err)
	}
	if err := waitExit(); err == nil {
		t.Fatal("expected non-zero install.sh exit")
	}
	body, err = os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "attempt-two\n" {
		t.Fatalf("truncated log=%q", body)
	}
	if strings.Contains(string(body), "attempt-one") || strings.Contains(string(body), "stale") {
		t.Fatalf("previous attempt remained in log: %q", body)
	}
}

func TestDefaultDaemonHealthy(t *testing.T) {
	// Smoke test: default hook reads supervisor status without panic.
	_ = defaultDaemonHealthy()
	_ = models.ModuleStatusRunning
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("writeFile(%s): %v", path, err)
	}
}

func writeKV(t *testing.T, path string, kv map[string]string) {
	t.Helper()
	var b strings.Builder
	for k, v := range kv {
		_, _ = b.WriteString(k)
		_, _ = b.WriteString("=")
		_, _ = b.WriteString(v)
		_, _ = b.WriteString("\n")
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		t.Fatalf("writeKV(%s): %v", path, err)
	}
}
