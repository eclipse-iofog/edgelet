//go:build linux

package cri

import (
	"context"
	"encoding/json"
	"slices"
	"strconv"
	"strings"

	"github.com/eclipse-iofog/edgelet/internal/workloadmeta"
	runtimeapi "k8s.io/cri-api/pkg/apis/runtime/v1"
)

// leftoverLabeledTaskStates are CRI states that still occupy a k8s.io task.
var leftoverLabeledTaskStates = []runtimeapi.ContainerState{
	runtimeapi.ContainerState_CONTAINER_CREATED,
	runtimeapi.ContainerState_CONTAINER_RUNNING,
}

// WorkloadRuntime is the CRI surface used to list and stop labeled workloads.
type WorkloadRuntime interface {
	ListContainers(ctx context.Context, filter *runtimeapi.ContainerFilter) ([]*runtimeapi.Container, error)
	StopContainer(ctx context.Context, containerID string, timeout int64) error
}

// ListLabeledRunningIDs returns running CRI container IDs that carry an Edgelet
// microservice UID label (same selection as process-manager drain).
func ListLabeledRunningIDs(ctx context.Context, runtime WorkloadRuntime) ([]string, error) {
	if runtime == nil {
		return nil, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}

	filter := &runtimeapi.ContainerFilter{
		State: &runtimeapi.ContainerStateValue{State: runtimeapi.ContainerState_CONTAINER_RUNNING},
	}
	containers, err := runtime.ListContainers(ctx, filter)
	if err != nil {
		return nil, err
	}

	idsSet := make(map[string]struct{}, len(containers))
	for _, container := range containers {
		if container == nil {
			continue
		}
		id := strings.TrimSpace(container.Id)
		if id == "" {
			continue
		}
		if container.State != runtimeapi.ContainerState_CONTAINER_RUNNING {
			continue
		}
		if workloadmeta.MicroserviceUIDFromLabels(container.Labels) == "" {
			continue
		}
		idsSet[id] = struct{}{}
	}

	ids := make([]string, 0, len(idsSet))
	for id := range idsSet {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	return ids, nil
}

// ListLabeledTaskIDs returns labeled CRI containers that still have a runc task
// in the k8s.io namespace (created or running).
func ListLabeledTaskIDs(ctx context.Context, runtime WorkloadRuntime) ([]string, error) {
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

	idsSet := make(map[string]struct{}, len(containers))
	for _, container := range containers {
		if container == nil {
			continue
		}
		id := strings.TrimSpace(container.Id)
		if id == "" {
			continue
		}
		if !isLeftoverLabeledTaskState(container.State) {
			continue
		}
		if workloadmeta.MicroserviceUIDFromLabels(container.Labels) == "" {
			continue
		}
		idsSet[id] = struct{}{}
	}

	ids := make([]string, 0, len(idsSet))
	for id := range idsSet {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	return ids, nil
}

func isLeftoverLabeledTaskState(state runtimeapi.ContainerState) bool {
	return slices.Contains(leftoverLabeledTaskStates, state)
}

// ContainerPIDLookup resolves the host PID of a CRI container.
type ContainerPIDLookup interface {
	ContainerPID(ctx context.Context, containerID string) (int, error)
}

// ListLabeledPIDs returns host PIDs for labeled running workloads.
func ListLabeledPIDs(ctx context.Context, runtime WorkloadRuntime) ([]int, error) {
	ids, err := ListLabeledRunningIDs(ctx, runtime)
	if err != nil {
		return nil, err
	}
	lookup, ok := runtime.(ContainerPIDLookup)
	if !ok {
		return nil, nil
	}

	pids := make([]int, 0, len(ids))
	seen := make(map[int]struct{}, len(ids))
	for _, id := range ids {
		pid, pidErr := lookup.ContainerPID(ctx, id)
		if pidErr != nil || pid <= 1 {
			continue
		}
		if _, exists := seen[pid]; exists {
			continue
		}
		seen[pid] = struct{}{}
		pids = append(pids, pid)
	}
	slices.Sort(pids)
	return pids, nil
}

// ParseContainerPID reads a host PID from CRI verbose status info.
func ParseContainerPID(info map[string]string) int {
	if len(info) == 0 {
		return 0
	}
	if pid := parsePIDString(info["pid"]); pid > 0 {
		return pid
	}
	raw := strings.TrimSpace(info["info"])
	if raw == "" {
		return 0
	}
	var payload struct {
		PID int `json:"pid"`
	}
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		return 0
	}
	if payload.PID <= 1 {
		return 0
	}
	return payload.PID
}

func parsePIDString(raw string) int {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0
	}
	pid, err := strconv.Atoi(raw)
	if err != nil || pid <= 1 {
		return 0
	}
	return pid
}
