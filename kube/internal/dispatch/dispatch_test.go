package dispatch

import (
	"errors"
	"testing"

	"github.com/eclipse-iofog/edgelet-kube/internal/bundle"
)

func TestParseServerUsesDefaultDataDir(t *testing.T) {
	got, err := Parse([]string{"edgelet-kube", "server"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if got.DataDir != bundle.DefaultDataDir {
		t.Fatalf("data dir %q", got.DataDir)
	}
	if len(got.Args) != 2 || got.Args[0] != "edgelet-kube" || got.Args[1] != "server" {
		t.Fatalf("args %#v", got.Args)
	}
}

func TestParseFlagWinsOverEnv(t *testing.T) {
	got, err := Parse([]string{"edgelet-kube", "--data-dir", "/from-flag", "server"}, "/from-env")
	if err != nil {
		t.Fatal(err)
	}
	if got.DataDir != "/from-flag" {
		t.Fatalf("data dir %q", got.DataDir)
	}
}

func TestParseDataDirEqualsForm(t *testing.T) {
	got, err := Parse([]string{"/usr/local/bin/edgelet-kube", "server", "--data-dir=/var/tmp/kube"}, "/from-env")
	if err != nil {
		t.Fatal(err)
	}
	if got.DataDir != "/var/tmp/kube" {
		t.Fatalf("data dir %q", got.DataDir)
	}
}

func TestParseLastDataDirWins(t *testing.T) {
	got, err := Parse([]string{"edgelet-kube", "server", "--data-dir", "/a", "--data-dir=/b"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if got.DataDir != "/b" {
		t.Fatalf("data dir %q", got.DataDir)
	}
}

func TestParseDataDirRequiresPath(t *testing.T) {
	_, err := Parse([]string{"edgelet-kube", "server", "--data-dir"}, "")
	if err == nil {
		t.Fatal("expected an error")
	}
}

func TestParseClientSubcommand(t *testing.T) {
	got, err := Parse([]string{"edgelet-kube", "--data-dir", "/data", "kubectl", "get", "pods"}, "")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"kubectl", "--data-dir", "/data", "get", "pods"}
	if len(got.Args) != len(want) {
		t.Fatalf("args %#v", got.Args)
	}
	for i := range want {
		if got.Args[i] != want[i] {
			t.Fatalf("args %#v", got.Args)
		}
	}
	if got.DataDir != "/data" {
		t.Fatalf("data dir %q", got.DataDir)
	}
}

func TestParseArgv0Client(t *testing.T) {
	args := []string{"/usr/local/bin/crictl", "ps"}
	got, err := Parse(args, "/data")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Args) != 2 || got.Args[0] != args[0] || got.Args[1] != "ps" {
		t.Fatalf("args %#v", got.Args)
	}
}

func TestParseUsage(t *testing.T) {
	for _, args := range [][]string{
		{"edgelet-kube"},
		{"edgelet-kube", "version"},
		{"edgelet-kube", "rotate"},
		{"edgelet-kube", "reencrypt"},
		nil,
	} {
		_, err := Parse(args, "")
		if !errors.Is(err, ErrUsage) {
			t.Fatalf("args %#v err %v", args, err)
		}
	}
}
