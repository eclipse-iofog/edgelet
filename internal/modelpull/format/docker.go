package format

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

func unpackDocker(manifest ocispec.Manifest, blobs BlobOpener, destDir string) (*UnpackResult, error) {
	var (
		ggufIdx    int
		ggufTotal  int
		safeIdx    int
		safeTotal  int
		relPaths   []string
		mediaTypes []string
	)
	for _, layer := range manifest.Layers {
		switch strings.ToLower(layer.MediaType) {
		case MediaDockerGGUF:
			ggufTotal++
		case MediaDockerSafetensors:
			safeTotal++
		}
	}

	for _, layer := range manifest.Layers {
		mt := strings.ToLower(strings.TrimSpace(layer.MediaType))
		mediaTypes = append(mediaTypes, mt)
		name := filepathAnnotation(layer.Annotations)
		if name == "" {
			name = dockerLayerName(mt, &ggufIdx, ggufTotal, &safeIdx, safeTotal)
		}
		if name == "" {
			name = fallbackLayerName(layer, mt)
		}
		if isTarMedia(mt) {
			paths, err := unpackTarLayer(blobs, digestString(layer), destDir, isGzipTarMedia(mt))
			if err != nil {
				return nil, err
			}
			relPaths = append(relPaths, prefixContent(paths)...)
			continue
		}
		dest, err := safeJoin(destDir, name)
		if err != nil {
			return nil, err
		}
		if err := copyBlobToFile(blobs, digestString(layer), dest); err != nil {
			return nil, err
		}
		relPaths = append(relPaths, contentRel(name))
	}
	if len(relPaths) == 0 {
		return nil, errors.New("docker model artifact has no layers to materialize")
	}
	return &UnpackResult{RelPaths: relPaths, Format: inferFormat(mediaTypes, "")}, nil
}

func dockerLayerName(mediaType string, ggufIdx *int, ggufTotal int, safeIdx *int, safeTotal int) string {
	switch mediaType {
	case MediaDockerGGUF:
		*ggufIdx++
		if ggufTotal <= 1 {
			return "model.gguf"
		}
		return fmt.Sprintf("model-%05d-of-%05d.gguf", *ggufIdx, ggufTotal)
	case MediaDockerGGUFLora:
		return "adapter.gguf"
	case MediaDockerGGUFMMProj, MediaDockerMMProj:
		return "model.mmproj"
	case MediaDockerLicense:
		return "LICENSE"
	case MediaDockerChatTemplate:
		return "template.jinja"
	case MediaDockerSafetensors:
		*safeIdx++
		if safeTotal <= 1 {
			return "model.safetensors"
		}
		return fmt.Sprintf("model-%05d-of-%05d.safetensors", *safeIdx, safeTotal)
	case MediaDockerDDUF:
		return "model.dduf"
	default:
		return ""
	}
}

func fallbackLayerName(layer ocispec.Descriptor, mediaType string) string {
	if title := titleAnnotation(layer.Annotations); title != "" {
		return filepath.Base(title)
	}
	hex := layer.Digest.Encoded()
	if hex == "" {
		hex = "blob"
	}
	ext := extensionForMedia(mediaType)
	return hex + ext
}

func extensionForMedia(mediaType string) string {
	switch {
	case strings.Contains(mediaType, "gguf"):
		return ".gguf"
	case strings.Contains(mediaType, "safetensors"):
		return ".safetensors"
	case strings.Contains(mediaType, "jinja"):
		return ".jinja"
	case strings.Contains(mediaType, "onnx"):
		return ".onnx"
	default:
		return ".bin"
	}
}

func prefixContent(paths []string) []string {
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		out = append(out, contentRel(p))
	}
	return out
}

func unpackTarLayer(blobs BlobOpener, digest, destDir string, gzipped bool) ([]string, error) {
	rc, err := blobs.Open(digest)
	if err != nil {
		return nil, fmt.Errorf("open tar blob %s: %w", digest, err)
	}
	defer func() {
		_ = rc.Close()
	}()
	return extractTar(rc, destDir, gzipped)
}
