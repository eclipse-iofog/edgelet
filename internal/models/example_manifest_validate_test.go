package models

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestOperatorMicroserviceExampleValidates(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	path := filepath.Join(filepath.Dir(thisFile), "..", "..", "docs", "edgelet", "examples", "microservice.yaml")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read example: %v", err)
	}
	doc := &LocalDeployManifest{}
	if err := yaml.Unmarshal(raw, doc); err != nil {
		t.Fatalf("unmarshal example: %v", err)
	}
	if err := doc.Validate(); err != nil {
		t.Fatalf("example must pass container/catalog validate: %v", err)
	}
	if doc.Spec.Models == nil || !doc.Spec.Models.HasItems() {
		t.Fatal("example must include a catalog bind")
	}
}
