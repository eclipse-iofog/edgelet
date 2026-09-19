package volume

import "strings"

// CommandLong returns the volume command group introduction.
func CommandLong() string {
	return strings.TrimSpace(`Persistent VOLUME operations on this agent.

Private volumes live per microservice UUID. Shared volumes are node-global names
consumed by local and controller microservices. BIND host paths are never listed
or deleted here.

Subcommands: ls, rm, prune.`)
}

// CommandExamples returns volume command examples for Cobra.
func CommandExamples() string {
	return strings.TrimSpace(`edgelet volume ls
  edgelet volume rm <uuid>
  edgelet volume rm <uuid> <name> --force
  edgelet volume rm --shared <name>
  edgelet volume prune
  edgelet volume prune --orphans --yes`)
}

// RemoveCommandLong returns help for volume rm.
func RemoveCommandLong() string {
	return strings.TrimSpace(`Remove persistent VOLUME data.

volume rm <uuid> [<name>] destroys a private claim under volumes/data.
volume rm --shared <name> destroys a shared claim under volumes/shared.

Refuses if the claim is still desired or mounted. --force bypasses the desired-state
gate only when nothing is mounted.`)
}

// PruneCommandLong returns help for volume prune.
func PruneCommandLong() string {
	return strings.TrimSpace(`List or destroy unreferenced persistent VOLUME claims.

Default is a dry-run of orphan candidates. --yes is required to destroy.
Skip claims younger than 24h unless --force. Control-plane volumes are never pruned.`)
}
