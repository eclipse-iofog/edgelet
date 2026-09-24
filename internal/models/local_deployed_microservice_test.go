package models

import (
	"testing"

	"github.com/eclipse-iofog/edgelet/internal/workloadmeta"
)

func TestLocalDeployedMicroserviceNormalizeDefaults(t *testing.T) {
	item := &LocalDeployedMicroservice{
		LocalUUID:       "local-1",
		State:           "running",
		Generation:      0,
		RuntimeState:    "",
		DesiredState:    "",
		ApplicationName: "",
	}

	item.NormalizeDefaults()

	if item.ApplicationName != workloadmeta.LocalDeployApplicationName {
		t.Fatalf("expected application_name %q, got %q", workloadmeta.LocalDeployApplicationName, item.ApplicationName)
	}
	if item.DesiredState != "running" {
		t.Fatalf("expected desired_state running, got %q", item.DesiredState)
	}
	if item.RuntimeState != "running" {
		t.Fatalf("expected runtime_state inferred from state, got %q", item.RuntimeState)
	}
	if item.Generation != 1 {
		t.Fatalf("expected generation 1, got %d", item.Generation)
	}
	if item.LastTransitionAt <= 0 {
		t.Fatalf("expected last_transition_at > 0, got %d", item.LastTransitionAt)
	}
	if item.LastReconcileAt != 0 {
		t.Fatalf("expected last_reconcile_at default 0, got %d", item.LastReconcileAt)
	}
	if item.LastStartAttemptAt != 0 {
		t.Fatalf("expected last_start_attempt_at default 0, got %d", item.LastStartAttemptAt)
	}
	if item.FailureCount != 0 {
		t.Fatalf("expected failure_count default 0, got %d", item.FailureCount)
	}
}

func TestLocalDeployedMicroserviceIsDeletingAndIsGone(t *testing.T) {
	ts := int64(1)
	deleting := &LocalDeployedMicroservice{
		DesiredState: "deleted",
		RuntimeState: "deleting",
		ContainerID:  "abc",
		DeletedAt:    &ts,
	}
	if !deleting.IsDeleting() {
		t.Fatal("expected deleting workload to report IsDeleting")
	}
	if deleting.IsGone() {
		t.Fatal("expected in-progress delete not to report IsGone")
	}

	tombstone := &LocalDeployedMicroservice{
		DesiredState: "deleted",
		RuntimeState: "deleted",
		DeletedAt:    &ts,
	}
	if tombstone.IsDeleting() {
		t.Fatal("expected tombstone not to report IsDeleting")
	}
	if !tombstone.IsGone() {
		t.Fatal("expected tombstone to report IsGone")
	}

	live := &LocalDeployedMicroservice{DesiredState: "running", RuntimeState: "running", ContainerID: "abc"}
	if live.IsDeleting() || live.IsGone() {
		t.Fatal("expected running workload to be live")
	}
}
