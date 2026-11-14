package containerd

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
)

func TestBuildRuntimeCatalogOrderAndEligibility(t *testing.T) {
	prev := lookPathForRuntimeCatalog
	t.Cleanup(func() { lookPathForRuntimeCatalog = prev })

	lookPathForRuntimeCatalog = func(file string) (string, error) {
		switch file {
		case "runc":
			return "/usr/bin/runc", nil
		case "containerd-shim-spin-v2":
			return "/usr/local/bin/containerd-shim-spin-v2", nil
		case "containerd-shim-edgelet-v2":
			return "/opt/edgelet/containerd-shim-edgelet-v2", nil
		default:
			return "", fmt.Errorf("not found: %s", file)
		}
	}

	catalog := BuildRuntimeCatalog()
	if len(catalog) != 3 {
		t.Fatalf("expected found-only catalog entries, got %d", len(catalog))
	}
	for i := 1; i < len(catalog); i++ {
		if catalog[i-1].Handler > catalog[i].Handler {
			t.Fatalf("catalog must be sorted by handler: %q > %q", catalog[i-1].Handler, catalog[i].Handler)
		}
	}

	runc, ok := runtimeEntryByHandler("runc", catalog)
	if !ok || runc.Path != "/usr/bin/runc" || runc.Family != RuntimeFamilyRunc {
		t.Fatalf("expected discovered runc entry, got %+v", runc)
	}
	if _, ok := runtimeEntryByHandler("wasmer", catalog); ok {
		t.Fatal("wasmer should be absent when its binary is missing")
	}
	wasm, ok := runtimeEntryByHandler("edgelet-wasmtime", catalog)
	if !ok || wasm.Family != RuntimeFamilyShim || wasm.RuntimeType != "io.containerd.edgelet.v2" {
		t.Fatalf("expected discovered edgelet-wasmtime shim, got %+v", wasm)
	}
}

func TestBuildRuntimeCatalogUsesNvidiaExperimentalBinary(t *testing.T) {
	prev := lookPathForRuntimeCatalog
	t.Cleanup(func() { lookPathForRuntimeCatalog = prev })

	lookPathForRuntimeCatalog = func(file string) (string, error) {
		if file == "nvidia-container-runtime-experimental" {
			return "/usr/bin/nvidia-container-runtime-experimental", nil
		}
		return "", errors.New("not found")
	}

	catalog := BuildRuntimeCatalog()
	got, ok := runtimeEntryByHandler("nvidia-experimental", catalog)
	if !ok {
		t.Fatal("expected nvidia-experimental entry")
	}
	if got.Binary != "nvidia-container-runtime-experimental" {
		t.Fatalf("binary %q", got.Binary)
	}
}

func TestRegisteredRuntimeHandlersAlwaysIncludeCrun(t *testing.T) {
	prev := lookPathForRuntimeCatalog
	t.Cleanup(func() { lookPathForRuntimeCatalog = prev })
	lookPathForRuntimeCatalog = func(file string) (string, error) {
		switch file {
		case "nvidia-container-runtime.cdi":
			return "/usr/bin/nvidia-container-runtime.cdi", nil
		case "containerd-shim-edgelet-v2":
			return "/opt/edgelet/containerd-shim-edgelet-v2", nil
		default:
			return "", errors.New("not found")
		}
	}

	got := RegisteredRuntimeHandlers()
	want := []string{"crun", "edgelet-wasmtime", "nvidia-cdi"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("handlers %v", got)
	}
	for _, name := range []string{"wasmer", "runc", "nvidia"} {
		for _, gotName := range got {
			if gotName == name {
				t.Fatalf("unregistered handler %s was listed", name)
			}
		}
	}
	known := KnownRuntimeHandlers()
	for _, name := range []string{"crun", "runc", "nvidia-cdi", "edgelet-wasmtime", "wasmer"} {
		if !slices.Contains(known, name) {
			t.Fatalf("known handlers %v missing %s", known, name)
		}
	}
}

func TestValidateRuntimeHandlerEligibility(t *testing.T) {
	catalog := []RuntimeCatalogEntry{
		{
			Handler:     "spin",
			Binary:      "containerd-shim-spin-v2",
			Path:        "/usr/local/bin/containerd-shim-spin-v2",
			RuntimeType: "io.containerd.spin.v2",
			Family:      RuntimeFamilyShim,
		},
	}
	if _, err := ValidateRuntimeHandlerEligibility("spin", catalog); err != nil {
		t.Fatal(err)
	}

	_, err := ValidateRuntimeHandlerEligibility("wasmer", catalog)
	if err == nil {
		t.Fatal("expected an error for wasmer")
	}
	var unavailable *ErrRuntimeHandlerUnavailable
	if !errors.As(err, &unavailable) {
		t.Fatalf("error type %T", err)
	}
	if !strings.Contains(err.Error(), "handler \"wasmer\" is not available in PATH") {
		t.Fatalf("error %q", err)
	}

	_, err = ValidateRuntimeHandlerEligibility("unknown", catalog)
	if err == nil || !strings.Contains(err.Error(), "handler \"unknown\" is not supported") {
		t.Fatalf("error %v", err)
	}
}
