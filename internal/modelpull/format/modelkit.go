package format

import (
	"errors"
	"fmt"
	"strings"

	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

func unpackModelKit(manifest ocispec.Manifest, blobs BlobOpener, destDir string) (*UnpackResult, error) {
	var (
		relPaths   []string
		mediaTypes []string
	)
	if kit := kitfileAnnotation(manifest.Annotations); kit != "" {
		// Kitfile is metadata only; layers hold the payload.
		_ = kit
	}
	for i, layer := range manifest.Layers {
		mt := strings.ToLower(strings.TrimSpace(layer.MediaType))
		mediaTypes = append(mediaTypes, mt)
		if isTarMedia(mt) || strings.HasPrefix(mt, prefixKitOps) || strings.HasPrefix(mt, prefixJozu) {
			if isTarMedia(mt) || strings.Contains(mt, "tar") {
				paths, err := unpackTarLayer(blobs, digestString(layer), destDir, isGzipTarMedia(mt))
				if err != nil {
					return nil, err
				}
				relPaths = append(relPaths, prefixContent(paths)...)
				continue
			}
		}
		name := filepathAnnotation(layer.Annotations)
		if name == "" {
			name = titleAnnotation(layer.Annotations)
		}
		if name == "" {
			name = fallbackLayerName(layer, mt)
			if strings.HasSuffix(name, ".bin") {
				name = fmt.Sprintf("layer-%d%s", i, extensionForMedia(mt))
			}
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
		return nil, errors.New("modelkit artifact has no layers to materialize")
	}
	return &UnpackResult{RelPaths: relPaths, Format: inferFormat(mediaTypes, "")}, nil
}
