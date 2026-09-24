package processmanager

import (
	"testing"

	"github.com/eclipse-iofog/edgelet/internal/models"
)

func TestParseCRIReasonAndExitCode(t *testing.T) {
	reason, code := parseCRIReasonAndExitCode("CRI reason=CONTAINER_EXITED exitCode=255 message=container is in CONTAINER_EXITED state")
	if reason != "CONTAINER_EXITED" {
		t.Fatalf("expected CONTAINER_EXITED, got %q", reason)
	}
	if code != 255 {
		t.Fatalf("expected exit code 255, got %d", code)
	}
}

func TestShouldForceRecreateFromStatus(t *testing.T) {
	msg := "CRI reason=CONTAINER_EXITED exitCode=255 message=container is in CONTAINER_EXITED state"
	status := models.NewMicroserviceStatusWithState(models.MicroserviceStateExiting)
	status.ErrorMessage = &msg

	force, reason, code := shouldForceRecreateFromStatus(status)
	if !force {
		t.Fatal("expected force recreate for CONTAINER_EXITED")
	}
	if reason != "CONTAINER_EXITED" || code != 255 {
		t.Fatalf("unexpected parsed values reason=%q code=%d", reason, code)
	}
}

func TestShouldForceRecreateFromStatus_NonTerminalReason(t *testing.T) {
	msg := "CRI reason=OOMKILLED exitCode=137 message=oom"
	status := models.NewMicroserviceStatusWithState(models.MicroserviceStateExiting)
	status.ErrorMessage = &msg

	force, reason, code := shouldForceRecreateFromStatus(status)
	if force {
		t.Fatalf("did not expect force recreate for reason=%q code=%d", reason, code)
	}
}

func TestShouldForceRecreateFromStatus_DockerInspectText(t *testing.T) {
	msg := "exitCode=1 oomKilled=false error=config missing"
	status := models.NewMicroserviceStatusWithState(models.MicroserviceStateExiting)
	status.ErrorMessage = &msg

	force, reason, code := shouldForceRecreateFromStatus(status)
	if force {
		t.Fatalf("docker inspect text must not force CRI recreate, reason=%q code=%d", reason, code)
	}
}
