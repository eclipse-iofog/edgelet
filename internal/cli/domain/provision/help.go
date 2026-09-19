package provision

import "strings"

// ProvisionLong returns the provision command introduction.
//
//nolint:revive // exported API
func ProvisionLong() string {
	return strings.TrimSpace(`Register this agent with an ioFog Controller using a provisioning key.

The key is issued by the Controller when creating or enrolling an agent.`)
}

// ProvisionExamples returns provision command examples for Cobra.
//
//nolint:revive // exported API
func ProvisionExamples() string {
	return strings.TrimSpace(`edgelet provision <provisioning-key>
  edgelet -o json provision <provisioning-key>`)
}

// DeprovisionLong returns the deprovision command introduction.
func DeprovisionLong() string {
	return strings.TrimSpace(`Remove agent provisioning and begin cleanup of managed resources.

WARNING: Deprovisioning stops controller management and may remove managed microservices.
Use --keep-local or --scope local to preserve locally deployed microservices.
Persistent VOLUME data under volumes/data and volumes/shared is preserved unless
--purge-volumes is set. Control-plane volumes are never purged.`)
}

// DeprovisionExamples returns deprovision command examples for Cobra.
func DeprovisionExamples() string {
	return strings.TrimSpace(`edgelet deprovision
  edgelet deprovision --scope local
  edgelet deprovision --keep-local
  edgelet deprovision --scope all --purge-volumes`)
}
