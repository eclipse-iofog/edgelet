## edgelet volume prune

Prune unreferenced persistent volumes

### Synopsis

List or destroy unreferenced persistent VOLUME claims.

Default is a dry-run of orphan candidates. --yes is required to destroy.
Skip claims younger than 24h unless --force. Control-plane volumes are never pruned.

```
edgelet volume prune [flags]
```

### Examples

```
edgelet volume prune
edgelet volume prune --orphans --yes
edgelet volume prune --orphans --yes --force
```

### Options

```
      --force     Bypass the 24h grace window when unmounted
  -h, --help      help for prune
      --orphans   Select unreferenced persistent volumes (default for this command)
      --yes       Destroy listed orphans; without this flag prune is a dry-run
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

* [edgelet volume](edgelet_volume.md)	 - Persistent volume operations


