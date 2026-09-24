package microservice

import "strings"

// CommandLong returns the ms command group introduction.
func CommandLong() string {
	return strings.TrimSpace(`Microservice lifecycle and observability on this agent.

Subcommands: ls, inspect, logs, exec, start, stop, restart, kill, rm.`)
}

// CommandExamples returns ms command examples for Cobra.
func CommandExamples() string {
	return strings.TrimSpace(`edgelet ms ls -o json
  edgelet ms ls --source local
  edgelet ms inspect <uuid>
  edgelet ms logs <uuid> --follow
  edgelet ms exec <uuid> -- /bin/sh`)
}

// KillCommandLong returns help for ms kill.
func KillCommandLong() string {
	return strings.TrimSpace(`Forcefully terminate a microservice container.

WARNING: kill sends SIGKILL-equivalent termination; in-flight work may be lost.`)
}

// RemoveCommandLong returns help for ms rm.
func RemoveCommandLong() string {
	return strings.TrimSpace(`Remove a microservice and its local deployment state.

Persistent VOLUME directories are retained. Shared claims drop this consumer only.
--cleanup records a reserved bit for a later explicit volume prune; it does not
delete data now and does not start a sweeper.

WARNING: This deletes the microservice record and associated container resources.`)
}
