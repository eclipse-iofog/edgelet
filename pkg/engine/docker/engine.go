// Package docker implements the ContainerEngine interface backed by the Docker daemon.
// It wraps the existing pkg/docker client so all Docker-specific logic stays there;
// this layer only adapts types to the engine-agnostic interface.
package docker

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"strings"
	"time"

	"github.com/eclipse-iofog/edgelet/internal/constants"
	"github.com/eclipse-iofog/edgelet/internal/models"
	"github.com/eclipse-iofog/edgelet/pkg/docker"
	"github.com/eclipse-iofog/edgelet/pkg/engine"
)

// Engine implements engine.ContainerEngine using the Docker daemon.
type Engine struct {
	client *docker.Client
}

// New returns a new Docker engine. Call Init() before use.
func New() *Engine {
	return &Engine{client: docker.GetInstance()}
}

// Init dials the Docker daemon socket.
func (e *Engine) Init(cfg engine.EngineConfig) error {
	return e.client.Init(cfg.SocketURL, cfg.APIVersion)
}

// Close releases the Docker client.
func (e *Engine) Close() error {
	return e.client.Close()
}

// --- Container lifecycle ---

func (e *Engine) GetContainer(microserviceUUID string) (*engine.Container, error) {
	c, err := e.client.GetContainer(microserviceUUID)
	if err != nil || c == nil {
		return nil, err
	}
	return adaptContainer(*c), nil
}

func (e *Engine) GetContainerByID(containerID string) (*engine.Container, error) {
	c, err := e.client.GetContainerByID(containerID)
	if err != nil || c == nil {
		return nil, err
	}
	return adaptContainer(*c), nil
}

func (e *Engine) GetContainerSandboxID(_ string) (string, error) {
	return "", nil // Docker has no sandbox concept
}

func (e *Engine) GetRunningContainers() ([]engine.Container, error) {
	cs, err := e.client.GetRunningContainers()
	if err != nil {
		return nil, err
	}
	return adaptContainers(cs), nil
}

func (e *Engine) GetAllContainers() ([]engine.Container, error) {
	cs, err := e.client.GetAllContainers()
	if err != nil {
		return nil, err
	}
	return adaptContainers(cs), nil
}

func (e *Engine) CreateContainer(ms *models.Microservice, hostname string) (string, error) {
	// Scope/network policy is centralized in pkg/docker via shared workloadmeta resolver.
	return e.client.CreateContainer(ms, hostname)
}

func (e *Engine) StartContainer(containerID string) error {
	return e.client.StartContainer(containerID)
}

func (e *Engine) StopContainer(containerID string) error {
	return e.client.StopContainer(containerID)
}

func (e *Engine) KillContainer(containerID string) error {
	return e.client.KillContainer(containerID)
}

func (e *Engine) RemoveContainer(containerID string, removeVolumes bool) error {
	return e.client.RemoveContainer(containerID, removeVolumes)
}

// --- Image management ---

func (e *Engine) PullImage(imageRef string, registry *models.Registry, opts *engine.PullImageOptions) error {
	var cb func(float32)
	var platform string
	if opts != nil {
		cb = opts.ProgressCallback
		platform = opts.Platform
	}
	return e.client.PullImage(imageRef, "", platform, registry, cb)
}

func (e *Engine) FindLocalImage(imageRef, _ string, _ bool) (bool, error) {
	return e.client.FindLocalImage(imageRef)
}

func (e *Engine) RemoveImage(imageRef string) error {
	return e.client.RemoveImage(imageRef)
}

func (e *Engine) PruneImages() error {
	_, err := e.client.DockerPrune()
	return err
}

func (e *Engine) ListImages(_ context.Context) ([]engine.ImageInfo, error) {
	summaries, err := e.client.GetImages()
	if err != nil {
		return nil, err
	}
	inUseCounts, inUseErr := e.client.ImageInUseCounts()
	result := make([]engine.ImageInfo, 0, len(summaries))
	for _, s := range summaries {
		repository, tag := engine.PickPrimaryRepoTag(s.RepoTags)
		shortID := strings.TrimPrefix(s.ID, "sha256:")
		if len(shortID) > 12 {
			shortID = shortID[:12]
		}
		digest := ""
		if len(s.RepoDigests) > 0 {
			digest = s.RepoDigests[0]
		}
		contentSize, diskUsage, contentKnown := engine.ImageSizesFromSummary(s)
		if !contentKnown {
			contentSize = engine.SizeUnknown
		}
		inUse := engine.ImageInUseFromSummary(s)
		if inUse == engine.SizeUnknown && inUseErr == nil {
			inUse = engine.LookupImageInUseByID(s.ID, inUseCounts)
		}
		result = append(result, engine.ImageInfo{
			ID:               s.ID,
			RepoTags:         s.RepoTags,
			ShortID:          shortID,
			Repository:       repository,
			Tag:              tag,
			Digest:           digest,
			CreatedAt:        time.Unix(s.Created, 0).UTC(),
			ContentSizeBytes: contentSize,
			DiskUsageBytes:   diskUsage,
			InUse:            inUse,
			Engine:           "docker",
		})
	}
	return result, nil
}

func (e *Engine) LoadImageFromPath(_ context.Context, archivePath string) ([]engine.LoadedImage, error) {
	f, err := os.Open(archivePath) // #nosec G304 daemon validates local path before opening
	if err != nil {
		return nil, err
	}
	defer func() {
		_ = f.Close()
	}()
	loaded, err := e.client.LoadImage(f)
	if err != nil {
		return nil, err
	}
	out := make([]engine.LoadedImage, 0, len(loaded))
	for _, item := range loaded {
		out = append(out, engine.LoadedImage{Name: item.Name, ID: item.ID})
	}
	return out, nil
}

func (e *Engine) DeleteImage(_ context.Context, nameOrID string) error {
	return e.client.RemoveImage(nameOrID)
}

// PruneDangling removes only untagged images not referenced by any container.
func (e *Engine) PruneDangling(_ context.Context) (*engine.ImagePruneReport, error) {
	report, err := e.client.DockerPrune()
	if err != nil {
		return nil, err
	}
	deleted := make([]string, 0, len(report.ImagesDeleted))
	for _, item := range report.ImagesDeleted {
		if strings.TrimSpace(item.Deleted) != "" {
			deleted = append(deleted, strings.TrimSpace(item.Deleted))
			continue
		}
		if strings.TrimSpace(item.Untagged) != "" {
			deleted = append(deleted, strings.TrimSpace(item.Untagged))
		}
	}
	return &engine.ImagePruneReport{
		Deleted:             deleted,
		DeletedCount:        len(deleted),
		SpaceReclaimedBytes: int64(report.SpaceReclaimed), // #nosec G115 -- Docker prune space fits int64 in practice
	}, nil
}

func (e *Engine) PruneContainers(_ context.Context) (*engine.ContainerPruneReport, error) {
	report, err := e.client.PruneContainers()
	if err != nil {
		return nil, err
	}
	deleted := make([]string, 0, len(report.ContainersDeleted))
	for _, id := range report.ContainersDeleted {
		if strings.TrimSpace(id) != "" {
			deleted = append(deleted, strings.TrimSpace(id))
		}
	}
	return &engine.ContainerPruneReport{
		Deleted:      deleted,
		DeletedCount: len(deleted),
	}, nil
}

func (e *Engine) PruneVolumes(_ context.Context) (*engine.VolumePruneReport, error) {
	report, err := e.client.PruneVolumes()
	if err != nil {
		return nil, err
	}
	deleted := make([]string, 0, len(report.VolumesDeleted))
	for _, name := range report.VolumesDeleted {
		if strings.TrimSpace(name) != "" {
			deleted = append(deleted, strings.TrimSpace(name))
		}
	}
	return &engine.VolumePruneReport{
		Deleted:             deleted,
		DeletedCount:        len(deleted),
		SpaceReclaimedBytes: int64(report.SpaceReclaimed), // #nosec G115 -- Docker prune space fits int64 in practice
	}, nil
}

func (e *Engine) RemoveNamedVolume(_ context.Context, name string) error {
	return e.client.RemoveNamedVolume(name)
}

// --- Inspection / stats ---

func (e *Engine) GetContainerStatus(containerID, microserviceUUID string) (*models.MicroserviceStatus, error) {
	status, err := e.client.GetMicroserviceStatus(containerID, microserviceUUID)
	if err != nil {
		return nil, err
	}
	if status != nil {
		status.ApplyPodID(constants.EngineDocker, "")
	}
	return status, nil
}

func (e *Engine) GetContainerStats(containerID string) (*engine.ContainerStats, error) {
	s, err := e.client.GetContainerStats(containerID)
	if err != nil {
		return nil, err
	}
	return &engine.ContainerStats{
		CPUUsage:    s.CPUUsage,
		MemoryUsage: s.MemoryUsage,
	}, nil
}

func (e *Engine) GetContainerIPAddress(containerID string) (string, error) {
	return e.client.GetContainerIPAddress(containerID)
}

func (e *Engine) GetContainerStartedAt(containerID string) (int64, error) {
	return e.client.GetContainerStartedAt(containerID)
}

func (e *Engine) InspectContainerRaw(containerID string) (map[string]any, error) {
	raw, err := e.client.GetContainerInspectRaw(containerID)
	if err != nil {
		return nil, err
	}
	out := map[string]any{}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// --- Log streaming ---

func (e *Engine) TailContainerLogs(containerID, sessionID, microserviceUUID string, handler engine.LogTailHandler, cfg *engine.TailConfig) error {
	dockerCfg := &docker.TailConfig{
		Follow: cfg.Follow,
		Lines:  cfg.Lines,
		Since:  cfg.Since,
		Until:  cfg.Until,
		Ctx:    cfg.Ctx,
	}
	return e.client.TailContainerLogs(containerID, sessionID, microserviceUUID, &logHandlerAdapter{h: handler}, dockerCfg)
}

// --- Configuration drift ---

func (e *Engine) AreMicroserviceAndContainerEqual(containerID string, ms *models.Microservice, _ *models.Registry) bool {
	return e.client.AreMicroserviceAndContainerEqual(containerID, ms)
}

// --- Network ---

func (e *Engine) EnsureNetwork(_ string) error {
	// The Docker client always ensures the "edgelet" bridge network exists on Init.
	return nil
}

// --- Exec ---

func (e *Engine) CreateExecSession(containerID string, runtimeExecID string, cmd []string) (string, error) {
	return e.client.CreateExecSession(containerID, runtimeExecID, cmd)
}

// StartExecSession attaches stdin/stdout/stderr to a previously created Docker exec session
// and starts it. The call blocks until the process exits or an error occurs.
func (e *Engine) StartExecSession(execID string, stdin io.Reader, stdout, stderr io.Writer) error {
	return e.client.StartExecSession(execID, stdin, stdout, stderr)
}

// StopExecSession best-effort stops a Docker exec session.
func (e *Engine) StopExecSession(execID string) error {
	return e.client.StopExecSession(execID)
}

// GetExecSessionStatus reports whether the Docker exec process is still running.
func (e *Engine) GetExecSessionStatus(execID string) (bool, error) {
	info, err := e.client.GetExecSessionStatus(execID)
	if err != nil {
		return false, err
	}
	return info.Running, nil
}

func (e *Engine) GetExecSessionExitCode(execID string) (int, error) {
	return e.client.GetExecSessionExitCode(execID)
}

func (e *Engine) ResizeExecSession(execID string, cols, rows uint32) error {
	return e.client.ResizeExecSession(execID, cols, rows)
}

// --- Helpers ---

func (e *Engine) GetContainerMicroserviceUUID(cont engine.Container) string {
	return e.client.GetContainerMicroserviceUUID(docker.Container{
		ID:     cont.ID,
		Names:  cont.Names,
		Image:  cont.Image,
		Status: cont.Status,
		State:  cont.State,
		Labels: cont.Labels,
	})
}

func (e *Engine) GetContainerName(cont engine.Container) string {
	return e.client.GetContainerName(docker.Container{
		ID:     cont.ID,
		Names:  cont.Names,
		Image:  cont.Image,
		Status: cont.Status,
		State:  cont.State,
		Labels: cont.Labels,
	})
}

// --- Type adapters ---

func adaptContainer(c docker.Container) *engine.Container {
	return &engine.Container{
		ID:     c.ID,
		Names:  c.Names,
		Image:  c.Image,
		Status: c.Status,
		State:  c.State,
		Labels: c.Labels,
	}
}

func adaptContainers(cs []docker.Container) []engine.Container {
	out := make([]engine.Container, len(cs))
	for i, c := range cs {
		out[i] = engine.Container{
			ID:     c.ID,
			Names:  c.Names,
			Image:  c.Image,
			Status: c.Status,
			State:  c.State,
			Labels: c.Labels,
		}
	}
	return out
}

// logHandlerAdapter bridges engine.LogTailHandler to docker.LogTailHandler.
type logHandlerAdapter struct {
	h engine.LogTailHandler
}

func (a *logHandlerAdapter) OnLogLine(sessionID, microserviceUUID string, line []byte, st docker.StreamType) {
	var est engine.StreamType
	if st == docker.STDERR {
		est = engine.Stderr
	}
	a.h.OnLogLine(sessionID, microserviceUUID, line, est)
}

func (a *logHandlerAdapter) OnComplete(sessionID string) { a.h.OnComplete(sessionID) }
func (a *logHandlerAdapter) OnError(sessionID string, err error) {
	a.h.OnError(sessionID, err)
}
