package models

import "testing"

func TestVolumeMappingEqualsIncludesScope(t *testing.T) {
	private := NewVolumeMapping("config", "/app/config", "rw", VolumeMappingTypeVolume)
	alsoPrivate := NewVolumeMapping("config", "/app/config", "rw", VolumeMappingTypeVolume)
	if !private.Equals(alsoPrivate) {
		t.Fatal("empty and default private scope should be equal")
	}

	shared := NewVolumeMapping("config", "/app/config", "rw", VolumeMappingTypeVolume)
	shared.Scope = VolumeScopeShared
	if private.Equals(shared) {
		t.Fatal("private and shared volume mappings must not be equal")
	}

	bindShared := NewVolumeMapping("/var/lib/data", "/data", "rw", VolumeMappingTypeBind)
	bindShared.Scope = VolumeScopeShared
	bindPrivate := NewVolumeMapping("/var/lib/data", "/data", "rw", VolumeMappingTypeBind)
	if !bindPrivate.Equals(bindShared) {
		t.Fatal("BIND scope is ignored and must compare as private")
	}
}

func TestCanonicalVolumeScopeUnknownIsPrivate(t *testing.T) {
	if got := CanonicalVolumeScope(""); got != VolumeScopePrivate {
		t.Fatalf("empty=%q", got)
	}
	if got := CanonicalVolumeScope("shared"); got != VolumeScopeShared {
		t.Fatalf("shared=%q", got)
	}
	if got := CanonicalVolumeScope("SHARED"); got != VolumeScopePrivate {
		t.Fatalf("SHARED=%q want private", got)
	}
	if got := CanonicalVolumeScope("foo"); got != VolumeScopePrivate {
		t.Fatalf("foo=%q want private", got)
	}
}
