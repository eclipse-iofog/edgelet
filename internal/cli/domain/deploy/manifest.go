package deploy

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

// Target is a EdgeletAPI deploy collection.
type Target string

const (
	TargetMicroservices  Target = "microservices"
	TargetRegistries     Target = "registries"
	TargetModels         Target = "models"
	TargetKnowledge      Target = "knowledge"
	TargetRuntimeClasses Target = "runtimeclasses"
	TargetControlPlane   Target = "controlplane"
)

type manifestDocument struct {
	Kind string
	Raw  string
}

// DetectTargetFromManifest inspects manifest kind to choose the deploy API target.
func DetectTargetFromManifest(path string) (Target, error) {
	kind, err := DetectManifestKind(path)
	if err != nil {
		return TargetMicroservices, err
	}
	switch {
	case strings.EqualFold(kind, "Registry"):
		return TargetRegistries, nil
	case strings.EqualFold(kind, "Model"):
		return TargetModels, nil
	case strings.EqualFold(kind, "Knowledge"):
		return TargetKnowledge, nil
	case strings.EqualFold(kind, "RuntimeClass"):
		return TargetRuntimeClasses, nil
	case strings.EqualFold(kind, "ControlPlane"):
		return TargetControlPlane, nil
	default:
		return TargetMicroservices, nil
	}
}

// DetectManifestKind reads the first document's kind field.
func DetectManifestKind(path string) (string, error) {
	docs, err := splitManifestDocuments(path)
	if err != nil {
		return "", err
	}
	if len(docs) == 0 {
		return "", errors.New("manifest is empty")
	}
	return docs[0].Kind, nil
}

func splitManifestDocuments(path string) ([]manifestDocument, error) {
	raw, err := os.ReadFile(path) // #nosec G304 CLI manifest path provided by caller
	if err != nil {
		return nil, err
	}
	return decodeManifestDocuments(raw)
}

func decodeManifestDocuments(raw []byte) ([]manifestDocument, error) {
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	var docs []manifestDocument
	for {
		var node yaml.Node
		err := dec.Decode(&node)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		if node.Kind == 0 {
			continue
		}
		out, err := yaml.Marshal(&node)
		if err != nil {
			return nil, err
		}
		var hdr struct {
			Kind string `yaml:"kind"`
		}
		if err := yaml.Unmarshal(out, &hdr); err != nil {
			return nil, err
		}
		kind := strings.TrimSpace(hdr.Kind)
		if kind == "" && strings.TrimSpace(string(out)) == "" {
			continue
		}
		docs = append(docs, manifestDocument{Kind: kind, Raw: string(out)})
	}
	if len(docs) == 0 {
		return nil, errors.New("manifest is empty")
	}
	return docs, nil
}

func partitionManifestDocuments(docs []manifestDocument) (models, microservices []manifestDocument, err error) {
	for _, doc := range docs {
		switch {
		case strings.EqualFold(doc.Kind, "Model"):
			models = append(models, doc)
		case strings.EqualFold(doc.Kind, "Microservice"):
			microservices = append(microservices, doc)
		default:
			return nil, nil, fmt.Errorf("mixed manifest cannot include kind %q with Model and Microservice documents", doc.Kind)
		}
	}
	return models, microservices, nil
}

func joinManifestDocuments(docs []manifestDocument) string {
	parts := make([]string, 0, len(docs))
	for _, doc := range docs {
		trimmed := strings.TrimSpace(doc.Raw)
		if trimmed == "" {
			continue
		}
		parts = append(parts, trimmed)
	}
	return strings.Join(parts, "\n---\n")
}

func (t Target) validatePath() string {
	return "/v1/deploy/" + string(t) + ":validate"
}

func (t Target) applyPath() string {
	return "/v1/deploy/" + string(t) + ":apply"
}

func (t Target) applyStatusPath(operationID string) string {
	return "/v1/deploy/" + string(t) + ":apply/" + operationID
}
