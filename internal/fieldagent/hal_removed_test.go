package fieldagent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFieldAgentOmitsHALControllerPaths(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read fieldagent package: %v", err)
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		data, err := os.ReadFile(filepath.Clean(entry.Name()))
		if err != nil {
			t.Fatalf("read %s: %v", entry.Name(), err)
		}
		src := string(data)
		for _, needle := range []string{"hal/hw", "hal/usb"} {
			if strings.Contains(src, needle) {
				t.Errorf("%s must not contain %q", entry.Name(), needle)
			}
		}
	}
}
