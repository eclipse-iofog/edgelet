package format

import (
	"strings"

	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

// Detect returns the packaging format for an OCI manifest.
// Order: Docker model-spec, CNCF ModelPack, KitOps ModelKit, generic ORAS.
func Detect(manifest ocispec.Manifest) Kind {
	if isDockerModelSpec(manifest) {
		return KindDocker
	}
	if isModelPack(manifest) {
		return KindModelPack
	}
	if isModelKit(manifest) {
		return KindModelKit
	}
	return KindORAS
}

func isDockerModelSpec(manifest ocispec.Manifest) bool {
	mt := strings.ToLower(strings.TrimSpace(manifest.Config.MediaType))
	if strings.HasPrefix(mt, prefixDockerModelConfig) {
		return true
	}
	if strings.HasPrefix(mt, prefixDockerAI) && strings.Contains(mt, "model.config") {
		return true
	}
	return false
}

func isModelPack(manifest ocispec.Manifest) bool {
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(manifest.ArtifactType)), prefixCNCFModel) {
		return true
	}
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(manifest.Config.MediaType)), prefixCNCFModel) {
		return true
	}
	for _, layer := range manifest.Layers {
		if strings.HasPrefix(strings.ToLower(strings.TrimSpace(layer.MediaType)), prefixCNCFModel) {
			return true
		}
		if filepathAnnotation(layer.Annotations) != "" {
			return true
		}
	}
	return false
}

func isModelKit(manifest ocispec.Manifest) bool {
	cfg := strings.ToLower(strings.TrimSpace(manifest.Config.MediaType))
	if strings.HasPrefix(cfg, prefixKitOps) || strings.HasPrefix(cfg, prefixJozu) {
		return true
	}
	if kitfileAnnotation(manifest.Annotations) != "" {
		return true
	}
	for _, layer := range manifest.Layers {
		mt := strings.ToLower(strings.TrimSpace(layer.MediaType))
		if strings.HasPrefix(mt, prefixKitOps) || strings.HasPrefix(mt, prefixJozu) {
			return true
		}
		if kitfileAnnotation(layer.Annotations) != "" {
			return true
		}
	}
	return false
}

func filepathAnnotation(ann map[string]string) string {
	if ann == nil {
		return ""
	}
	return strings.TrimSpace(ann[AnnotationFilePath])
}

func kitfileAnnotation(ann map[string]string) string {
	if ann == nil {
		return ""
	}
	if v := strings.TrimSpace(ann[AnnotationKitfile]); v != "" {
		return v
	}
	return strings.TrimSpace(ann[AnnotationKitfileAlt])
}

func titleAnnotation(ann map[string]string) string {
	if ann == nil {
		return ""
	}
	return strings.TrimSpace(ann[AnnotationTitle])
}
