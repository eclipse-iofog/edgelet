package format

import (
	"testing"

	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

func TestDetect_DockerModelSpec(t *testing.T) {
	manifest := ocispec.Manifest{
		Config: ocispec.Descriptor{MediaType: MediaDockerModelConfigV01},
		Layers: []ocispec.Descriptor{{MediaType: MediaDockerGGUF}},
	}
	if got := Detect(manifest); got != KindDocker {
		t.Fatalf("got %q, want %q", got, KindDocker)
	}

	manifest.Config.MediaType = MediaDockerModelConfigV02
	manifest.Layers[0].Annotations = map[string]string{AnnotationFilePath: "weights/model.gguf"}
	if got := Detect(manifest); got != KindDocker {
		t.Fatalf("docker config with filepath annotation should stay docker, got %q", got)
	}
}

func TestDetect_ModelPack(t *testing.T) {
	byArtifact := ocispec.Manifest{
		ArtifactType: MediaModelPackManifest,
		Config:       ocispec.Descriptor{MediaType: MediaModelPackConfig},
		Layers: []ocispec.Descriptor{{
			MediaType:   "application/vnd.cncf.model.weight.v1.raw",
			Annotations: map[string]string{AnnotationFilePath: "model.safetensors"},
		}},
	}
	if got := Detect(byArtifact); got != KindModelPack {
		t.Fatalf("got %q, want %q", got, KindModelPack)
	}

	byAnnotation := ocispec.Manifest{
		Config: ocispec.Descriptor{MediaType: ocispec.MediaTypeImageConfig},
		Layers: []ocispec.Descriptor{{
			MediaType:   MediaTypeOctetStream,
			Annotations: map[string]string{AnnotationFilePath: "tokenizer.json"},
		}},
	}
	if got := Detect(byAnnotation); got != KindModelPack {
		t.Fatalf("filepath annotation should detect modelpack, got %q", got)
	}
}

func TestDetect_ModelKit(t *testing.T) {
	byConfig := ocispec.Manifest{
		Config: ocispec.Descriptor{MediaType: MediaKitOpsConfig},
		Layers: []ocispec.Descriptor{{MediaType: "application/vnd.kitops.modelkit.model.v1.tar+gzip"}},
	}
	if got := Detect(byConfig); got != KindModelKit {
		t.Fatalf("got %q, want %q", got, KindModelKit)
	}

	byKitfile := ocispec.Manifest{
		Config: ocispec.Descriptor{MediaType: ocispec.MediaTypeImageConfig},
		Annotations: map[string]string{
			AnnotationKitfile: `{"manifestVersion":"1.0"}`,
		},
		Layers: []ocispec.Descriptor{{MediaType: MediaTypeOctetStream}},
	}
	if got := Detect(byKitfile); got != KindModelKit {
		t.Fatalf("kitfile annotation should detect modelkit, got %q", got)
	}
}

func TestDetect_ORASFallback(t *testing.T) {
	manifest := ocispec.Manifest{
		Config: ocispec.Descriptor{MediaType: ocispec.MediaTypeImageConfig},
		Layers: []ocispec.Descriptor{{MediaType: MediaTypeOctetStream}},
	}
	if got := Detect(manifest); got != KindORAS {
		t.Fatalf("unknown layers should fall back to oras, got %q", got)
	}
}
