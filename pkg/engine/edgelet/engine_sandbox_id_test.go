//go:build linux

package edgelet

import "testing"

func TestGetContainerSandboxID_FromStore(t *testing.T) {
	e := New("")
	e.store.set("app-1", &containerState{sandboxID: "pause-1"})
	got, err := e.GetContainerSandboxID("app-1")
	if err != nil {
		t.Fatalf("sandbox id: %v", err)
	}
	if got != "pause-1" {
		t.Fatalf("got %q want pause-1", got)
	}
}

func TestGetContainerSandboxID_MissingOmitted(t *testing.T) {
	e := New("")
	got, err := e.GetContainerSandboxID("missing")
	if err != nil {
		t.Fatalf("sandbox id: %v", err)
	}
	if got != "" {
		t.Fatalf("expected empty sandbox id, got %q", got)
	}
}
