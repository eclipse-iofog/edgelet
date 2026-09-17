## edgelet registry

Registry operations

### Synopsis

Manage local registry credentials used for image and model pulls.

Built-in rows (cannot be edited or removed): id 1 docker.io (oci),
id 2 from_cache (oci), id 3 https://huggingface.co (hf).

Subcommands: ls, inspect, rm.

### Examples

```
edgelet registry ls -o json
  edgelet registry inspect <id>
  edgelet registry inspect <id> --password-plain
  edgelet registry rm <id>
```

### Options

```
  -h, --help   help for registry
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
* [edgelet registry inspect](edgelet_registry_inspect.md)	 - Inspect a registry
* [edgelet registry ls](edgelet_registry_ls.md)	 - List registries
* [edgelet registry rm](edgelet_registry_rm.md)	 - Remove a registry


