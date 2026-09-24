package format

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

func unpackModelPack(manifest ocispec.Manifest, blobs BlobOpener, destDir string) (*UnpackResult, error) {
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
		if name == "" {
			name = fmt.Sprintf("layer-%d%s", i, extensionForMedia(mt))
		}
		name = filepath.ToSlash(name)
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
		return nil, errors.New("modelpack artifact has no layers to materialize")
	}
	return &UnpackResult{RelPaths: relPaths, Format: inferFormat(mediaTypes, "")}, nil
}
