package models

import (
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

type runtimeManifestContainer struct {
	Runtime string `yaml:"runtime"`
}

type runtimeManifestSpec struct {
	Container runtimeManifestContainer `yaml:"container"`
}

type runtimeManifestDoc struct {
	Spec runtimeManifestSpec `yaml:"spec"`
}

// RuntimeFromManifestYAML returns spec.container.runtime from a local
// Microservice manifest, or empty when the document has no runtime.
func RuntimeFromManifestYAML(manifest string) string {
	var doc runtimeManifestDoc
	if err := yaml.Unmarshal([]byte(strings.TrimSpace(manifest)), &doc); err != nil {
		return ""
	}
	return strings.TrimSpace(strings.ToLower(doc.Spec.Container.Runtime))
}

// RuntimeClassBlockingUUIDs returns microservice UUIDs that still reference
// the RuntimeClass. Local workloads only block while running. Controller
// microservices block whenever they select the class and are not marked delete.
func RuntimeClassBlockingUUIDs(item *LocalRuntimeClass, locals []*LocalDeployedMicroservice, controller []*Microservice) []string {
	if item == nil {
		return nil
	}
	runtimeSet := runtimeClassNameSet(item)
	if len(runtimeSet) == 0 {
		return nil
	}

	blocking := make(map[string]struct{})
	for _, local := range locals {
		if local == nil || local.DeletedAt != nil {
			continue
		}
		state := strings.TrimSpace(strings.ToLower(local.RuntimeState))
		if state == "" {
			state = strings.TrimSpace(strings.ToLower(local.State))
		}
		if state != "running" {
			continue
		}
		runtime := RuntimeFromManifestYAML(local.ManifestYAML)
		if runtime == "" {
			continue
		}
		if _, used := runtimeSet[runtime]; !used {
			continue
		}
		if uuid := strings.TrimSpace(local.LocalUUID); uuid != "" {
			blocking[uuid] = struct{}{}
		}
	}
	for _, ms := range controller {
		if ms == nil || ms.Delete {
			continue
		}
		if ms.Runtime == nil {
			continue
		}
		runtime := strings.TrimSpace(strings.ToLower(*ms.Runtime))
		if runtime == "" {
			continue
		}
		if _, used := runtimeSet[runtime]; !used {
			continue
		}
		if uuid := strings.TrimSpace(ms.MicroserviceUUID); uuid != "" {
			blocking[uuid] = struct{}{}
		}
	}

	uuids := make([]string, 0, len(blocking))
	for uuid := range blocking {
		uuids = append(uuids, uuid)
	}
	slices.Sort(uuids)
	return uuids
}

func runtimeClassNameSet(item *LocalRuntimeClass) map[string]struct{} {
	names := make(map[string]struct{}, 2)
	for _, name := range []string{item.RuntimeName, item.Name} {
		name = strings.TrimSpace(strings.ToLower(name))
		if name == "" {
			continue
		}
		names[name] = struct{}{}
	}
	return names
}
