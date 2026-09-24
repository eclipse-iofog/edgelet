## edgelet model pull

Pull a model

### Synopsis

Download model artifacts.

With only <name>, retry the existing deployed row.
With --repo and --registry, upsert the same row then pull.

```
edgelet model pull <name> [flags]
```

### Examples

```
edgelet model pull llama-2-7b-q2k
edgelet model pull tiny-gpt2 --repo hf-internal-testing/tiny-random-gpt2 --revision 71034c5d8bde858ff824298bdedc65515b97d2b9 --registry 3 --files config.json --format unknown
```

### Options

```
      --files stringArray   HF file path or glob (repeatable)
      --format string       Format hint (gguf, safetensors, onnx, pytorch, tensorrt, unknown)
  -h, --help                help for pull
  -r, --registry int        Registry id
      --repo string         Repository path without host
      --revision string     Tag, digest, branch, or commit
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


