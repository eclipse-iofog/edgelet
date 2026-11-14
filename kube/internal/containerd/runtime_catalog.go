package containerd

import (
	"cmp"
	"errors"
	"fmt"
	"os/exec"
	"slices"
	"strings"
)

var lookPathForRuntimeCatalog = exec.LookPath

var errRuntimeHandlerUnavailable = errors.New("runtime handler unavailable")

// RuntimeFamily is how a discovered runtime is registered with CRI.
type RuntimeFamily string

const (
	// RuntimeFamilyRunc uses the bundled shim and a BinaryName option.
	RuntimeFamilyRunc RuntimeFamily = "runc"
	// RuntimeFamilyShim is a runtime whose executable is the shim itself.
	RuntimeFamilyShim RuntimeFamily = "shim"
)

type runtimeSpec struct {
	Handler     string
	RuntimeType string
	Family      RuntimeFamily
	Candidates  []string
}

var runtimeSpecs = map[string]runtimeSpec{
	"runc": {
		Handler:     "runc",
		RuntimeType: "io.containerd.runc.v2",
		Family:      RuntimeFamilyRunc,
		Candidates:  []string{"runc"},
	},
	"nvidia": {
		Handler:     "nvidia",
		RuntimeType: "io.containerd.runc.v2",
		Family:      RuntimeFamilyRunc,
		Candidates:  []string{"nvidia-container-runtime"},
	},
	"nvidia-experimental": {
		Handler:     "nvidia-experimental",
		RuntimeType: "io.containerd.runc.v2",
		Family:      RuntimeFamilyRunc,
		Candidates:  []string{"nvidia-container-runtime-experimental", "nvidia-container-runtime"},
	},
	"nvidia-cdi": {
		Handler:     "nvidia-cdi",
		RuntimeType: "io.containerd.runc.v2",
		Family:      RuntimeFamilyRunc,
		Candidates:  []string{"nvidia-container-runtime.cdi", "nvidia-container-runtime"},
	},
	"lunatic": {
		Handler:     "lunatic",
		RuntimeType: "io.containerd.lunatic.v1",
		Family:      RuntimeFamilyShim,
		Candidates:  []string{"containerd-shim-lunatic-v1", "containerd-shim-lunatic-v2", "lunatic"},
	},
	"slight": {
		Handler:     "slight",
		RuntimeType: "io.containerd.slight.v1",
		Family:      RuntimeFamilyShim,
		Candidates:  []string{"containerd-shim-slight-v1", "containerd-shim-slight-v2", "slight"},
	},
	"spin": {
		Handler:     "spin",
		RuntimeType: "io.containerd.spin.v2",
		Family:      RuntimeFamilyShim,
		Candidates:  []string{"containerd-shim-spin-v2", "containerd-shim-spin-v1", "spin"},
	},
	"wws": {
		Handler:     "wws",
		RuntimeType: "io.containerd.wws.v1",
		Family:      RuntimeFamilyShim,
		Candidates:  []string{"containerd-shim-wws-v1", "containerd-shim-wws-v2", "wws"},
	},
	"wasmedge": {
		Handler:     "wasmedge",
		RuntimeType: "io.containerd.wasmedge.v1",
		Family:      RuntimeFamilyShim,
		Candidates:  []string{"containerd-shim-wasmedge-v1", "containerd-shim-wasmedge-v2", "wasmedge"},
	},
	"wasmer": {
		Handler:     "wasmer",
		RuntimeType: "io.containerd.wasmer.v1",
		Family:      RuntimeFamilyShim,
		Candidates:  []string{"containerd-shim-wasmer-v1", "containerd-shim-wasmer-v2", "wasmer"},
	},
	"wasmtime": {
		Handler:     "wasmtime",
		RuntimeType: "io.containerd.wasmtime.v1",
		Family:      RuntimeFamilyShim,
		Candidates:  []string{"containerd-shim-wasmtime-v1", "containerd-shim-wasmtime-v2", "wasmtime"},
	},
	"edgelet-wasmtime": {
		Handler:     "edgelet-wasmtime",
		RuntimeType: "io.containerd.edgelet.v2",
		Family:      RuntimeFamilyShim,
		Candidates:  []string{"containerd-shim-edgelet-v2", "containerd-shim-edgelet-wasm-v2", "containerd-shim-edgelet", "edgelet-wasm"},
	},
}

// RuntimeCatalogEntry is one runtime handler found on PATH.
type RuntimeCatalogEntry struct {
	// Handler is the CRI runtime name.
	Handler string
	// Binary is the candidate name that resolved.
	Binary string
	// Path is the absolute executable path.
	Path string
	// RuntimeType is the containerd runtime type.
	RuntimeType string
	// Family selects how the handler is written into the containerd config.
	Family RuntimeFamily
}

// ErrRuntimeHandlerUnavailable indicates a requested handler is missing or not eligible.
type ErrRuntimeHandlerUnavailable struct {
	Handler    string
	Candidates []string
}

func (e *ErrRuntimeHandlerUnavailable) Error() string {
	handler := strings.TrimSpace(strings.ToLower(e.Handler))
	candidates := append([]string{}, e.Candidates...)
	if len(candidates) == 0 {
		return fmt.Sprintf("%v: handler %q is not supported", errRuntimeHandlerUnavailable, handler)
	}
	return fmt.Sprintf("%v: handler %q is not available in PATH (tried: %s)", errRuntimeHandlerUnavailable, handler, strings.Join(candidates, ", "))
}

func (e *ErrRuntimeHandlerUnavailable) Unwrap() error {
	return errRuntimeHandlerUnavailable
}

// RegisteredRuntimeHandlers returns the CRI handlers written into the
// containerd config. crun is always included because it is the default
// runtime. Every other name is a catalog runtime whose binary was found.
func RegisteredRuntimeHandlers() []string {
	handlers := map[string]struct{}{RuntimeName: {}}
	for _, entry := range BuildRuntimeCatalog() {
		if entry.Handler == "" || entry.Handler == RuntimeName {
			continue
		}
		handlers[entry.Handler] = struct{}{}
	}
	return sortedHandlers(handlers)
}

// KnownRuntimeHandlers returns every handler the catalog can register, plus crun.
// A name in this list that is not registered is removed from the API.
func KnownRuntimeHandlers() []string {
	handlers := map[string]struct{}{RuntimeName: {}}
	for name := range runtimeSpecs {
		handlers[name] = struct{}{}
	}
	return sortedHandlers(handlers)
}

func sortedHandlers(handlers map[string]struct{}) []string {
	out := make([]string, 0, len(handlers))
	for name := range handlers {
		out = append(out, name)
	}
	slices.Sort(out)
	return out
}

// BuildRuntimeCatalog discovers supported runtimes from PATH.
// Only discovered handlers are returned.
func BuildRuntimeCatalog() []RuntimeCatalogEntry {
	entries := make([]RuntimeCatalogEntry, 0, len(runtimeSpecs))
	entries = findRunCContainerRuntime(entries)
	entries = findNvidiaContainerRuntimes(entries)
	entries = findWasiRuntimes(entries)
	slices.SortFunc(entries, func(a, b RuntimeCatalogEntry) int {
		return cmp.Compare(a.Handler, b.Handler)
	})
	return entries
}

// ValidateRuntimeHandlerEligibility returns a runtime entry only when handler
// exists in the catalog and was discovered.
func ValidateRuntimeHandlerEligibility(handler string, catalog []RuntimeCatalogEntry) (RuntimeCatalogEntry, error) {
	normalized := strings.TrimSpace(strings.ToLower(handler))
	if _, ok := runtimeSpecs[normalized]; !ok {
		return RuntimeCatalogEntry{}, &ErrRuntimeHandlerUnavailable{
			Handler:    normalized,
			Candidates: runtimeHandlerLookupCandidates(normalized),
		}
	}
	entry, ok := runtimeEntryByHandler(normalized, catalog)
	if !ok {
		return RuntimeCatalogEntry{}, &ErrRuntimeHandlerUnavailable{
			Handler:    normalized,
			Candidates: runtimeHandlerLookupCandidates(normalized),
		}
	}
	return entry, nil
}

func runtimeEntryByHandler(handler string, catalog []RuntimeCatalogEntry) (RuntimeCatalogEntry, bool) {
	for _, item := range catalog {
		if item.Handler == handler {
			return item, true
		}
	}
	return RuntimeCatalogEntry{}, false
}

func runtimeHandlerLookupCandidates(handler string) []string {
	spec, ok := runtimeSpecs[handler]
	if !ok {
		return nil
	}
	return append([]string{}, spec.Candidates...)
}

func findRunCContainerRuntime(entries []RuntimeCatalogEntry) []RuntimeCatalogEntry {
	return searchForRuntimes(map[string]runtimeSpec{
		"runc": runtimeSpecs["runc"],
	}, entries)
}

func findNvidiaContainerRuntimes(entries []RuntimeCatalogEntry) []RuntimeCatalogEntry {
	return searchForRuntimes(map[string]runtimeSpec{
		"nvidia":              runtimeSpecs["nvidia"],
		"nvidia-cdi":          runtimeSpecs["nvidia-cdi"],
		"nvidia-experimental": runtimeSpecs["nvidia-experimental"],
	}, entries)
}

func findWasiRuntimes(entries []RuntimeCatalogEntry) []RuntimeCatalogEntry {
	return searchForRuntimes(map[string]runtimeSpec{
		"edgelet-wasmtime": runtimeSpecs["edgelet-wasmtime"],
		"lunatic":          runtimeSpecs["lunatic"],
		"slight":           runtimeSpecs["slight"],
		"spin":             runtimeSpecs["spin"],
		"wasmedge":         runtimeSpecs["wasmedge"],
		"wasmer":           runtimeSpecs["wasmer"],
		"wasmtime":         runtimeSpecs["wasmtime"],
		"wws":              runtimeSpecs["wws"],
	}, entries)
}

func searchForRuntimes(potential map[string]runtimeSpec, entries []RuntimeCatalogEntry) []RuntimeCatalogEntry {
	handlers := make([]string, 0, len(potential))
	for runtimeName := range potential {
		handlers = append(handlers, runtimeName)
	}
	slices.Sort(handlers)
	for _, runtimeName := range handlers {
		spec := potential[runtimeName]
		for _, candidate := range spec.Candidates {
			path, err := lookPathForRuntimeCatalog(candidate)
			if err != nil {
				continue
			}
			entries = append(entries, RuntimeCatalogEntry{
				Handler:     spec.Handler,
				Binary:      candidate,
				Path:        path,
				RuntimeType: spec.RuntimeType,
				Family:      spec.Family,
			})
			break
		}
	}
	return entries
}
