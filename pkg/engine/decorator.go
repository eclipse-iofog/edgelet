package engine

import (
	"context"
	"errors"
	"io"
	"time"

	"github.com/eclipse-iofog/edgelet/internal/models"
	"github.com/eclipse-iofog/edgelet/internal/runtimeops"
)

const loggingEngineModule = "ContainerEngine"

// loggingEngine wraps a ContainerEngine with Debug success and Warn failure logs for mutating API calls.
type loggingEngine struct {
	inner      ContainerEngine
	engineName string
}

// NewLoggingEngine returns a ContainerEngine decorator that emits structured runtimeops events.
func NewLoggingEngine(inner ContainerEngine, engineName string) ContainerEngine {
	return &loggingEngine{inner: inner, engineName: engineName}
}

func (l *loggingEngine) Init(cfg EngineConfig) error {
	return l.inner.Init(cfg)
}

func (l *loggingEngine) Close() error {
	return l.inner.Close()
}

func (l *loggingEngine) GetContainer(microserviceUUID string) (*Container, error) {
	return l.inner.GetContainer(microserviceUUID)
}

func (l *loggingEngine) GetContainerByID(containerID string) (*Container, error) {
	return l.inner.GetContainerByID(containerID)
}

func (l *loggingEngine) GetContainerSandboxID(containerID string) (string, error) {
	return l.inner.GetContainerSandboxID(containerID)
}

func (l *loggingEngine) GetRunningContainers() ([]Container, error) {
	return l.inner.GetRunningContainers()
}

func (l *loggingEngine) GetAllContainers() ([]Container, error) {
	return l.inner.GetAllContainers()
}

func (l *loggingEngine) CreateContainer(ms *models.Microservice, hostname string) (containerID string, err error) {
	start := time.Now()
	image := ""
	if ms != nil {
		image = ms.ImageName
	}
	containerID, err = l.inner.CreateContainer(ms, hostname)
	if errors.Is(err, ErrReconcilePaused) {
		return "", err
	}
	l.emitMutating(runtimeops.EventEngineCRIContainerCreated, containerID, image, runtimeops.ReasonCreateFailed, "container create failed", "container created", start, err, err == nil)
	return containerID, err
}

func (l *loggingEngine) StartContainer(containerID string) error {
	start := time.Now()
	err := l.inner.StartContainer(containerID)
	l.emitMutating(runtimeops.EventEngineContainerStart, containerID, "", runtimeops.ReasonStartFailed, "container start failed", "container started", start, err, true)
	return err
}

func (l *loggingEngine) StopContainer(containerID string) error {
	start := time.Now()
	err := l.inner.StopContainer(containerID)
	l.emitMutating(runtimeops.EventEngineContainerStop, containerID, "", runtimeops.ReasonStopFailed, "container stop failed", "container stopped", start, err, true)
	return err
}

func (l *loggingEngine) KillContainer(containerID string) error {
	start := time.Now()
	err := l.inner.KillContainer(containerID)
	l.emitMutating(runtimeops.EventEngineContainerStop, containerID, "", runtimeops.ReasonStopFailed, "container kill failed", "container killed", start, err, true, map[string]any{"force": true})
	return err
}

func (l *loggingEngine) RemoveContainer(containerID string, removeVolumes bool) error {
	start := time.Now()
	err := l.inner.RemoveContainer(containerID, removeVolumes)
	extra := map[string]any{"removeVolumes": removeVolumes}
	l.emitMutating(runtimeops.EventEngineContainerRemove, containerID, "", runtimeops.ReasonRemoveFailed, "container remove failed", "container removed", start, err, true, extra)
	return err
}

func (l *loggingEngine) PullImage(imageRef string, registry *models.Registry, opts *PullImageOptions) error {
	start := time.Now()
	err := l.inner.PullImage(imageRef, registry, opts)
	l.emitMutating(runtimeops.EventEngineImagePulled, "", imageRef, runtimeops.ReasonPullFailed, "image pull failed", "image pulled", start, err, true)
	return err
}

func (l *loggingEngine) FindLocalImage(imageRef, registryURL string, fromCache bool) (bool, error) {
	return l.inner.FindLocalImage(imageRef, registryURL, fromCache)
}

func (l *loggingEngine) RemoveImage(imageRef string) error {
	start := time.Now()
	err := l.inner.RemoveImage(imageRef)
	l.emitMutating(runtimeops.EventEnginePrune, "", "", runtimeops.ReasonRemoveFailed, "image remove failed", "image removed", start, err, false, map[string]any{"image": imageRef, "operation": "remove"})
	return err
}

func (l *loggingEngine) PruneImages() error {
	start := time.Now()
	err := l.inner.PruneImages()
	l.emitMutating(runtimeops.EventEnginePrune, "", "", "", "image prune failed", "images pruned", start, err, false, map[string]any{"operation": "pruneImages"})
	return err
}

func (l *loggingEngine) ListImages(ctx context.Context) ([]ImageInfo, error) {
	return l.inner.ListImages(ctx)
}

func (l *loggingEngine) LoadImageFromPath(ctx context.Context, archivePath string) ([]LoadedImage, error) {
	start := time.Now()
	loaded, err := l.inner.LoadImageFromPath(ctx, archivePath)
	l.emitMutating(runtimeops.EventEngineImagePulled, "", "", runtimeops.ReasonPullFailed, "image load failed", "image loaded", start, err, false, map[string]any{"archivePath": archivePath})
	return loaded, err
}

func (l *loggingEngine) DeleteImage(ctx context.Context, nameOrID string) error {
	start := time.Now()
	err := l.inner.DeleteImage(ctx, nameOrID)
	l.emitMutating(runtimeops.EventEnginePrune, "", "", runtimeops.ReasonRemoveFailed, "image delete failed", "image deleted", start, err, false, map[string]any{"image": nameOrID, "operation": "delete"})
	return err
}

func (l *loggingEngine) PruneDangling(ctx context.Context) (*ImagePruneReport, error) {
	start := time.Now()
	report, err := l.inner.PruneDangling(ctx)
	l.emitPrune("pruneDangling", start, err, report != nil, func() map[string]any {
		if report == nil {
			return nil
		}
		return map[string]any{"deletedCount": report.DeletedCount}
	})
	return report, err
}

func (l *loggingEngine) PruneContainers(ctx context.Context) (*ContainerPruneReport, error) {
	start := time.Now()
	report, err := l.inner.PruneContainers(ctx)
	l.emitPrune("pruneContainers", start, err, report != nil, func() map[string]any {
		if report == nil {
			return nil
		}
		return map[string]any{"deletedCount": report.DeletedCount}
	})
	return report, err
}

func (l *loggingEngine) PruneVolumes(ctx context.Context) (*VolumePruneReport, error) {
	start := time.Now()
	report, err := l.inner.PruneVolumes(ctx)
	l.emitPrune("pruneVolumes", start, err, report != nil, func() map[string]any {
		if report == nil {
			return nil
		}
		return map[string]any{"deletedCount": report.DeletedCount}
	})
	return report, err
}

func (l *loggingEngine) RemoveNamedVolume(ctx context.Context, name string) error {
	return l.inner.RemoveNamedVolume(ctx, name)
}

func (l *loggingEngine) GetContainerStatus(containerID, microserviceUUID string) (*models.MicroserviceStatus, error) {
	return l.inner.GetContainerStatus(containerID, microserviceUUID)
}

func (l *loggingEngine) GetContainerStats(containerID string) (*ContainerStats, error) {
	return l.inner.GetContainerStats(containerID)
}

func (l *loggingEngine) GetContainerIPAddress(containerID string) (string, error) {
	return l.inner.GetContainerIPAddress(containerID)
}

func (l *loggingEngine) GetContainerStartedAt(containerID string) (int64, error) {
	return l.inner.GetContainerStartedAt(containerID)
}

func (l *loggingEngine) InspectContainerRaw(containerID string) (map[string]any, error) {
	return l.inner.InspectContainerRaw(containerID)
}

func (l *loggingEngine) TailContainerLogs(containerID, sessionID, microserviceUUID string, handler LogTailHandler, cfg *TailConfig) error {
	return l.inner.TailContainerLogs(containerID, sessionID, microserviceUUID, handler, cfg)
}

func (l *loggingEngine) AreMicroserviceAndContainerEqual(containerID string, ms *models.Microservice, registry *models.Registry) bool {
	return l.inner.AreMicroserviceAndContainerEqual(containerID, ms, registry)
}

func (l *loggingEngine) SetContainerSpecCompare(allow bool) {
	type specGate interface {
		SetContainerSpecCompare(bool)
	}
	if g, ok := l.inner.(specGate); ok {
		g.SetContainerSpecCompare(allow)
	}
}

func (l *loggingEngine) EnsureNetwork(name string) error {
	start := time.Now()
	err := l.inner.EnsureNetwork(name)
	l.emitMutating(runtimeops.EventEnginePrune, "", "", "", "ensure network failed", "network ensured", start, err, false, map[string]any{"network": name, "operation": "ensureNetwork"})
	return err
}

func (l *loggingEngine) CreateExecSession(containerID string, runtimeExecID string, cmd []string) (string, error) {
	start := time.Now()
	execID, err := l.inner.CreateExecSession(containerID, runtimeExecID, cmd)
	l.emitMutating(runtimeops.EventEnginePrune, containerID, "", "", "exec create failed", "exec created", start, err, false, map[string]any{"operation": "createExec", "execId": execID})
	return execID, err
}

func (l *loggingEngine) StartExecSession(execID string, stdin io.Reader, stdout, stderr io.Writer) error {
	start := time.Now()
	err := l.inner.StartExecSession(execID, stdin, stdout, stderr)
	l.emitMutating(runtimeops.EventEnginePrune, "", "", "", "exec start failed", "exec started", start, err, false, map[string]any{"operation": "startExec", "execId": execID})
	return err
}

func (l *loggingEngine) GetExecSessionStatus(execID string) (bool, error) {
	return l.inner.GetExecSessionStatus(execID)
}

func (l *loggingEngine) GetExecSessionExitCode(execID string) (int, error) {
	return l.inner.GetExecSessionExitCode(execID)
}

func (l *loggingEngine) ResizeExecSession(execID string, cols, rows uint32) error {
	start := time.Now()
	err := l.inner.ResizeExecSession(execID, cols, rows)
	l.emitMutating(runtimeops.EventEnginePrune, "", "", "", "exec resize failed", "exec resized", start, err, false, map[string]any{"operation": "resizeExec", "execId": execID})
	return err
}

func (l *loggingEngine) StopExecSession(execID string) error {
	start := time.Now()
	err := l.inner.StopExecSession(execID)
	l.emitMutating(runtimeops.EventEnginePrune, "", "", "", "exec stop failed", "exec stopped", start, err, false, map[string]any{"operation": "stopExec", "execId": execID})
	return err
}

func (l *loggingEngine) GetContainerMicroserviceUUID(cont Container) string {
	return l.inner.GetContainerMicroserviceUUID(cont)
}

func (l *loggingEngine) GetContainerName(cont Container) string {
	return l.inner.GetContainerName(cont)
}

func (l *loggingEngine) sandboxIDFor(containerID string) string {
	if containerID == "" {
		return ""
	}
	sandboxID, err := l.inner.GetContainerSandboxID(containerID)
	if err != nil {
		return ""
	}
	return sandboxID
}

func (l *loggingEngine) emitMutating(
	event, containerID, image, reasonCode, failMsg, successMsg string,
	start time.Time,
	err error,
	logSuccess bool,
	extra ...map[string]any,
) {
	var fields map[string]any
	if len(extra) > 0 {
		fields = extra[0]
	}
	l.emit(runtimeops.RuntimeEvent{
		Event:       event,
		Engine:      l.engineName,
		ContainerID: containerID,
		SandboxID:   l.sandboxIDFor(containerID),
		Image:       image,
		Module:      loggingEngineModule,
		DurationMs:  time.Since(start).Milliseconds(),
		Message:     successMsg,
		Fields:      fields,
	}, err, reasonCode, failMsg, logSuccess)
}

func (l *loggingEngine) emitPrune(operation string, start time.Time, err error, hasReport bool, reportFields func() map[string]any) {
	fields := map[string]any{"operation": operation}
	if hasReport && reportFields != nil {
		for k, v := range reportFields() {
			fields[k] = v
		}
	}
	l.emitMutating(runtimeops.EventEnginePrune, "", "", runtimeops.ReasonRemoveFailed, operation+" failed", operation+" completed", start, err, err == nil, fields)
}

func (l *loggingEngine) emit(e runtimeops.RuntimeEvent, err error, reasonCode, failMsg string, logSuccess bool) {
	if err != nil {
		e.Level = runtimeops.LevelWarn
		e.ReasonCode = reasonCode
		e.Result = runtimeops.ResultFailed
		e.Error = err.Error()
		e.Message = failMsg
		runtimeops.Emit(context.Background(), e)
		return
	}
	if !logSuccess {
		return
	}
	e.Level = runtimeops.LevelDebug
	e.Result = runtimeops.ResultOK
	runtimeops.Emit(context.Background(), e)
}
