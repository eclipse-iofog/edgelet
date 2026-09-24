package format

import (
	"errors"
	"fmt"
	"strings"

	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

func unpackORAS(manifest ocispec.Manifest, blobs BlobOpener, destDir string) (*UnpackResult, error) {
	if len(manifest.Layers) == 0 {
		return nil, errors.New("oras artifact has no layers to materialize")
	}
	var (
		relPaths   []string
		mediaTypes []string
	)
	for i, layer := range manifest.Layers {
		mt := strings.ToLower(strings.TrimSpace(layer.MediaType))
		mediaTypes = append(mediaTypes, mt)
		if isTarMedia(mt) {
			paths, err := unpackTarLayer(blobs, digestString(layer), destDir, isGzipTarMedia(mt))
			if err != nil {
				return nil, err
			}
			relPaths = append(relPaths, prefixContent(paths)...)
			continue
		}
		name := filepathAnnotation(layer.Annotations)
		if name == "" {
			name = titleAnnotation(layer.Annotations)
		}
		if name == "" && len(manifest.Layers) == 1 {
			name = "model.bin"
		}
		if name == "" {
			name = fmt.Sprintf("layer-%d%s", i, extensionForMedia(mt))
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
	return &UnpackResult{RelPaths: relPaths, Format: inferFormat(mediaTypes, "")}, nil
}
