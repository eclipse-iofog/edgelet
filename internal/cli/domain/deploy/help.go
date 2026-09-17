package deploy

import "strings"

// CommandLong returns the deploy command introduction.
func CommandLong() string {
	return strings.TrimSpace(`Apply or validate a local manifest via EdgeletAPI v1.

Supported kinds: Microservice, Registry, Model, RuntimeClass, and ControlPlane (singleton controller per node).
Manifest kind is auto-detected from the YAML file.`)
}

// CommandExamples returns deploy command examples for Cobra.
func CommandExamples() string {
	return strings.TrimSpace(`edgelet deploy -f microservice.yaml
  edgelet deploy -f microservice.yaml --dry-run
  edgelet deploy -f microservice.yaml --sourceName my-app
  edgelet deploy -f registry.yaml
  edgelet deploy -f model.yaml
  edgelet deploy -f controlplane.yaml
  edgelet deploy -f controlplane.yaml --dry-run
  edgelet -o json deploy -f microservice.yaml --dry-run`)
}
