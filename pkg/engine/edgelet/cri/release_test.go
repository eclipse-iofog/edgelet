//go:build linux

package cri

import (
	"context"
	"errors"
	"testing"

	"github.com/eclipse-iofog/edgelet/internal/workloadmeta"
	runtimeapi "k8s.io/cri-api/pkg/apis/runtime/v1"
)

type releaseRuntimeFake struct {
	containers []*runtimeapi.Container
	sandboxes  []*runtimeapi.PodSandbox
	order      []string
	removeErr  error
}

func (f *releaseRuntimeFake) ListContainers(context.Context, *runtimeapi.ContainerFilter) ([]*runtimeapi.Container, error) {
	return f.containers, nil
}

func (f *releaseRuntimeFake) StopContainer(_ context.Context, id string, _ int64) error {
	f.order = append(f.order, "stop:"+id)
	return nil
}

func (f *releaseRuntimeFake) RemoveContainer(_ context.Context, id string) error {
	f.order = append(f.order, "remove:"+id)
	if f.removeErr != nil {
		return f.removeErr
	}
	next := f.containers[:0]
	for _, container := range f.containers {
		if container.Id != id {
			next = append(next, container)
		}
	}
	f.containers = next
	return nil
}

func (f *releaseRuntimeFake) ListPodSandboxes(context.Context, *runtimeapi.PodSandboxFilter) ([]*runtimeapi.PodSandbox, error) {
	return f.sandboxes, nil
}

func (f *releaseRuntimeFake) StopPodSandbox(_ context.Context, id string) error {
	f.order = append(f.order, "stop-sandbox:"+id)
	return nil
}

func (f *releaseRuntimeFake) RemovePodSandbox(_ context.Context, id string) error {
	f.order = append(f.order, "remove-sandbox:"+id)
	next := f.sandboxes[:0]
	for _, sandbox := range f.sandboxes {
		if sandbox.Id != id {
			next = append(next, sandbox)
		}
	}
	f.sandboxes = next
	return nil
}

func TestReleaseLabeledWorkloads_RemovesExitedContainerAndSandbox(t *testing.T) {
	runtime := &releaseRuntimeFake{
		containers: []*runtimeapi.Container{
			{
				Id:           "exited",
				PodSandboxId: "sb-1",
				State:        runtimeapi.ContainerState_CONTAINER_EXITED,
				Labels: map[string]string{
					workloadmeta.LabelMicroserviceUID: "ms-1",
				},
			},
			{
				Id:    "other",
				State: runtimeapi.ContainerState_CONTAINER_RUNNING,
			},
		},
		sandboxes: []*runtimeapi.PodSandbox{
			{
				Id: "sb-1",
				Labels: map[string]string{
					workloadmeta.LabelMicroserviceUID: "ms-1",
				},
			},
		},
	}

	if err := ReleaseLabeledWorkloads(context.Background(), runtime, 30); err != nil {
		t.Fatalf("release: %v", err)
	}
	want := []string{"remove:exited", "stop-sandbox:sb-1", "remove-sandbox:sb-1"}
	if len(runtime.order) != len(want) {
		t.Fatalf("order=%v", runtime.order)
	}
	for i := range want {
		if runtime.order[i] != want[i] {
			t.Fatalf("order=%v", runtime.order)
		}
	}
	residue, err := ListLabeledResidueIDs(context.Background(), runtime)
	if err != nil {
		t.Fatalf("residue: %v", err)
	}
	if len(residue) != 0 {
		t.Fatalf("expected no labeled residue, got %v", residue)
	}
}

func TestReleaseLabeledWorkloads_StopsRunningContainerOnceThenDeletesSandbox(t *testing.T) {
	runtime := &releaseRuntimeFake{
		containers: []*runtimeapi.Container{
			{
				Id:           "running",
				PodSandboxId: "sb-1",
				State:        runtimeapi.ContainerState_CONTAINER_RUNNING,
				Labels: map[string]string{
					workloadmeta.LabelMicroserviceUID: "ms-1",
				},
			},
		},
		sandboxes: []*runtimeapi.PodSandbox{
			{
				Id: "sb-1",
				Labels: map[string]string{
					workloadmeta.LabelMicroserviceUID: "ms-1",
				},
			},
		},
	}
	if err := ReleaseLabeledWorkloads(context.Background(), runtime, 30); err != nil {
		t.Fatalf("release: %v", err)
	}
	want := []string{"stop:running", "remove:running", "stop-sandbox:sb-1", "remove-sandbox:sb-1"}
	if len(runtime.order) != len(want) {
		t.Fatalf("order=%v", runtime.order)
	}
	for i := range want {
		if runtime.order[i] != want[i] {
			t.Fatalf("order=%v", runtime.order)
		}
	}
}

func TestReleaseLabeledWorkloads_RemoveErrorKeepsSandbox(t *testing.T) {
	runtime := &releaseRuntimeFake{
		containers: []*runtimeapi.Container{
			{
				Id:           "exited",
				PodSandboxId: "sb-1",
				State:        runtimeapi.ContainerState_CONTAINER_EXITED,
				Labels: map[string]string{
					workloadmeta.LabelMicroserviceUID: "ms-1",
				},
			},
		},
		sandboxes: []*runtimeapi.PodSandbox{
			{
				Id: "sb-1",
				Labels: map[string]string{
					workloadmeta.LabelMicroserviceUID: "ms-1",
				},
			},
		},
		removeErr: errors.New("busy"),
	}
	err := ReleaseLabeledWorkloads(context.Background(), runtime, 30)
	if err == nil {
		t.Fatal("expected remove error")
	}
	for _, step := range runtime.order {
		if step == "stop:exited" || step == "stop-sandbox:sb-1" || step == "remove-sandbox:sb-1" {
			t.Fatalf("exited container must not be stopped again and its sandbox must stay, order=%v", runtime.order)
		}
	}
	if len(runtime.sandboxes) != 1 || runtime.sandboxes[0].Id != "sb-1" {
		t.Fatalf("sandbox removed after container delete failed: %+v", runtime.sandboxes)
	}
}

func TestReleaseLabeledWorkloads_RemoveError(t *testing.T) {
	runtime := &releaseRuntimeFake{
		containers: []*runtimeapi.Container{
			{
				Id:    "exited",
				State: runtimeapi.ContainerState_CONTAINER_EXITED,
				Labels: map[string]string{
					workloadmeta.LabelMicroserviceUID: "ms-1",
				},
			},
		},
		removeErr: errors.New("busy"),
	}
	err := ReleaseLabeledWorkloads(context.Background(), runtime, 30)
	if err == nil {
		t.Fatal("expected remove error")
	}
}

func TestListLabeledResidueIDs_IncludesExitedAndSandbox(t *testing.T) {
	runtime := &releaseRuntimeFake{
		containers: []*runtimeapi.Container{
			{
				Id:    "exited",
				State: runtimeapi.ContainerState_CONTAINER_EXITED,
				Labels: map[string]string{
					workloadmeta.LabelMicroserviceUID: "ms-1",
				},
			},
		},
		sandboxes: []*runtimeapi.PodSandbox{
			{
				Id: "sb-1",
				Labels: map[string]string{
					workloadmeta.LabelMicroserviceUID: "ms-1",
				},
			},
		},
	}
	ids, err := ListLabeledResidueIDs(context.Background(), runtime)
	if err != nil {
		t.Fatalf("residue: %v", err)
	}
	if len(ids) != 2 || ids[0] != "exited" || ids[1] != "sandbox:sb-1" {
		t.Fatalf("residue=%v", ids)
	}
}

func TestReleaseLabeledWorkloads_NotFoundIsSuccess(t *testing.T) {
	runtime := &releaseRuntimeFake{
		containers: []*runtimeapi.Container{
			{
				Id:    "gone",
				State: runtimeapi.ContainerState_CONTAINER_EXITED,
				Labels: map[string]string{
					workloadmeta.LabelMicroserviceUID: "ms-1",
				},
			},
		},
		removeErr: errors.New("container not found"),
	}
	if err := ReleaseLabeledWorkloads(context.Background(), runtime, 30); err != nil {
		t.Fatalf("not found must not fail release: %v", err)
	}
}
