## edgelet model prune

Prune unreferenced models

### Synopsis

Prune dangling models only (on-disk artifacts with no deployed row or workload reference).

```
edgelet model prune [dangling] [flags]
```

### Examples

```
edgelet model prune
edgelet model prune dangling
edgelet model prune --mode dangling
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

* [edgelet model](edgelet_model.md)	 - Model operations


