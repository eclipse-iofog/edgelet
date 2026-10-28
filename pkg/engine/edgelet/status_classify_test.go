package edgelet

import (
	"testing"

	"github.com/eclipse-iofog/edgelet/internal/models"
	runtimeapi "k8s.io/cri-api/pkg/apis/runtime/v1"
)

func TestClassifyMissingTask(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		state    runtimeapi.ContainerState
		reason   string
		exitCode int32
		message  string
		want     models.MicroserviceState
		failure  string
	}{
		{
			name:  "never started",
			state: runtimeapi.ContainerState_CONTAINER_CREATED,
			want:  models.MicroserviceStateCreated,
		},
		{
			name:     "nonzero exit",
			state:    runtimeapi.ContainerState_CONTAINER_EXITED,
			reason:   "Error",
			exitCode: 139,
			want:     models.MicroserviceStateExiting,
			failure:  "CRI reason=Error exitCode=139 message=",
		},
		{
			name:    "clean exit",
			state:   runtimeapi.ContainerState_CONTAINER_EXITED,
			reason:  "Completed",
			want:    models.MicroserviceStateExiting,
			failure: "CRI reason=Completed exitCode=0 message=",
		},
		{
			name:     "start failure",
			state:    runtimeapi.ContainerState_CONTAINER_EXITED,
			reason:   "StartError",
			exitCode: 128,
			message:  "failed to start",
			want:     models.MicroserviceStateExiting,
			failure:  "CRI reason=StartError exitCode=128 message=failed to start",
		},
		{
			name:  "unknown",
			state: runtimeapi.ContainerState_CONTAINER_UNKNOWN,
			want:  models.MicroserviceStateUnknown,
		},
		{
			name:  "running without a task",
			state: runtimeapi.ContainerState_CONTAINER_RUNNING,
			want:  models.MicroserviceStateRunning,
		},
		{
			name:     "running keeps a stale exit code off the status",
			state:    runtimeapi.ContainerState_CONTAINER_RUNNING,
			reason:   "Error",
			exitCode: 139,
			want:     models.MicroserviceStateRunning,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, failure := classifyMissingTask(tt.state, tt.reason, tt.exitCode, tt.message)
			if got != tt.want {
				t.Fatalf("state=%s want %s", got, tt.want)
			}
			if failure != tt.failure {
				t.Fatalf("failure=%q want %q", failure, tt.failure)
			}
		})
	}
}
