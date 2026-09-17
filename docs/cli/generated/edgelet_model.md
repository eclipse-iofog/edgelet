## edgelet model

Model operations

### Synopsis

Local model artifact operations.

Subcommands: pull, ls, inspect, prune, rm.

### Examples

```
edgelet model pull llama-2-7b-q2k
  edgelet model pull tiny-gpt2 --repo hf-internal-testing/tiny-random-gpt2 --registry 3 --files config.json
  edgelet model ls
  edgelet model inspect llama-2-7b-q2k
  edgelet model prune
  edgelet model prune dangling
  edgelet model rm llama-2-7b-q2k
```

### Options

```
  -h, --help   help for model
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
* [edgelet model inspect](edgelet_model_inspect.md)	 - Inspect a model
* [edgelet model ls](edgelet_model_ls.md)	 - List models
* [edgelet model prune](edgelet_model_prune.md)	 - Prune unreferenced models
* [edgelet model pull](edgelet_model_pull.md)	 - Pull a model
* [edgelet model rm](edgelet_model_rm.md)	 - Remove a model


