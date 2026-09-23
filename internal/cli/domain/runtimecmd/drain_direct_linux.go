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
	path, err := resolveDirectDrainFat(directDrainEnv{
		Engine:    containerEngineFromConfig(utils.ConfigYAMLPath),
		EmbedHash: data.EmbeddedBundleHash(),
		StageDir:  filepath.Join(constants.EdgeletRunDir, "runtime-drain"),
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
	return env.Stage(env.StageDir)
}

func containerEngineFromConfig(path string) string {
	raw, err := os.ReadFile(path) // #nosec G304 -- operator config path supplied by the caller
	if err != nil {
		return constants.EngineEdgelet
	}
	var doc models.YamlConfig
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return constants.EngineEdgelet
	}
	name := strings.TrimSpace(doc.CurrentProfile)
	if name == "" {
		name = "default"
	}
	profile := doc.GetProfile(name)
	if profile == nil {
		return constants.EngineEdgelet
	}
	engine := strings.ToLower(strings.TrimSpace(profile.GetProperty("containerEngine")))
	if engine == "" {
		return constants.EngineEdgelet
	}
	return engine
}
