package cmd

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/eclipse-iofog/edgelet/internal/cli/run"
)

type fakeClient struct {
	running   bool
	gets      map[string]map[string]any
	errs      map[string]error
	applyPoll int
}

func (f *fakeClient) IsDaemonRunning() bool {
	return f.running
}

func (f *fakeClient) Request(method, path string, _ any) (map[string]any, error) {
	key := method + " " + path
	if strings.Contains(path, ":apply/op-1") {
		f.applyPoll++
		switch f.applyPoll {
		case 1:
			return map[string]any{"status": "running", "stage": "persisting"}, nil
		case 2:
			return map[string]any{"status": "running", "stage": "pulling"}, nil
		default:
			return map[string]any{"status": "succeeded", "deploymentId": "dep-1", "stage": "done"}, nil
		}
	}
	if err, ok := f.errs[key]; ok {
		return nil, err
	}
	if data, ok := f.gets[key]; ok {
		return data, nil
	}
	if method == "POST" && path == "/v1/images:pull" {
		return map[string]any{"status": "running", "operationId": "pull-1"}, nil
	}
	if method == "GET" && strings.HasPrefix(path, "/v1/images:pull/") {
		return map[string]any{
			"status":        "succeeded",
			"resolvedImage": "docker.io/library/alpine:3.19",
			"engine":        "edgelet",
			"platform":      "linux/amd64",
			"operationId":   "pull-1",
		}, nil
	}
	if method == "POST" && path == "/v1/models:pull" {
		return map[string]any{"status": "running", "operationId": "model-pull-1", "name": "llama-2-7b-q2k"}, nil
	}
	if method == "GET" && strings.HasPrefix(path, "/v1/models:pull/") {
		return map[string]any{
			"status":      "succeeded",
			"name":        "llama-2-7b-q2k",
			"operationId": "model-pull-1",
			"progress":    100,
		}, nil
	}
	return map[string]any{}, nil
}

func TestSystemStatusJSONStdoutOnly(t *testing.T) {
	client := &fakeClient{
		running: true,
		gets: map[string]map[string]any{
			"GET /v1/system/status": {
				"edgeletDaemon":          "running",
				"connectionToController": "ok",
			},
		},
	}
	stdout, stderr, code := runCLI(t, client, "system", "status", "-o", "json")
	if code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, stderr)
	}
	if strings.TrimSpace(stderr) != "" {
		t.Fatalf("expected UX on stderr only, got stderr: %q", stderr)
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(stdout)), &decoded); err != nil {
		t.Fatalf("stdout is not valid JSON: %v (%q)", err, stdout)
	}
	if decoded["edgeletDaemon"] != "running" {
		t.Fatalf("unexpected payload: %#v", decoded)
	}
}

func TestVersionMatchesSystemVersionHuman(t *testing.T) {
	client := &fakeClient{
		running: true,
		gets: map[string]map[string]any{
			"GET /v1/system/version": {
				"version":                "1.2.3",
				"buildTime":              "2026-01-01",
				"gitCommit":              "deadbeef",
				"allowedContainerEngine": "edgelet",
			},
		},
	}
	flagOut, _, code := runCLI(t, client, "--version")
	if code != 0 {
		t.Fatalf("--version exit=%d", code)
	}
	sysOut, _, code := runCLI(t, client, "system", "version")
	if code != 0 {
		t.Fatalf("system version exit=%d", code)
	}
	if strings.TrimSpace(flagOut) != strings.TrimSpace(sysOut) {
		t.Fatalf("human version mismatch:\n--version=%q\nsystem version=%q", flagOut, sysOut)
	}
}

func TestVersionMatchesSystemVersionJSON(t *testing.T) {
	client := &fakeClient{
		running: true,
		gets: map[string]map[string]any{
			"GET /v1/system/version": {
				"version":   "1.2.3",
				"buildTime": "2026-01-01",
				"gitCommit": "deadbeef",
			},
		},
	}
	flagOut, _, _ := runCLI(t, client, "--version", "-o", "json")
	sysOut, _, _ := runCLI(t, client, "system", "version", "-o", "json")
	if strings.TrimSpace(flagOut) != strings.TrimSpace(sysOut) {
		t.Fatalf("json version mismatch:\n--version=%q\nsystem version=%q", flagOut, sysOut)
	}
}

func TestDaemonDownExit10(t *testing.T) {
	client := &fakeClient{running: false}
	_, stderr, code := runCLI(t, client, "system", "status")
	if code != run.ExitDaemonUnavailable {
		t.Fatalf("expected exit 10, got %d", code)
	}
	if !strings.Contains(stderr, "edgelet daemon") || !strings.Contains(stderr, "systemctl start edgelet") {
		t.Fatalf("expected daemon start guidance, got stderr=%q", stderr)
	}
}

func TestTopLevelCertRejected(t *testing.T) {
	client := &fakeClient{running: true}
	_, stderr, code := runCLI(t, client, "cert", "abc")
	if code != run.ExitInvalidArgument {
		t.Fatalf("expected exit 2, got %d stderr=%q", code, stderr)
	}
	if !strings.Contains(stderr, "config cert") {
		t.Fatalf("expected config cert hint, got stderr=%q", stderr)
	}
}

func TestTopLevelSwitchRejected(t *testing.T) {
	client := &fakeClient{running: true}
	_, stderr, code := runCLI(t, client, "switch", "dev")
	if code != run.ExitInvalidArgument {
		t.Fatalf("expected exit 2, got %d stderr=%q", code, stderr)
	}
	if !strings.Contains(stderr, "config switch") {
		t.Fatalf("expected config switch hint, got stderr=%q", stderr)
	}
}

func TestConfigSetSubcommandRejected(t *testing.T) {
	client := &fakeClient{running: true}
	_, _, code := runCLI(t, client, "config", "set", "networkInterface", "eth0")
	if code == 0 {
		t.Fatal("expected non-zero exit for config set")
	}
}

func TestLegacyTopLevelStatusRejected(t *testing.T) {
	client := &fakeClient{running: true}
	_, _, code := runCLI(t, client, "status")
	if code == 0 {
		t.Fatal("expected non-zero exit for legacy top-level status")
	}
}

func TestMSPsRejected(t *testing.T) {
	client := &fakeClient{running: true}
	_, stderr, code := runCLI(t, client, "ms", "ps")
	if code != run.ExitInvalidArgument {
		t.Fatalf("expected exit 2, got %d stderr=%q", code, stderr)
	}
	if !strings.Contains(stderr, "ms ls") {
		t.Fatalf("expected ms ls hint, got stderr=%q", stderr)
	}
}

func runCLI(t *testing.T, client run.EdgeletAPIClient, args ...string) (stdout, stderr string, exitCode int) {
	t.Helper()
	clearInteractiveEnv(t)

	oldFactory := newClient
	newClient = func() run.EdgeletAPIClient { return client }
	t.Cleanup(func() { newClient = oldFactory })

	SetBuildInfo("test-cli", "test-time", "test-commit")

	var outBuf, errBuf bytes.Buffer
	root := newRootCommand()
	root.SetOut(&outBuf)
	root.SetErr(&errBuf)
	root.SetArgs(args)
	appCtx = nil

	if err := root.Execute(); err != nil {
		writeCommandError(err)
		return outBuf.String(), errBuf.String(), run.ExitCodeForError(err)
	}
	return outBuf.String(), errBuf.String(), run.ExitSuccess
}

func clearInteractiveEnv(t *testing.T) {
	t.Helper()
	t.Setenv("CI", "")
	t.Setenv("NO_COLOR", "")
	t.Setenv("TERM", "xterm-256color")
	_ = os.Unsetenv("NO_COLOR")
}
