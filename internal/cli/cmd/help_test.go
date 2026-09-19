package cmd

import (
	"strings"
	"testing"
)

func TestHelp_DeployShowsLong(t *testing.T) {
	client := &fakeClient{running: true}
	stdout, stderr, code := runCLI(t, client, "deploy", "--help")
	if code != 0 {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	if !strings.Contains(stdout, "Apply or validate a local manifest") {
		t.Fatalf("expected deploy Long in help stdout, got stdout=%q", stdout)
	}
	if strings.Contains(stderr, bannerMarker) {
		t.Fatalf("expected no banner for deploy --help, got stderr=%q", stderr)
	}
}

func TestHelp_DeployShowsExamples(t *testing.T) {
	client := &fakeClient{running: true}
	stdout, _, code := runCLI(t, client, "deploy", "--help")
	if code != 0 {
		t.Fatalf("exit=%d stdout=%q", code, stdout)
	}
	if !strings.Contains(stdout, "Examples:") || !strings.Contains(stdout, "deploy -f microservice.yaml --dry-run") {
		t.Fatalf("expected deploy Examples in help stdout, got stdout=%q", stdout)
	}
}

func TestHelp_ConfigCertShowsLong(t *testing.T) {
	client := &fakeClient{running: true}
	stdout, _, code := runCLI(t, client, "config", "cert", "--help")
	if code != 0 {
		t.Fatalf("exit=%d stdout=%q", code, stdout)
	}
	if !strings.Contains(stdout, "base64-encoded PEM") {
		t.Fatalf("expected config cert Long in help stdout, got stdout=%q", stdout)
	}
}

func TestHelp_ConfigShowsExamples(t *testing.T) {
	client := &fakeClient{running: true}
	stdout, _, code := runCLI(t, client, "config", "--help")
	if code != 0 {
		t.Fatalf("exit=%d stdout=%q", code, stdout)
	}
	if !strings.Contains(stdout, "Examples:") || !strings.Contains(stdout, "edgelet config --controller-url") {
		t.Fatalf("expected config Examples in help stdout, got stdout=%q", stdout)
	}
}

func TestHelp_RootShowsLongAndBanner(t *testing.T) {
	client := &fakeClient{running: true}
	stdout, stderr, code := runCLI(t, client, "--help")
	if code != 0 {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	if !strings.Contains(stdout, "Local CLI for the Edgelet daemon") {
		t.Fatalf("expected root Long in help stdout, got stdout=%q", stdout)
	}
	if strings.Count(stderr, bannerMarker) != 1 {
		t.Fatalf("expected banner once on root --help, got %d in stderr=%q", strings.Count(stderr, bannerMarker), stderr)
	}
}

func TestHelp_BareInvocationShowsRootHelp(t *testing.T) {
	client := &fakeClient{running: true}
	stdout, stderr, code := runCLI(t, client)
	if code != 0 {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	if !strings.Contains(stdout, "Local CLI for the Edgelet daemon") {
		t.Fatalf("expected root Long on bare invocation, got stdout=%q", stdout)
	}
	if strings.Count(stderr, bannerMarker) != 1 {
		t.Fatalf("expected banner once on bare invocation, got %d in stderr=%q", strings.Count(stderr, bannerMarker), stderr)
	}
}

func TestHelp_MSLogsShowsFlagsAndID(t *testing.T) {
	client := &fakeClient{running: true}
	stdout, _, code := runCLI(t, client, "ms", "logs", "--help")
	if code != 0 {
		t.Fatalf("exit=%d stdout=%q", code, stdout)
	}
	for _, want := range []string{"--follow", "--tail", "logs <id>"} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("expected %q in ms logs help, got stdout=%q", want, stdout)
		}
	}
}

func TestHelp_DeprovisionShowsFlags(t *testing.T) {
	client := &fakeClient{running: true}
	stdout, _, code := runCLI(t, client, "deprovision", "--help")
	if code != 0 {
		t.Fatalf("exit=%d stdout=%q", code, stdout)
	}
	for _, want := range []string{"--scope", "--keep-local", "--purge-volumes", "WARNING:", "Examples:"} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("expected %q in deprovision help, got stdout=%q", want, stdout)
		}
	}
}

func TestHelp_ProvisionShowsLongAndExamples(t *testing.T) {
	client := &fakeClient{running: true}
	stdout, _, code := runCLI(t, client, "provision", "--help")
	if code != 0 {
		t.Fatalf("exit=%d stdout=%q", code, stdout)
	}
	if !strings.Contains(stdout, "provisioning key") || !strings.Contains(stdout, "Examples:") {
		t.Fatalf("expected provision Long/Examples in help stdout, got stdout=%q", stdout)
	}
}

func TestHelp_SystemShowsIntro(t *testing.T) {
	client := &fakeClient{running: true}
	stdout, _, code := runCLI(t, client, "system", "--help")
	if code != 0 {
		t.Fatalf("exit=%d stdout=%q", code, stdout)
	}
	if !strings.Contains(stdout, "Subcommands: status, info") {
		t.Fatalf("expected system group Long in help stdout, got stdout=%q", stdout)
	}
}

func TestHelp_SystemStopShowsWarning(t *testing.T) {
	client := &fakeClient{running: true}
	stdout, _, code := runCLI(t, client, "system", "stop", "--help")
	if code != 0 {
		t.Fatalf("exit=%d stdout=%q", code, stdout)
	}
	if !strings.Contains(stdout, "WARNING:") {
		t.Fatalf("expected stop warning in help stdout, got stdout=%q", stdout)
	}
}

func TestHelp_MSShowsExamples(t *testing.T) {
	client := &fakeClient{running: true}
	stdout, _, code := runCLI(t, client, "ms", "--help")
	if code != 0 {
		t.Fatalf("exit=%d stdout=%q", code, stdout)
	}
	if !strings.Contains(stdout, "Examples:") || !strings.Contains(stdout, "ms logs") {
		t.Fatalf("expected ms Examples in help stdout, got stdout=%q", stdout)
	}
}

func TestHelp_MSKillShowsWarning(t *testing.T) {
	client := &fakeClient{running: true}
	stdout, _, code := runCLI(t, client, "ms", "kill", "--help")
	if code != 0 {
		t.Fatalf("exit=%d stdout=%q", code, stdout)
	}
	if !strings.Contains(stdout, "WARNING:") {
		t.Fatalf("expected ms kill warning in help stdout, got stdout=%q", stdout)
	}
}

func TestHelp_MSRemoveShowsWarning(t *testing.T) {
	client := &fakeClient{running: true}
	stdout, _, code := runCLI(t, client, "ms", "rm", "--help")
	if code != 0 {
		t.Fatalf("exit=%d stdout=%q", code, stdout)
	}
	if !strings.Contains(stdout, "WARNING:") {
		t.Fatalf("expected ms rm warning in help stdout, got stdout=%q", stdout)
	}
	if !strings.Contains(stdout, "--cleanup") {
		t.Fatalf("expected --cleanup in ms rm help, got stdout=%q", stdout)
	}
}

func TestHelp_ImageShowsExamples(t *testing.T) {
	client := &fakeClient{running: true}
	stdout, _, code := runCLI(t, client, "image", "--help")
	if code != 0 {
		t.Fatalf("exit=%d stdout=%q", code, stdout)
	}
	if !strings.Contains(stdout, "Examples:") || !strings.Contains(stdout, "image load -f") {
		t.Fatalf("expected image Examples in help stdout, got stdout=%q", stdout)
	}
}

func TestHelp_ImageLoadShowsFileFlag(t *testing.T) {
	client := &fakeClient{running: true}
	stdout, _, code := runCLI(t, client, "image", "load", "--help")
	if code != 0 {
		t.Fatalf("exit=%d stdout=%q", code, stdout)
	}
	if !strings.Contains(stdout, "--file") && !strings.Contains(stdout, "-f") {
		t.Fatalf("expected file flag in image load help, got stdout=%q", stdout)
	}
}

func TestHelp_MSListShowsSourceFlag(t *testing.T) {
	client := &fakeClient{running: true}
	stdout, _, code := runCLI(t, client, "ms", "ls", "--help")
	if code != 0 {
		t.Fatalf("exit=%d stdout=%q", code, stdout)
	}
	if !strings.Contains(stdout, "--source") {
		t.Fatalf("expected --source in ms ls help, got stdout=%q", stdout)
	}
}

func TestHelp_ImagePullShowsImageRefAndShorthands(t *testing.T) {
	client := &fakeClient{running: true}
	stdout, _, code := runCLI(t, client, "image", "pull", "--help")
	if code != 0 {
		t.Fatalf("exit=%d stdout=%q", code, stdout)
	}
	for _, want := range []string{"pull <image-ref>", "registry-id", "-r", "platform", "-p"} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("expected %q in image pull help, got stdout=%q", want, stdout)
		}
	}
}

func TestHelp_RootListsCompletion(t *testing.T) {
	client := &fakeClient{running: true}
	stdout, _, code := runCLI(t, client, "--help")
	if code != 0 {
		t.Fatalf("exit=%d stdout=%q", code, stdout)
	}
	if !strings.Contains(stdout, "completion") {
		t.Fatalf("expected completion in root help, got stdout=%q", stdout)
	}
}

func TestHelp_CompletionShowsLongAndExamples(t *testing.T) {
	client := &fakeClient{running: true}
	stdout, _, code := runCLI(t, client, "completion", "--help")
	if code != 0 {
		t.Fatalf("exit=%d stdout=%q", code, stdout)
	}
	for _, want := range []string{
		"Generate shell completion scripts",
		"/etc/bash_completion.d/edgelet",
		"Examples:",
		"completion bash",
		"completion fish",
	} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("expected %q in completion help, got stdout=%q", want, stdout)
		}
	}
}

func TestHelp_CompletionBashShowsInstallExample(t *testing.T) {
	client := &fakeClient{running: true}
	stdout, _, code := runCLI(t, client, "completion", "bash", "--help")
	if code != 0 {
		t.Fatalf("exit=%d stdout=%q", code, stdout)
	}
	if !strings.Contains(stdout, "Examples:") || !strings.Contains(stdout, "bash_completion.d/edgelet") {
		t.Fatalf("expected bash install example in help, got stdout=%q", stdout)
	}
}

func TestHelp_AuthShowsIntroAndExamples(t *testing.T) {
	client := &fakeClient{running: true}
	stdout, _, code := runCLI(t, client, "auth", "--help")
	if code != 0 {
		t.Fatalf("exit=%d stdout=%q", code, stdout)
	}
	if !strings.Contains(stdout, "Subcommands: whoami, tokens, revoke") || !strings.Contains(stdout, "Examples:") {
		t.Fatalf("expected auth group help, got stdout=%q", stdout)
	}
}

func TestHelp_RegistryShowsIntroAndExamples(t *testing.T) {
	client := &fakeClient{running: true}
	stdout, _, code := runCLI(t, client, "registry", "--help")
	if code != 0 {
		t.Fatalf("exit=%d stdout=%q", code, stdout)
	}
	if !strings.Contains(stdout, "Subcommands: ls, inspect, rm") ||
		!strings.Contains(stdout, "--password-plain") ||
		!strings.Contains(stdout, "https://huggingface.co") {
		t.Fatalf("expected registry group help, got stdout=%q", stdout)
	}
}

func TestHelp_RuntimeClassShowsIntro(t *testing.T) {
	client := &fakeClient{running: true}
	stdout, _, code := runCLI(t, client, "runtimeclass", "--help")
	if code != 0 {
		t.Fatalf("exit=%d stdout=%q", code, stdout)
	}
	if !strings.Contains(stdout, "Subcommands: ls, inspect, rm") {
		t.Fatalf("expected runtimeclass group help, got stdout=%q", stdout)
	}
}

func TestHelp_SystemLogsShowsFlags(t *testing.T) {
	client := &fakeClient{running: true}
	stdout, _, code := runCLI(t, client, "system", "logs", "--help")
	if code != 0 {
		t.Fatalf("exit=%d stdout=%q", code, stdout)
	}
	for _, want := range []string{"--follow", "--tail", "--since"} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("expected %q in system logs help, got stdout=%q", want, stdout)
		}
	}
}

func TestHelp_RegistryInspectShowsPasswordPlainFlag(t *testing.T) {
	client := &fakeClient{running: true}
	stdout, _, code := runCLI(t, client, "registry", "inspect", "--help")
	if code != 0 {
		t.Fatalf("exit=%d stdout=%q", code, stdout)
	}
	if !strings.Contains(stdout, "--password-plain") {
		t.Fatalf("expected --password-plain in registry inspect help, got stdout=%q", stdout)
	}
}

func TestHelp_MSInspectShowsSummaryFlag(t *testing.T) {
	client := &fakeClient{running: true}
	stdout, _, code := runCLI(t, client, "ms", "inspect", "--help")
	if code != 0 {
		t.Fatalf("exit=%d stdout=%q", code, stdout)
	}
	if !strings.Contains(stdout, "--summary") {
		t.Fatalf("expected --summary in ms inspect help, got stdout=%q", stdout)
	}
}

func TestHelp_AuthRevokeShowsJTISyntax(t *testing.T) {
	client := &fakeClient{running: true}
	stdout, _, code := runCLI(t, client, "auth", "revoke", "--help")
	if code != 0 {
		t.Fatalf("exit=%d stdout=%q", code, stdout)
	}
	if !strings.Contains(stdout, "revoke <jti>") {
		t.Fatalf("expected revoke jti syntax in help, got stdout=%q", stdout)
	}
}

func TestHelp_ModelPullShowsSpecFlags(t *testing.T) {
	client := &fakeClient{running: true}
	stdout, _, code := runCLI(t, client, "model", "pull", "--help")
	if code != 0 {
		t.Fatalf("exit=%d stdout=%q", code, stdout)
	}
	for _, want := range []string{"pull <name>", "--repo", "--revision", "--registry", "-r", "--files", "--format"} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("expected %q in model pull help, got stdout=%q", want, stdout)
		}
	}
}

func TestHelp_ModelShowsSubcommands(t *testing.T) {
	client := &fakeClient{running: true}
	stdout, _, code := runCLI(t, client, "model", "--help")
	if code != 0 {
		t.Fatalf("exit=%d stdout=%q", code, stdout)
	}
	for _, want := range []string{"pull", "ls", "inspect", "prune", "rm"} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("expected %q in model help, got stdout=%q", want, stdout)
		}
	}
}

func TestHelp_DeployMentionsModelKind(t *testing.T) {
	client := &fakeClient{running: true}
	stdout, _, code := runCLI(t, client, "deploy", "--help")
	if code != 0 {
		t.Fatalf("exit=%d stdout=%q", code, stdout)
	}
	if !strings.Contains(stdout, "Model") || !strings.Contains(stdout, "model.yaml") {
		t.Fatalf("expected Model kind in deploy help, got stdout=%q", stdout)
	}
}

func TestHelp_SystemPruneShowsModeFlag(t *testing.T) {
	client := &fakeClient{running: true}
	stdout, _, code := runCLI(t, client, "system", "prune", "--help")
	if code != 0 {
		t.Fatalf("exit=%d stdout=%q", code, stdout)
	}
	if !strings.Contains(stdout, "--mode") {
		t.Fatalf("expected --mode in system prune help, got stdout=%q", stdout)
	}
	if !strings.Contains(stdout, "volume prune") {
		t.Fatalf("expected volume prune pointer in system prune help, got stdout=%q", stdout)
	}
}

func TestHelp_VolumeShowsSubcommands(t *testing.T) {
	client := &fakeClient{running: true}
	stdout, _, code := runCLI(t, client, "volume", "--help")
	if code != 0 {
		t.Fatalf("exit=%d stdout=%q", code, stdout)
	}
	for _, want := range []string{"ls", "rm", "prune", "--shared"} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("expected %q in volume help, got stdout=%q", want, stdout)
		}
	}
}
