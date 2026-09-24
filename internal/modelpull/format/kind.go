// Package format detects and unpacks OCI AI artifact packaging formats.
package format

// Kind is a detected OCI AI artifact packaging format.
type Kind string

const (
	KindDocker    Kind = "docker-model-spec"
	KindModelPack Kind = "modelpack"
	KindModelKit  Kind = "modelkit"
	KindORAS      Kind = "oras"
)

// Media types used to detect and unpack model artifacts.
const (
	MediaDockerModelConfigV01 = "application/vnd.docker.ai.model.config.v0.1+json"
	MediaDockerModelConfigV02 = "application/vnd.docker.ai.model.config.v0.2+json"
	MediaDockerGGUF           = "application/vnd.docker.ai.gguf.v3"
	MediaDockerGGUFLora       = "application/vnd.docker.ai.gguf.v3.lora"
	MediaDockerGGUFMMProj     = "application/vnd.docker.ai.gguf.v3.mmproj"
	MediaDockerLicense        = "application/vnd.docker.ai.license"
	MediaDockerChatTemplate   = "application/vnd.docker.ai.chat.template.jinja"
	MediaDockerSafetensors    = "application/vnd.docker.ai.safetensors"
	MediaDockerMMProj         = "application/vnd.docker.ai.mmproj"
	MediaDockerModelFile      = "application/vnd.docker.ai.model.file"
	MediaDockerDDUF           = "application/vnd.docker.ai.dduf"

	MediaModelPackManifest = "application/vnd.cncf.model.manifest.v1+json"
	MediaModelPackConfig   = "application/vnd.cncf.model.config.v1+json"

	MediaKitOpsConfig = "application/vnd.kitops.modelkit.config.v1+json"
	MediaJozuConfig   = "application/vnd.jozu.model.config.v1+json"

	AnnotationFilePath   = "org.cncf.model.filepath"
	AnnotationTitle      = "org.opencontainers.image.title"
	AnnotationKitfile    = "ml.kitops.modelkit.kitfile"
	AnnotationKitfileAlt = "org.kitops.modelkit.kitfile"
	MediaTypeOctetStream = "application/octet-stream"
)

const (
	prefixDockerModelConfig = "application/vnd.docker.ai.model.config"
	prefixDockerAI          = "application/vnd.docker.ai."
	prefixCNCFModel         = "application/vnd.cncf.model."
	prefixKitOps            = "application/vnd.kitops.modelkit."
	prefixJozu              = "application/vnd.jozu.model."
)
