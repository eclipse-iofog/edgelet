//go:build linux

package cri

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/eclipse-iofog/edgelet/internal/workloadmeta"
	runtimeapi "k8s.io/cri-api/pkg/apis/runtime/v1"
)

// workloadReleaser is the CRI surface that deletes a labeled workload after SIGTERM.
type workloadReleaser interface {
	WorkloadRuntime
	RemoveContainer(ctx context.Context, containerID string) error
	ListPodSandboxes(ctx context.Context, filter *runtimeapi.PodSandboxFilter) ([]*runtimeapi.PodSandbox, error)
	StopPodSandbox(ctx context.Context, podSandboxID string) error
	RemovePodSandbox(ctx context.Context, podSandboxID string) error
}

// ReleaseLabeledWorkloads deletes labeled containers and their pod sandboxes.
// StopContainer is skipped once a container has exited. A sandbox is removed
// only after every labeled container that referenced it has been deleted.
func ReleaseLabeledWorkloads(ctx context.Context, runtime WorkloadRuntime, stopTimeoutSec int64) error {
	if runtime == nil {
		return nil
	}
	releaser, ok := runtime.(workloadReleaser)
	if !ok {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if stopTimeoutSec <= 0 {
		stopTimeoutSec = 1
	}

	containers, err := runtime.ListContainers(ctx, nil)
	if err != nil {
		return fmt.Errorf("list labeled workloads for delete: %w", err)
	}

	protectedSandboxes := make(map[string]struct{})
	releasedContainers := make([][]string, 0, len(containers))
	var releaseErr error
	for _, container := range containers {
		if container == nil || strings.TrimSpace(container.Id) == "" {
			continue
		}
		if workloadmeta.MicroserviceUIDFromLabels(container.Labels) == "" {
			continue
		}
		sandboxIDs := containerSandboxIDs(container)
		// A container that has already exited must not be stopped again.
		if container.State == runtimeapi.ContainerState_CONTAINER_RUNNING ||
			container.State == runtimeapi.ContainerState_CONTAINER_CREATED {
			if stopErr := runtime.StopContainer(ctx, container.Id, stopTimeoutSec); stopErr != nil && !workloadAlreadyGone(stopErr) {
				protectSandboxes(protectedSandboxes, sandboxIDs)
				releaseErr = fmt.Errorf("stop container %s: %w", container.Id, stopErr)
				continue
			}
		}
		if removeErr := releaser.RemoveContainer(ctx, container.Id); removeErr != nil && !workloadAlreadyGone(removeErr) {
			protectSandboxes(protectedSandboxes, sandboxIDs)
			releaseErr = fmt.Errorf("remove container %s: %w", container.Id, removeErr)
			continue
		}
		releasedContainers = append(releasedContainers, sandboxIDs)
	}

	sandboxIDs := make(map[string]struct{})
	for _, ids := range releasedContainers {
		for _, id := range ids {
			if _, blocked := protectedSandboxes[id]; blocked {
				continue
			}
			rememberSandboxID(sandboxIDs, id)
		}
	}

	sandboxes, err := releaser.ListPodSandboxes(ctx, nil)
	if err != nil {
		return fmt.Errorf("list labeled sandboxes for delete: %w", err)
	}
	for _, sandbox := range sandboxes {
		if sandbox == nil || strings.TrimSpace(sandbox.Id) == "" {
			continue
		}
		if _, blocked := protectedSandboxes[sandbox.Id]; blocked {
			continue
		}
		if workloadmeta.MicroserviceUIDFromLabels(sandbox.Labels) == "" {
			continue
		}
		rememberSandboxID(sandboxIDs, sandbox.Id)
	}

	ids := make([]string, 0, len(sandboxIDs))
	for id := range sandboxIDs {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	for _, id := range ids {
		if _, blocked := protectedSandboxes[id]; blocked {
			continue
		}
		if stopErr := releaser.StopPodSandbox(ctx, id); stopErr != nil && !workloadAlreadyGone(stopErr) {
			releaseErr = fmt.Errorf("stop pod sandbox %s: %w", id, stopErr)
			continue
		}
		if removeErr := releaser.RemovePodSandbox(ctx, id); removeErr != nil && !workloadAlreadyGone(removeErr) {
			releaseErr = fmt.Errorf("remove pod sandbox %s: %w", id, removeErr)
		}
	}
	return releaseErr
}

// ListLabeledResidueIDs returns labeled containers in any state and labeled pod
// sandboxes. An exited container is still a task containerd will try to restore.
func ListLabeledResidueIDs(ctx context.Context, runtime WorkloadRuntime) ([]string, error) {
	if runtime == nil {
		return nil, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}

	containers, err := runtime.ListContainers(ctx, nil)
	if err != nil {
		return nil, err
	}
	idsSet := make(map[string]struct{})
	for _, container := range containers {
		if container == nil {
			continue
		}
		id := strings.TrimSpace(container.Id)
		if id == "" || workloadmeta.MicroserviceUIDFromLabels(container.Labels) == "" {
			continue
		}
		idsSet[id] = struct{}{}
	}

	if releaser, ok := runtime.(workloadReleaser); ok {
		sandboxes, sandErr := releaser.ListPodSandboxes(ctx, nil)
		if sandErr != nil {
			return nil, sandErr
		}
		for _, sandbox := range sandboxes {
			if sandbox == nil {
				continue
			}
			id := strings.TrimSpace(sandbox.Id)
			if id == "" || workloadmeta.MicroserviceUIDFromLabels(sandbox.Labels) == "" {
				continue
			}
			idsSet["sandbox:"+id] = struct{}{}
		}
	}

	ids := make([]string, 0, len(idsSet))
	for id := range idsSet {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	return ids, nil
}

func containerSandboxIDs(container *runtimeapi.Container) []string {
	if container == nil {
		return nil
	}
	ids := make([]string, 0, 2)
	remember := func(id string) {
		id = strings.TrimSpace(id)
		if id == "" {
			return
		}
		for _, existing := range ids {
			if existing == id {
				return
			}
		}
		ids = append(ids, id)
	}
	remember(container.PodSandboxId)
	if container.Labels != nil {
		remember(container.Labels[workloadmeta.LabelSandboxID])
	}
	return ids
}

func protectSandboxes(dst map[string]struct{}, ids []string) {
	for _, id := range ids {
		rememberSandboxID(dst, id)
	}
}

func rememberSandboxID(dst map[string]struct{}, id string) {
	id = strings.TrimSpace(id)
	if id == "" {
		return
	}
	dst[id] = struct{}{}
}

func workloadAlreadyGone(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "not found") || strings.Contains(msg, "does not exist")
}
