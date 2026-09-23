## edgelet runtime drain

Drain labeled microservice containers before data-plane stop

### Synopsis

Stops labeled microservice containers while the CRI socket is still up.

Default path uses the control-plane API. --direct quiesces through the embedded
runtime without the control-plane API, then force-kills leftovers and volume
holders and verifies before success.

Default timeout follows shutdownGracePeriodSeconds (90s). Exit 0 when drain completes;
exit 1 on timeout or verify failure.

```
edgelet runtime drain [flags]
```

### Options

```
      --direct        Drain through the embedded runtime without the control-plane API
  -h, --help          help for drain
      --timeout int   Drain budget in seconds (0 = server default) (default 90)
```

### Options inherited from parent commands

```
      --debug           Debug logging
      --no-color        Disable color and interactive UX
  -o, --output string   Output format: human, json, yaml (default "human")
      --quiet           Suppress interactive progress output
      --socket string   Edgelet API unix socket path
      --verbose         Verbose logging
```

### SEE ALSO

* [edgelet runtime](edgelet_runtime.md)	 - Embedded runtime data-plane operations


