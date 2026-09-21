## edgelet knowledge prune

Prune unreferenced knowledge

### Synopsis

Prune dangling knowledge only (on-disk artifacts with no deployed row or workload reference).

```
edgelet knowledge prune [dangling] [flags]
```

### Examples

```
edgelet knowledge prune
edgelet knowledge prune dangling
edgelet knowledge prune --mode dangling
```

### Options

```
  -h, --help          help for prune
  -m, --mode string   Prune mode (only: dangling)
```

### Options inherited from parent commands

```
      --debug            Debug logging
      --no-color         Disable color and interactive UX
  -o, --output string    Output format: human, json, yaml (default "human")
      --quiet            Suppress interactive progress output
      --socket string    Edgelet API unix socket path
      --timeout string   Request timeout
      --verbose          Verbose logging
```

### SEE ALSO

* [edgelet knowledge](edgelet_knowledge.md)	 - Knowledge operations


