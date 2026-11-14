package stage

import (
	"fmt"
	"slices"

	"github.com/eclipse-iofog/edgelet-kube/internal/config"
	"github.com/eclipse-iofog/edgelet-kube/internal/containerd"
	"go.yaml.in/yaml/v3"
)

const runtimeAddon = "runtimes"

type runtimeClassDoc struct {
	APIVersion string           `yaml:"apiVersion"`
	Kind       string           `yaml:"kind"`
	Metadata   runtimeClassMeta `yaml:"metadata"`
	Handler    string           `yaml:"handler"`
}

type runtimeClassMeta struct {
	Name string `yaml:"name"`
}

// runtimeManifests stages one RuntimeClass for each CRI handler this node
// registers. crun is always registered. A catalog handler is registered only
// when its binary is on PATH. Handlers that are not registered are deleted
// so a pod cannot name a runtime this node does not have. Disabling the
// addon deletes every class in the catalog, including crun. Pods that omit
// runtimeClassName keep using the default handler.
func runtimeManifests(srv config.Server) ([]manifest, error) {
	return runtimeClassManifests(!disabled(srv.Disable, runtimeAddon), containerd.RegisteredRuntimeHandlers(), containerd.KnownRuntimeHandlers())
}

func runtimeClassManifests(enabled bool, registered, known []string) ([]manifest, error) {
	reg := handlerSet(registered)
	all := handlerSet(known)
	for name := range reg {
		all[name] = struct{}{}
	}
	var applyNames, removeNames []string
	if enabled {
		applyNames = sortedKeys(reg)
		for _, name := range sortedKeys(all) {
			if _, ok := reg[name]; !ok {
				removeNames = append(removeNames, name)
			}
		}
	} else {
		removeNames = sortedKeys(all)
	}
	var out []manifest
	if len(applyNames) > 0 {
		body, err := runtimeClassYAML(applyNames)
		if err != nil {
			return nil, err
		}
		out = append(out, manifest{name: runtimeAddon, enabled: true, body: body})
	}
	if len(removeNames) > 0 {
		body, err := runtimeClassYAML(removeNames)
		if err != nil {
			return nil, err
		}
		out = append(out, manifest{name: runtimeAddon, enabled: false, body: body})
	}
	return out, nil
}

func runtimeClassYAML(handlers []string) ([]byte, error) {
	docs := make([][]byte, 0, len(handlers))
	for _, name := range handlers {
		if !validRuntimeHandler(name) {
			return nil, fmt.Errorf("runtime handler %q is not a RuntimeClass name", name)
		}
		body, err := yaml.Marshal(runtimeClassDoc{
			APIVersion: "node.k8s.io/v1",
			Kind:       "RuntimeClass",
			Metadata:   runtimeClassMeta{Name: name},
			Handler:    name,
		})
		if err != nil {
			return nil, fmt.Errorf("runtime class %s: %w", name, err)
		}
		docs = append(docs, body)
	}
	return joinYAML(docs...), nil
}

func validRuntimeHandler(name string) bool {
	if name == "" || len(name) > 63 {
		return false
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9':
		case c == '-' && i > 0 && i < len(name)-1:
		default:
			return false
		}
	}
	return true
}

func handlerSet(names []string) map[string]struct{} {
	out := make(map[string]struct{}, len(names))
	for _, name := range names {
		if name == "" {
			continue
		}
		out[name] = struct{}{}
	}
	return out
}

func sortedKeys(handlers map[string]struct{}) []string {
	out := make([]string, 0, len(handlers))
	for name := range handlers {
		out = append(out, name)
	}
	slices.Sort(out)
	return out
}
