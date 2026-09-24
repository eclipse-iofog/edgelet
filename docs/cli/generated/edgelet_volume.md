## edgelet volume

Persistent volume operations

### Synopsis

Persistent VOLUME operations on this agent.

Private volumes live per microservice UUID. Shared volumes are node-global names
consumed by local and controller microservices. BIND host paths are never listed
or deleted here.

Subcommands: ls, rm, prune.

### Examples

```
edgelet volume ls
  edgelet volume rm <uuid>
  edgelet volume rm <uuid> <name> --force
  edgelet volume rm --shared <name>
  edgelet volume prune
  edgelet volume prune --orphans --yes
```

### Options

```
  -h, --help   help for volume
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

* [edgelet](edgelet.md)	 - Local CLI for the Edgelet daemon
* [edgelet volume ls](edgelet_volume_ls.md)	 - List persistent volumes
* [edgelet volume prune](edgelet_volume_prune.md)	 - Prune unreferenced persistent volumes
* [edgelet volume rm](edgelet_volume_rm.md)	 - Remove a persistent volume


