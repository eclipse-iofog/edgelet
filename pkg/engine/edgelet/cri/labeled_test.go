//go:build linux

package cri

import (
	"context"
	"testing"

	"github.com/eclipse-iofog/edgelet/internal/workloadmeta"
	runtimeapi "k8s.io/cri-api/pkg/apis/runtime/v1"
)

type listRuntimeFake struct {
	containers []*runtimeapi.Container
	err        error
}

func (f *listRuntimeFake) ListContainers(context.Context, *runtimeapi.ContainerFilter) ([]*runtimeapi.Container, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.containers, nil
}

func (f *listRuntimeFake) StopContainer(context.Context, string, int64) error { return nil }

func TestListLabeledRunningIDs_SelectsMicroserviceUID(t *testing.T) {
	runtime := &listRuntimeFake{
		containers: []*runtimeapi.Container{
			{
				Id:    "labeled-1",
				State: runtimeapi.ContainerState_CONTAINER_RUNNING,
				Labels: map[string]string{
					workloadmeta.LabelMicroserviceUID: "ms-1",
				},
			},
			{
				Id:    "unlabeled",
				State: runtimeapi.ContainerState_CONTAINER_RUNNING,
				Labels: map[string]string{
					"app": "other",
				},
			},
			{
				Id:    "exited-labeled",
				State: runtimeapi.ContainerState_CONTAINER_EXITED,
				Labels: map[string]string{
					workloadmeta.LabelMicroserviceUID: "ms-2",
				},
			},
			{
				Id:    "  ",
				State: runtimeapi.ContainerState_CONTAINER_RUNNING,
				Labels: map[string]string{
					workloadmeta.LabelMicroserviceUID: "ms-empty-id",
				},
			},
		},
	}

	ids, err := ListLabeledRunningIDs(context.Background(), runtime)
	if err != nil {
		t.Fatalf("ListLabeledRunningIDs: %v", err)
	}
	if len(ids) != 1 || ids[0] != "labeled-1" {
		t.Fatalf("expected only labeled running workload, got %v", ids)
	}
}

func TestListLabeledRunningIDs_EmptySet(t *testing.T) {
	ids, err := ListLabeledRunningIDs(context.Background(), &listRuntimeFake{})
	if err != nil {
		t.Fatalf("ListLabeledRunningIDs: %v", err)
	}
	if len(ids) != 0 {
		t.Fatalf("expected empty set, got %v", ids)
	}
}

func TestListLabeledRunningIDs_NilRuntime(t *testing.T) {
	ids, err := ListLabeledRunningIDs(context.Background(), nil)
	if err != nil {
		t.Fatalf("ListLabeledRunningIDs: %v", err)
	}
	if len(ids) != 0 {
		t.Fatalf("expected nil runtime to return empty set, got %v", ids)
	}
}

func TestListLabeledTaskIDs_IncludesCreatedAndRunning(t *testing.T) {
	runtime := &listRuntimeFake{
		containers: []*runtimeapi.Container{
			{
				Id:    "running",
				State: runtimeapi.ContainerState_CONTAINER_RUNNING,
				Labels: map[string]string{
					workloadmeta.LabelMicroserviceUID: "ms-1",
				},
			},
			{
				Id:    "created",
				State: runtimeapi.ContainerState_CONTAINER_CREATED,
				Labels: map[string]string{
					workloadmeta.LabelMicroserviceUID: "ms-2",
				},
			},
			{
				Id:    "exited",
				State: runtimeapi.ContainerState_CONTAINER_EXITED,
				Labels: map[string]string{
					workloadmeta.LabelMicroserviceUID: "ms-3",
				},
			},
			{
				Id:    "unlabeled-running",
				State: runtimeapi.ContainerState_CONTAINER_RUNNING,
			},
		},
	}
	ids, err := ListLabeledTaskIDs(context.Background(), runtime)
	if err != nil {
		t.Fatalf("ListLabeledTaskIDs: %v", err)
	}
	if len(ids) != 2 || ids[0] != "created" || ids[1] != "running" {
		t.Fatalf("expected leftover labeled tasks, got %v", ids)
	}
}

func TestParseContainerPID(t *testing.T) {
	if got := ParseContainerPID(map[string]string{"pid": "4242"}); got != 4242 {
		t.Fatalf("expected pid from info map, got %d", got)
	}
	if got := ParseContainerPID(map[string]string{"info": `{"pid": 77}`}); got != 77 {
		t.Fatalf("expected pid from verbose info JSON, got %d", got)
	}
	if got := ParseContainerPID(map[string]string{"pid": "1"}); got != 0 {
		t.Fatalf("must ignore pid 1, got %d", got)
	}
}
