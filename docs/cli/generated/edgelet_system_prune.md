## edgelet system prune

Prune unused resources

### Synopsis

Prune unused resources. Default mode is dangling images. volumes does not destroy persistent VOLUME data; use edgelet volume prune.

```
edgelet system prune [dangling|containers|volumes|all] [flags]
```

### Examples

```
edgelet system prune
edgelet system prune all
edgelet system prune --mode all
```

### Options

```
  -h, --help          help for prune
  -m, --mode string   Prune mode: dangling|containers|volumes|all
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

* [edgelet system](edgelet_system.md)	 - System operations


