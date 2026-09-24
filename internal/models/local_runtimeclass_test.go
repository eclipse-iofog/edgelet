package models

import (
	"encoding/json"
	"testing"
)

func TestLocalRuntimeClassNormalizeDefaultSource(t *testing.T) {
	rc := &LocalRuntimeClass{Name: "Spin", Handler: "Spin"}
	rc.Normalize()
	if rc.Name != "spin" || rc.Handler != "spin" {
		t.Fatalf("expected lowercased identity, got %+v", rc)
	}
	if rc.Source != RuntimeClassSourceLocal {
		t.Fatalf("expected default source local, got %q", rc.Source)
	}

	managed := &LocalRuntimeClass{Name: "nvidia", Handler: "nvidia", Source: "Managed"}
	managed.Normalize()
	if managed.Source != RuntimeClassSourceManaged {
		t.Fatalf("expected managed source, got %q", managed.Source)
	}
}

func TestValidRuntimeClassSource(t *testing.T) {
	if !ValidRuntimeClassSource(RuntimeClassSourceLocal) || !ValidRuntimeClassSource("MANAGED") {
		t.Fatal("expected local and managed to be valid")
	}
	if ValidRuntimeClassSource("") || ValidRuntimeClassSource("fleet") {
		t.Fatal("expected empty and unknown sources to be invalid")
	}
}

func TestControllerRuntimeClassJSONIgnoresUnknownKeys(t *testing.T) {
	raw := []byte(`{"name":"spin","handler":"spin","podId":"ignored","extra":true}`)
	var row ControllerRuntimeClass
	if err := json.Unmarshal(raw, &row); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	row.NormalizeDefaults()
	if row.Name != "spin" || row.Handler != "spin" {
		t.Fatalf("unexpected row: %+v", row)
	}
}
