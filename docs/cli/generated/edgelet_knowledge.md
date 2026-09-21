## edgelet knowledge

Knowledge operations

### Synopsis

Local knowledge artifact operations.

Subcommands: pull, ls, inspect, prune, rm.

### Examples

```
edgelet knowledge pull product-docs
  edgelet knowledge pull wiki-faiss --repo acme/wiki --revision 9f3c111122223333444455556666777788889999 --registry 3 --files data/**/*.jsonl --format jsonl
  edgelet knowledge ls
  edgelet knowledge inspect product-docs
  edgelet knowledge prune
  edgelet knowledge prune dangling
  edgelet knowledge rm product-docs
```

### Options

```
  -h, --help   help for knowledge
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
* [edgelet knowledge inspect](edgelet_knowledge_inspect.md)	 - Inspect a knowledge
* [edgelet knowledge ls](edgelet_knowledge_ls.md)	 - List knowledge
* [edgelet knowledge prune](edgelet_knowledge_prune.md)	 - Prune unreferenced knowledge
* [edgelet knowledge pull](edgelet_knowledge_pull.md)	 - Pull a knowledge
* [edgelet knowledge rm](edgelet_knowledge_rm.md)	 - Remove a knowledge


