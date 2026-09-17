package format

import (
	"fmt"
	"strings"

	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

// UnpackResult is the set of files materialized under content/.
type UnpackResult struct {
	// RelPaths are paths relative to the model directory (content/…).
	RelPaths []string
	// Format is a model format hint inferred from layers (gguf, safetensors, …).
	Format string
}

// Unpack materializes artifact layers into destDir (the content/ directory).
func Unpack(kind Kind, manifest ocispec.Manifest, blobs BlobOpener, destDir string) (*UnpackResult, error) {
	switch kind {
	case KindDocker:
		return unpackDocker(manifest, blobs, destDir)
	case KindModelPack:
		return unpackModelPack(manifest, blobs, destDir)
	case KindModelKit:
		return unpackModelKit(manifest, blobs, destDir)
	case KindORAS:
		return unpackORAS(manifest, blobs, destDir)
	default:
		return nil, fmt.Errorf("unsupported model artifact format %q", kind)
	}
}

func inferFormat(mediaTypes []string, hint string) string {
	hint = strings.ToLower(strings.TrimSpace(hint))
	if hint != "" {
		return hint
	}
	for _, mt := range mediaTypes {
		switch {
		case strings.Contains(mt, "gguf"):
			return "gguf"
		case strings.Contains(mt, "safetensors"):
			return "safetensors"
		case strings.Contains(mt, "onnx"):
			return "onnx"
		case strings.Contains(mt, "dduf") || strings.Contains(mt, "diffusers"):
			return "pytorch"
		}
	}
	return "unknown"
}

func digestString(d ocispec.Descriptor) string {
	return string(d.Digest)
}
