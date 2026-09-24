package supervisor

import (
	"os"
	"strings"
	"testing"
)

func TestSupervisorStartsEdgeGuardWithoutResourceManager(t *testing.T) {
	src, err := os.ReadFile("supervisor.go")
	if err != nil {
		t.Fatalf("read supervisor.go: %v", err)
	}
	text := string(src)
	if strings.Contains(text, "resourcemanager") {
		t.Fatal("supervisor must not import or start Resource Manager")
	}
	if strings.Contains(text, "resourceManager") {
		t.Fatal("supervisor must not keep a Resource Manager field")
	}
	if !strings.Contains(text, "edgeGuardManager.Start()") {
		t.Fatal("supervisor must still start Edge Guard")
	}
}
