package resourceconsumption

import (
	"runtime"
	"strings"
	"testing"
)

func TestFormatHostOSVersion(t *testing.T) {
	tests := map[string]struct {
		platform, version string
		want              string
	}{
		"ubuntu":        {platform: "ubuntu", version: "22.04", want: "Ubuntu 22.04"},
		"version only":  {platform: "", version: "14.6.1", want: "14.6.1"},
		"platform only": {platform: "debian", version: "", want: "Debian"},
		"empty":         {platform: "", version: "", want: ""},
	}
	for name, tc := range tests {
		got := formatHostOSVersion(tc.platform, tc.version)
		if got != tc.want {
			t.Fatalf("%s: got %q want %q", name, got, tc.want)
		}
	}
}

func TestCollectHostIdentity_FamilyAndKernel(t *testing.T) {
	id := collectHostIdentity()
	if id.os == "" {
		t.Fatal("expected non-empty systemOs family")
	}
	if id.os != strings.ToLower(runtime.GOOS) {
		t.Fatalf("systemOs: got %q want %q", id.os, runtime.GOOS)
	}
	if runtime.GOOS == "linux" && id.kernelVersion == "" {
		t.Fatal("expected kernel version on linux")
	}
	if runtime.GOOS != "linux" && id.kernelVersion != "" {
		t.Fatalf("expected empty kernel on non-linux, got os=%q kernel=%q", id.os, id.kernelVersion)
	}
}
