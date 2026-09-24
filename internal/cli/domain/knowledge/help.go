package knowledge

import "strings"

// CommandLong returns the knowledge command group introduction.
func CommandLong() string {
	return strings.TrimSpace(`Local knowledge artifact operations.

Subcommands: pull, ls, inspect, prune, rm.`)
}

// CommandExamples returns knowledge command examples for Cobra.
func CommandExamples() string {
	return strings.TrimSpace(`edgelet knowledge pull product-docs
  edgelet knowledge pull wiki-faiss --repo acme/wiki --revision 9f3c111122223333444455556666777788889999 --registry 3 --files data/**/*.jsonl --format jsonl
  edgelet knowledge ls
  edgelet knowledge inspect product-docs
  edgelet knowledge prune
  edgelet knowledge prune dangling
  edgelet knowledge rm product-docs`)
}

// PullCommandLong returns the knowledge pull command introduction.
func PullCommandLong() string {
	return strings.TrimSpace(`Download knowledge artifacts.

With only <name>, retry the existing deployed row.
With --repo and --registry, upsert the same row then pull.`)
}
