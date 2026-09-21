## edgelet knowledge pull

Pull a knowledge

### Synopsis

Download knowledge artifacts.

With only <name>, retry the existing deployed row.
With --repo and --registry, upsert the same row then pull.

```
edgelet knowledge pull <name> [flags]
```

### Examples

```
edgelet knowledge pull product-docs
edgelet knowledge pull wiki-faiss --repo acme/wiki --revision 9f3c111122223333444455556666777788889999 --registry 3 --files data/**/*.jsonl --format jsonl
```

### Options

```
      --files stringArray   HF file path or glob (repeatable)
      --format string       Format hint (markdown, pdf, jsonl, parquet, arrow, sqlite, faiss, chroma, lance, unknown)
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

* [edgelet knowledge](edgelet_knowledge.md)	 - Knowledge operations


