## edgelet volume rm

Remove a persistent volume

### Synopsis

Remove persistent VOLUME data.

volume rm <uuid> [<name>] destroys a private claim under volumes/data.
volume rm --shared <name> destroys a shared claim under volumes/shared.

Refuses if the claim is still desired or mounted. --force bypasses the desired-state
gate only when nothing is mounted.

```
edgelet volume rm [<uuid> [<name>]] [flags]
```

### Options

```
      --force           Bypass the desired-state gate when unmounted
  -h, --help            help for rm
      --shared string   Destroy a shared volume by name
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


