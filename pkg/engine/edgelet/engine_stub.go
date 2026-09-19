//go:build !linux

// Package iofog provides a stub iofog engine for non-Linux platforms.
// The real implementation requires Linux (containerd, overlayfs, etc.).
//
//revive:disable:package-directory-mismatch
package edgelet

import (
	"context"
	"errors"
	"io"

	"github.com/eclipse-iofog/edgelet/internal/models"
	"github.com/eclipse-iofog/edgelet/pkg/engine"
)

// Engine is a stub on non-Linux platforms.
type Engine struct{}

// New returns a stub iofog engine.
func New(_ string) *Engine { return &Engine{} }

func (e *Engine) Init(_ engine.EngineConfig) error {
	return errors.New("iofog engine is only supported on Linux")
}
func (e *Engine) Close() error { return nil }
func (e *Engine) GetContainer(_ string) (*engine.Container, error) {
	return nil, errors.New("unsupported")
}
func (e *Engine) GetContainerByID(_ string) (*engine.Container, error) {
	return nil, errors.New("unsupported")
}
func (e *Engine) GetContainerSandboxID(_ string) (string, error) {
	return "", errors.New("unsupported")
}
func (e *Engine) GetRunningContainers() ([]engine.Container, error) {
	return nil, errors.New("unsupported")
}
func (e *Engine) GetAllContainers() ([]engine.Container, error) {
	return nil, errors.New("unsupported")
}
func (e *Engine) CreateContainer(_ *models.Microservice, _ string) (string, error) {
	return "", errors.New("unsupported")
}
func (e *Engine) StartContainer(_ string) error          { return errors.New("unsupported") }
func (e *Engine) StopContainer(_ string) error           { return errors.New("unsupported") }
func (e *Engine) KillContainer(_ string) error           { return errors.New("unsupported") }
func (e *Engine) RemoveContainer(_ string, _ bool) error { return errors.New("unsupported") }
func (e *Engine) PullImage(_ string, _ *models.Registry, _ *engine.PullImageOptions) error {
	return errors.New("unsupported")
}
func (e *Engine) FindLocalImage(_ string, _ string, _ bool) (bool, error) {
	return false, errors.New("unsupported")
}
func (e *Engine) RemoveImage(_ string) error { return errors.New("unsupported") }
func (e *Engine) PruneImages() error         { return errors.New("unsupported") }
func (e *Engine) GetContainerStatus(_, _ string) (*models.MicroserviceStatus, error) {
	return nil, errors.New("unsupported")
}
func (e *Engine) GetContainerStats(_ string) (*engine.ContainerStats, error) {
	return nil, errors.New("unsupported")
}
func (e *Engine) GetContainerIPAddress(_ string) (string, error) {
	return "", errors.New("unsupported")
}
func (e *Engine) GetContainerStartedAt(_ string) (int64, error) { return 0, errors.New("unsupported") }
func (e *Engine) TailContainerLogs(_, _, _ string, _ engine.LogTailHandler, _ *engine.TailConfig) error {
	return errors.New("unsupported")
}
func (e *Engine) AreMicroserviceAndContainerEqual(_ string, _ *models.Microservice, _ *models.Registry) bool {
	return false
}
func (e *Engine) EnsureNetwork(_ string) error { return errors.New("unsupported") }
func (e *Engine) CreateExecSession(_ string, _ string, _ []string) (string, error) {
	return "", errors.New("unsupported")
}
func (e *Engine) StartExecSession(_ string, _ io.Reader, _, _ io.Writer) error {
	return errors.New("unsupported")
}
func (e *Engine) GetExecSessionStatus(_ string) (bool, error) {
	return false, errors.New("unsupported")
}
func (e *Engine) GetExecSessionExitCode(_ string) (int, error) { return 0, errors.New("unsupported") }
func (e *Engine) ResizeExecSession(_ string, _, _ uint32) error {
	return errors.New("unsupported")
}
func (e *Engine) StopExecSession(_ string) error                         { return errors.New("unsupported") }
func (e *Engine) GetContainerMicroserviceUUID(_ engine.Container) string { return "" }
func (e *Engine) GetContainerName(_ engine.Container) string             { return "" }
func (e *Engine) ListImages(_ context.Context) ([]engine.ImageInfo, error) {
	return nil, errors.New("unsupported")
}
func (e *Engine) LoadImageFromPath(_ context.Context, _ string) ([]engine.LoadedImage, error) {
	return nil, errors.New("unsupported")
}
func (e *Engine) DeleteImage(_ context.Context, _ string) error { return errors.New("unsupported") }
func (e *Engine) PruneDangling(_ context.Context) (*engine.ImagePruneReport, error) {
	return nil, errors.New("unsupported")
}
func (e *Engine) PruneContainers(_ context.Context) (*engine.ContainerPruneReport, error) {
	return nil, errors.New("unsupported")
}
func (e *Engine) PruneVolumes(_ context.Context) (*engine.VolumePruneReport, error) {
	return nil, errors.New("unsupported")
}
func (e *Engine) RemoveNamedVolume(_ context.Context, name string) error {
	return discardNamedVolume(name)
}
func (e *Engine) InspectContainerRaw(_ string) (map[string]any, error) {
	return nil, errors.New("unsupported")
}
