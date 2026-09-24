package model

import "strings"

// CommandLong returns the model command group introduction.
func CommandLong() string {
	return strings.TrimSpace(`Local model artifact operations.

Subcommands: pull, ls, inspect, prune, rm.`)
}

// CommandExamples returns model command examples for Cobra.
func CommandExamples() string {
	return strings.TrimSpace(`edgelet model pull llama-2-7b-q2k
  edgelet model pull tiny-gpt2 --repo hf-internal-testing/tiny-random-gpt2 --registry 3 --files config.json
  edgelet model ls
  edgelet model inspect llama-2-7b-q2k
  edgelet model prune
  edgelet model prune dangling
  edgelet model rm llama-2-7b-q2k`)
}

// PullCommandLong returns the model pull command introduction.
func PullCommandLong() string {
	return strings.TrimSpace(`Download model artifacts.

With only <name>, retry the existing deployed row.
With --repo and --registry, upsert the same row then pull.`)
}
