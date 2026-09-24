//go:build linux

package edgelet

import (
	"context"
	"errors"
	"strings"
	"time"

	eventtypes "github.com/containerd/containerd/api/events"
	"github.com/containerd/containerd/v2/core/events"
	"github.com/containerd/typeurl/v2"
	"github.com/eclipse-iofog/edgelet/internal/processmanager"
	"github.com/eclipse-iofog/edgelet/internal/runtimeops"
	"github.com/eclipse-iofog/edgelet/internal/workloadmeta"
)

const runtimeEventResubscribeWait = 5 * time.Second

const (
	runtimeWakeStart  = "start"
	runtimeWakeExit   = "exit"
	runtimeWakeOOM    = "oom"
	runtimeWakeDelete = "delete"
)

// runtimeEventHooks replaces the containerd subscription in tests.
type runtimeEventHooks struct {
	subscribe func(context.Context) (<-chan *events.Envelope, <-chan error)
	lookup    func(context.Context, string) (string, bool)
	wake      func(string, string)
	wait      func(context.Context) bool
}

func (e *Engine) startContainerdRuntimeEventMonitor() {
	if e.client == nil && !e.hasRuntimeEventSubscribeHook() {
		return
	}
	ctx, cancel := context.WithCancel(e.ctx())
	e.runtimeEventsCancel = cancel
	go e.runContainerdRuntimeEventMonitor(ctx)
}

func (e *Engine) hasRuntimeEventSubscribeHook() bool {
	return e != nil && e.runtimeEventHooks != nil && e.runtimeEventHooks.subscribe != nil
}

func (e *Engine) runContainerdRuntimeEventMonitor(ctx context.Context) {
	for {
		if ctx.Err() != nil {
			return
		}
		ch, errCh := e.subscribeContainerdEvents(ctx)
		processmanager.GetInstance().NoteRuntimeEventStreamHealthy()
		stop, streamErr := e.consumeContainerdEvents(ctx, ch, errCh)
		if stop || ctx.Err() != nil {
			return
		}
		e.noteEventStreamUnavailable(streamErr)
		if !e.waitRuntimeResubscribe(ctx) {
			return
		}
	}
}

func (e *Engine) subscribeContainerdEvents(ctx context.Context) (<-chan *events.Envelope, <-chan error) {
	if e.hasRuntimeEventSubscribeHook() {
		return e.runtimeEventHooks.subscribe(ctx)
	}
	if e.client == nil {
		return nil, nil
	}
	return e.client.Subscribe(ctx,
		`topic=="/tasks/start"`,
		`topic=="/tasks/exit"`,
		`topic=="/tasks/oom"`,
		`topic=="/tasks/delete"`,
	)
}

func (e *Engine) waitRuntimeResubscribe(ctx context.Context) bool {
	if e.runtimeEventHooks != nil && e.runtimeEventHooks.wait != nil {
		return e.runtimeEventHooks.wait(ctx)
	}
	timer := time.NewTimer(runtimeEventResubscribeWait)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func (e *Engine) consumeContainerdEvents(ctx context.Context, ch <-chan *events.Envelope, errCh <-chan error) (stop bool, streamErr error) {
	if ch == nil && errCh == nil {
		return false, errors.New("event stream unavailable")
	}
	settled := time.NewTimer(runtimeEventResubscribeWait)
	defer settled.Stop()
	for {
		select {
		case <-ctx.Done():
			return true, nil
		case <-settled.C:
			processmanager.GetInstance().NoteRuntimeEventStreamSettled()
		case env, ok := <-ch:
			if !ok {
				if ctx.Err() != nil {
					return true, nil
				}
				return false, errors.New("event stream unavailable")
			}
			if env != nil {
				e.handleContainerdEventEnvelope(ctx, env)
			}
		case err, ok := <-errCh:
			if !ok || err == nil || errors.Is(err, context.Canceled) || ctx.Err() != nil {
				if ctx.Err() != nil || errors.Is(err, context.Canceled) {
					return true, nil
				}
				return false, errors.New("event stream unavailable")
			}
			return false, err
		}
	}
}

func (e *Engine) noteEventStreamUnavailable(err error) {
	if !processmanager.GetInstance().NoteRuntimeEventStreamUnavailable() {
		return
	}
	ev := runtimeops.RuntimeEvent{
		Event:   runtimeops.EventContainerRuntimeEvent,
		Level:   runtimeops.LevelWarn,
		Message: "event stream unavailable",
		Result:  runtimeops.ResultFailed,
		Source:  runtimeops.SourceRuntimeWatch,
		Fields: map[string]any{
			"runtimeStatus": "stream_error",
		},
	}
	if err != nil {
		ev.Error = err.Error()
	}
	e.emitRuntime(ev)
}

func (e *Engine) handleContainerdEventEnvelope(ctx context.Context, env *events.Envelope) {
	if env == nil || env.Event == nil {
		return
	}
	ev, err := typeurl.UnmarshalAny(env.Event)
	if err != nil {
		return
	}
	switch evt := ev.(type) {
	case *eventtypes.TaskExit:
		e.handleTaskExitRuntimeEvent(ctx, evt)
	case *eventtypes.TaskOOM:
		e.handleTaskOOMRuntimeEvent(ctx, evt)
	case *eventtypes.TaskDelete:
		e.handleTaskDeleteRuntimeEvent(ctx, evt)
	case *eventtypes.TaskStart:
		e.handleTaskStartRuntimeEvent(ctx, evt)
	}
}

func (e *Engine) handleTaskExitRuntimeEvent(ctx context.Context, ev *eventtypes.TaskExit) {
	if ev == nil || taskEventIsExec(ev.ID, ev.ContainerID) {
		return
	}
	containerID := taskExitContainerID(ev)
	if containerID == "" {
		return
	}
	msUUID, ok := e.resolveManagedMicroservice(ctx, containerID)
	if !ok {
		return
	}
	e.deliverRuntimeWake(msUUID, runtimeWakeExit)
	reason, exitCode := e.runtimeFailure(ctx, containerID)
	if reason == "" {
		reason = "ContainerExited"
	}
	e.emitContainerRuntimeWatchEvent(containerID, msUUID, runtimeWakeExit, exitCode, reason)
}

func (e *Engine) handleTaskOOMRuntimeEvent(ctx context.Context, ev *eventtypes.TaskOOM) {
	if ev == nil {
		return
	}
	containerID := strings.TrimSpace(ev.ContainerID)
	if containerID == "" {
		return
	}
	msUUID, ok := e.resolveManagedMicroservice(ctx, containerID)
	if !ok {
		return
	}
	e.deliverRuntimeWake(msUUID, runtimeWakeOOM)
	reason, exitCode := e.runtimeFailure(ctx, containerID)
	if reason == "" {
		reason = "OOMKilled"
	}
	e.emitContainerRuntimeWatchEvent(containerID, msUUID, runtimeWakeOOM, exitCode, reason)
}

func (e *Engine) handleTaskDeleteRuntimeEvent(ctx context.Context, ev *eventtypes.TaskDelete) {
	if ev == nil || taskEventIsExec(ev.ID, ev.ContainerID) {
		return
	}
	containerID := strings.TrimSpace(ev.ContainerID)
	if containerID == "" {
		containerID = strings.TrimSpace(ev.ID)
	}
	if containerID == "" {
		return
	}
	msUUID, ok := e.resolveManagedMicroservice(ctx, containerID)
	if !ok {
		return
	}
	e.deliverRuntimeWake(msUUID, runtimeWakeDelete)
	e.emitContainerRuntimeWatchEvent(containerID, msUUID, runtimeWakeDelete, 0, "")
}

func (e *Engine) handleTaskStartRuntimeEvent(ctx context.Context, ev *eventtypes.TaskStart) {
	if ev == nil {
		return
	}
	containerID := strings.TrimSpace(ev.ContainerID)
	if containerID == "" {
		return
	}
	msUUID, ok := e.resolveManagedMicroservice(ctx, containerID)
	if !ok {
		return
	}
	e.deliverRuntimeWake(msUUID, runtimeWakeStart)
	e.emitContainerRuntimeWatchEvent(containerID, msUUID, runtimeWakeStart, 0, "")
}

func (e *Engine) resolveManagedMicroservice(ctx context.Context, containerID string) (string, bool) {
	if e.runtimeEventHooks != nil && e.runtimeEventHooks.lookup != nil {
		return e.runtimeEventHooks.lookup(ctx, containerID)
	}
	return e.managedMicroserviceForContainer(ctx, containerID)
}

func (e *Engine) deliverRuntimeWake(uuid, kind string) {
	if e.runtimeEventHooks != nil && e.runtimeEventHooks.wake != nil {
		e.runtimeEventHooks.wake(uuid, kind)
		return
	}
	processmanager.GetInstance().ReconcileRuntimeEvent(uuid, kind)
}

func (e *Engine) runtimeFailure(ctx context.Context, containerID string) (string, int32) {
	if e == nil || e.criClient == nil {
		return "", 0
	}
	reason, exitCode, _ := e.readCRIContainerFailure(ctx, containerID)
	return reason, exitCode
}

func (e *Engine) managedMicroserviceForContainer(ctx context.Context, containerID string) (msUUID string, ok bool) {
	if e == nil || e.client == nil {
		return "", false
	}
	c, err := e.client.LoadContainer(ctx, containerID)
	if err != nil {
		return "", false
	}
	if isSandboxContainer(ctx, c) {
		return "", false
	}
	info, err := c.Info(ctx)
	if err != nil || info.Labels == nil {
		return "", false
	}
	if !workloadmeta.IsManagedByIofog(info.Labels) {
		return "", false
	}
	msUUID = workloadmeta.MicroserviceUIDFromLabels(info.Labels)
	if msUUID == "" {
		return "", false
	}
	return msUUID, true
}

func (e *Engine) emitContainerRuntimeWatchEvent(containerID, msUUID, runtimeStatus string, exitCode int32, reason string) {
	fields := map[string]any{
		"runtimeStatus": runtimeStatus,
	}
	if strings.TrimSpace(reason) != "" {
		fields["reason"] = reason
	}
	if exitCode != 0 {
		fields["exitCode"] = exitCode
	}
	e.emitRuntime(runtimeops.RuntimeEvent{
		Event:       runtimeops.EventContainerRuntimeEvent,
		Level:       runtimeops.LevelInfo,
		MsUUID:      msUUID,
		ContainerID: containerID,
		SandboxID:   e.sandboxIDFor(containerID),
		Source:      runtimeops.SourceRuntimeWatch,
		Message:     "container runtime event",
		Fields:      fields,
	})
}

// taskEventIsExec reports an exec lifecycle event. The task id differs from the container id.
func taskEventIsExec(taskID, containerID string) bool {
	taskID = strings.TrimSpace(taskID)
	containerID = strings.TrimSpace(containerID)
	return taskID != "" && containerID != "" && taskID != containerID
}

func taskExitContainerID(ev *eventtypes.TaskExit) string {
	// Match CRI: workload task exit uses ID as the container ID; exec exits use a different ID.
	if ev == nil {
		return ""
	}
	if id := strings.TrimSpace(ev.ID); id != "" {
		return id
	}
	return strings.TrimSpace(ev.ContainerID)
}
