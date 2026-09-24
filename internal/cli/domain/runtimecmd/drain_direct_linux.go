//go:build linux

package runtimecmd

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/eclipse-iofog/edgelet/internal/constants"
	"github.com/eclipse-iofog/edgelet/internal/models"
	"github.com/eclipse-iofog/edgelet/internal/utils"
	"github.com/eclipse-iofog/edgelet/pkg/data"
	"github.com/eclipse-iofog/edgelet/pkg/datadir"
	"gopkg.in/yaml.v3"
)

// execFatDrain replaces this process with the fat runtime. Tests may stub it.
var execFatDrain = func(path string, argv []string, env []string) error {
	return syscall.Exec(path, argv, env) // #nosec G204 -- path is the ready current fat runtime or a staged ELF from the embedded bundle
}

type directDrainEnv struct {
	Engine    string
	EmbedHash string
	DataDir   string
	StageDir  string
	Lookup    func(dataDir, embedHash string) (string, bool, error)
	Stage     func(dir string) (string, error)
}

// ExecDirectDrain runs data-plane quiesce in the fat runtime.
// On success this process is replaced; the fat exit code is the result.
func ExecDirectDrain(timeoutSeconds int) error {
	if timeoutSeconds <= 0 {
		timeoutSeconds = defaultDrainTimeoutSecs
	}
	dataDir, stageDir, err := directDrainLocations(utils.ConfigYAMLPath)
	if err != nil {
		return err
	}
	path, err := resolveDirectDrainFat(directDrainEnv{
		Engine:    containerEngineFromConfig(utils.ConfigYAMLPath),
		EmbedHash: data.EmbeddedBundleHash(),
		DataDir:   dataDir,
		StageDir:  stageDir,
		Lookup:    data.ReadyCurrentRuntime,
		Stage:     data.StageDrainFatELF,
	})
	if err != nil {
		return err
	}
	argv := directDrainArgv(path, timeoutSeconds)
	if err := execFatDrain(path, argv, os.Environ()); err != nil {
		return fmt.Errorf("exec data-plane drain: %w", err)
	}
	return nil
}

func directDrainArgv(bin string, timeoutSeconds int) []string {
	if timeoutSeconds <= 0 {
		timeoutSeconds = defaultDrainTimeoutSecs
	}
	return []string{filepath.Base(bin), "runtime-drain", "--timeout", strconv.Itoa(timeoutSeconds)}
}

func resolveDirectDrainFat(env directDrainEnv) (string, error) {
	engine := strings.ToLower(strings.TrimSpace(env.Engine))
	switch engine {
	case constants.EngineDocker, constants.EnginePodman:
		return "", fmt.Errorf("data-plane drain is not available for container engine %s", engine)
	}
	if strings.TrimSpace(env.EmbedHash) == "" {
		return "", errors.New("data-plane drain requires an embedded runtime")
	}
	if env.Lookup == nil {
		return "", errors.New("data-plane drain could not locate the runtime")
	}
	current, ok, err := env.Lookup(env.DataDir, env.EmbedHash)
	if err != nil {
		return "", err
	}
	if ok {
		return current, nil
	}
	if env.Stage == nil {
		return "", errors.New("data-plane drain could not stage the runtime")
	}
	if err := ensureRuntimeDrainStageDir(env.StageDir); err != nil {
		return "", err
	}
	return env.Stage(env.StageDir)
}

// directDrainLocations returns the disk directory used to find data/current
// and the directory where a hash mismatch stages the fat runtime. Both use
// the same resolved diskDirectory from config.
func directDrainLocations(configPath string) (string, string, error) {
	resolved, err := datadir.Resolve(diskDirectoryFromConfig(configPath))
	if err != nil {
		return "", "", err
	}
	return resolved, filepath.Join(resolved, constants.RuntimeDrainStageRel), nil
}

func ensureRuntimeDrainStageDir(dir string) error {
	if strings.TrimSpace(dir) == "" {
		return errors.New("data-plane drain could not stage the runtime")
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("prepare data-plane drain runtime: %w", err)
	}
	if err := os.Chmod(dir, 0o750); err != nil { // #nosec G302 -- stage directory is private to the runtime and must stay executable
		return fmt.Errorf("prepare data-plane drain runtime: %w", err)
	}
	return nil
}

func containerEngineFromConfig(path string) string {
	return engineFromProfile(profileFromConfig(path))
}

func diskDirectoryFromConfig(path string) string {
	profile := profileFromConfig(path)
	if profile == nil {
		return constants.EdgeletDataDir
	}
	dir := strings.TrimSpace(profile.GetProperty("diskDirectory"))
	if dir == "" {
		return constants.EdgeletDataDir
	}
	return dir
}

func profileFromConfig(path string) *models.ProfileConfig {
	raw, err := os.ReadFile(path) // #nosec G304 -- operator config path supplied by the caller
	if err != nil {
		return nil
	}
	var doc models.YamlConfig
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return nil
	}
	name := strings.TrimSpace(doc.CurrentProfile)
	if name == "" {
		name = "default"
	}
	return doc.GetProfile(name)
}

func engineFromProfile(profile *models.ProfileConfig) string {
	if profile == nil {
		return constants.EngineEdgelet
	}
	engine := strings.ToLower(strings.TrimSpace(profile.GetProperty("containerEngine")))
	if engine == "" {
		return constants.EngineEdgelet
	}
	return engine
}
