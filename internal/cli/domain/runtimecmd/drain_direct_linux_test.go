//go:build linux

package runtimecmd

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/eclipse-iofog/edgelet/internal/constants"
)

func TestResolveDirectDrainFatUsesCurrentWhenHashMatches(t *testing.T) {
	t.Parallel()

	want := "/var/lib/edgelet/data/current/bin/edgelet"
	staged := false
	got, err := resolveDirectDrainFat(directDrainEnv{
		Engine:    constants.EngineEdgelet,
		EmbedHash: "abc",
		Lookup: func(string, string) (string, bool, error) {
			return want, true, nil
		},
		Stage: func(string) (string, error) {
			staged = true
			return "", errors.New("must not stage")
		},
	})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got != want {
		t.Fatalf("path=%q want %q", got, want)
	}
	if staged {
		t.Fatal("hash match must exec data/current and must not stage a fat ELF")
	}
}

func TestResolveDirectDrainFatStagesTempELFWhenHashDiffers(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	dataRoot := filepath.Join(root, "data")
	installedHash := strings.Repeat("ab", 32)
	if err := os.MkdirAll(filepath.Join(dataRoot, installedHash, "bin"), 0o755); err != nil {
		t.Fatalf("mkdir bundle: %v", err)
	}
	current := filepath.Join(dataRoot, "current")
	if err := os.Symlink(installedHash, current); err != nil {
		t.Fatalf("symlink current: %v", err)
	}
	installedBin := filepath.Join(root, "usr-local-bin-edgelet")
	if err := os.WriteFile(installedBin, []byte("installed"), 0o755); err != nil {
		t.Fatalf("write installed: %v", err)
	}

	stageDir := filepath.Join(root, "runtime-drain")
	lookupCalled := false
	got, err := resolveDirectDrainFat(directDrainEnv{
		Engine:    constants.EngineEdgelet,
		EmbedHash: strings.Repeat("cd", 32),
		DataDir:   root,
		StageDir:  stageDir,
		Lookup: func(dataDir, embedHash string) (string, bool, error) {
			lookupCalled = true
			if dataDir != root {
				t.Fatalf("dataDir=%q", dataDir)
			}
			if embedHash != strings.Repeat("cd", 32) {
				t.Fatalf("embedHash=%q", embedHash)
			}
			return "", false, nil
		},
		Stage: func(dir string) (string, error) {
			if dir != stageDir {
				t.Fatalf("stage dir=%q want %q", dir, stageDir)
			}
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return "", err
			}
			path := filepath.Join(dir, "edgelet")
			if err := os.WriteFile(path, []byte("\x7fELFstaged"), 0o755); err != nil {
				return "", err
			}
			return path, nil
		},
	})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if !lookupCalled {
		t.Fatal("expected current lookup")
	}
	if got != filepath.Join(stageDir, "edgelet") {
		t.Fatalf("path=%q", got)
	}
	link, err := os.Readlink(current)
	if err != nil {
		t.Fatalf("readlink: %v", err)
	}
	if link != installedHash {
		t.Fatalf("current symlink = %q", link)
	}
	body, err := os.ReadFile(installedBin)
	if err != nil {
		t.Fatalf("read installed: %v", err)
	}
	if string(body) != "installed" {
		t.Fatalf("installed binary changed: %q", body)
	}
	if _, err := os.Stat(filepath.Join(dataRoot, strings.Repeat("cd", 32))); !os.IsNotExist(err) {
		t.Fatalf("hash mismatch must not create a bundle directory: %v", err)
	}
}

func TestResolveDirectDrainFatRejectsOtherEnginesWithoutStaging(t *testing.T) {
	t.Parallel()

	for _, engine := range []string{constants.EngineDocker, constants.EnginePodman, "Docker"} {
		staged := false
		_, err := resolveDirectDrainFat(directDrainEnv{
			Engine:    engine,
			EmbedHash: "abc",
			Lookup: func(string, string) (string, bool, error) {
				t.Fatal("must not look up a bundle")
				return "", false, nil
			},
			Stage: func(string) (string, error) {
				staged = true
				return "", nil
			},
		})
		if err == nil {
			t.Fatalf("engine %s: expected error", engine)
		}
		if staged {
			t.Fatalf("engine %s: must not unpack a fat ELF", engine)
		}
	}
}

func TestResolveDirectDrainFatRejectsMissingEmbedWithoutStaging(t *testing.T) {
	t.Parallel()

	staged := false
	_, err := resolveDirectDrainFat(directDrainEnv{
		Engine:    constants.EngineEdgelet,
		EmbedHash: "",
		Lookup: func(string, string) (string, bool, error) {
			t.Fatal("must not look up a bundle")
			return "", false, nil
		},
		Stage: func(string) (string, error) {
			staged = true
			return "", nil
		},
	})
	if err == nil {
		t.Fatal("expected error without an embedded runtime")
	}
	if staged {
		t.Fatal("must not unpack a fat ELF when the binary has no embed")
	}
}

func TestResolveDirectDrainFatLookupErrorDoesNotStage(t *testing.T) {
	t.Parallel()

	staged := false
	_, err := resolveDirectDrainFat(directDrainEnv{
		Engine:    constants.EngineEdgelet,
		EmbedHash: "abc",
		Lookup: func(string, string) (string, bool, error) {
			return "", false, errors.New("data directory unavailable")
		},
		Stage: func(string) (string, error) {
			staged = true
			return "", nil
		},
	})
	if err == nil {
		t.Fatal("expected lookup error")
	}
	if staged {
		t.Fatal("lookup failure must not unpack a fat ELF")
	}
}

func TestDirectDrainArgvForwardsTimeout(t *testing.T) {
	t.Parallel()

	got := directDrainArgv("/var/lib/edgelet/data/current/bin/edgelet", 45)
	want := []string{"edgelet", "runtime-drain", "--timeout", "45"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("argv=%q want %q", got, want)
	}
	got = directDrainArgv("/run/edgelet/runtime-drain/edgelet", 0)
	if got[len(got)-1] != "90" {
		t.Fatalf("default timeout argv=%q", got)
	}
}

func TestContainerEngineFromConfig(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	missing := filepath.Join(dir, "missing.yaml")
	if got := containerEngineFromConfig(missing); got != constants.EngineEdgelet {
		t.Fatalf("missing config engine=%q", got)
	}

	path := filepath.Join(dir, "config.yaml")
	body := "currentProfile: default\nprofiles:\n  default:\n    containerEngine: podman\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	if got := containerEngineFromConfig(path); got != constants.EnginePodman {
		t.Fatalf("engine=%q", got)
	}
}
