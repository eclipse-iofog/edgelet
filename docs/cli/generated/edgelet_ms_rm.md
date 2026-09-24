## edgelet ms rm

Remove a microservice

### Synopsis

Remove a microservice and its local deployment state.

Persistent VOLUME directories are retained. Shared claims drop this consumer only.
--cleanup records a reserved bit for a later explicit volume prune; it does not
delete data now and does not start a sweeper.

WARNING: This deletes the microservice record and associated container resources.

```
edgelet ms rm <id> [flags]
```

### Options

```
      --cleanup   Reserve a cleanup bit for later orphan prune; does not delete persistent VOLUME data now
  -h, --help      help for rm
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

* [edgelet ms](edgelet_ms.md)	 - Microservice operations


