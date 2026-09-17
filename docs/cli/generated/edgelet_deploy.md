## edgelet deploy

Deploy a local manifest

### Synopsis

Apply or validate a local manifest via EdgeletAPI v1.

Supported kinds: Microservice, Registry, Model, RuntimeClass, and ControlPlane (singleton controller per node).
Manifest kind is auto-detected from the YAML file.

```
edgelet deploy [flags]
```

### Examples

```
edgelet deploy -f microservice.yaml
  edgelet deploy -f microservice.yaml --dry-run
  edgelet deploy -f microservice.yaml --sourceName my-app
  edgelet deploy -f registry.yaml
  edgelet deploy -f model.yaml
  edgelet deploy -f controlplane.yaml
  edgelet deploy -f controlplane.yaml --dry-run
  edgelet -o json deploy -f microservice.yaml --dry-run
```

### Options

```
      --dry-run             Validate manifest without applying
  -f, --file string         Path to manifest YAML
  -h, --help                help for deploy
      --sourceName string   Optional source name for microservice deploy
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


