## edgelet deprovision

Deprovision the agent

### Synopsis

Remove agent provisioning and begin cleanup of managed resources.

WARNING: Deprovisioning stops controller management and may remove managed microservices.
Use --keep-local or --scope local to preserve locally deployed microservices.
Persistent VOLUME data under volumes/data and volumes/shared is preserved unless
--purge-volumes is set. Control-plane volumes are never purged.

```
edgelet deprovision [flags]
```

### Examples

```
edgelet deprovision
  edgelet deprovision --scope local
  edgelet deprovision --keep-local
  edgelet deprovision --scope all --purge-volumes
```

### Options

```
  -h, --help            help for deprovision
      --keep-local      Preserve local microservices (sets scope to local)
      --purge-volumes   Destroy workload persistent VOLUME data after deprovision
      --scope string    Deprovision scope: all or local (default "all")
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


