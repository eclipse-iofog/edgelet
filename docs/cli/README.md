# edgelet CLI reference

Operator-facing CLI for the Cobra-based `edgelet` command tree.

| Resource | Path |
|----------|------|
| Edgelet docs index | [../edgelet/README.md](../edgelet/README.md) |
| Migration from legacy CLI | [../edgelet/migration-from-iofog-agent-cli.md](../edgelet/migration-from-iofog-agent-cli.md) |
| JSON/YAML output shapes | [output-schemas.md](output-schemas.md) |
| Generated per-command pages | [generated/](generated/) (`make cli-docs`) |

## Quick start

```bash
# Runtime health (requires running edgelet daemon)
edgelet system status

# Structured output for automation
edgelet system status -o json | jq .
edgelet ms ls -o json | jq '.items[] | {uuid, name, state}'

# Apply a manifest (auto kind-detect)
edgelet deploy -f microservice.yaml

# Validate only
edgelet deploy -f microservice.yaml --dry-run
```

## Global flags

| Flag | Description |
|------|-------------|
| `-o`, `--output` | `human` (default), `json`, or `yaml` |
| `--quiet` | Suppress progress/spinner output on stderr |
| `--verbose` | Verbose logging |
| `--debug` | Debug logging |
| `--socket` | EdgeletAPI unix socket override |
| `--timeout` | Request timeout |
| `--no-color` | Disable color and interactive UX |
| `--version` | Print combined CLI + daemon version |

Environment variables respected by progress UX:

| Variable | Effect |
|----------|--------|
| `NO_COLOR=1` | Non-interactive output, no ANSI escapes |
| `CI=true` | Non-interactive progress even on a TTY |
| `TERM=dumb` | Non-interactive output |

Human-mode `deploy -f` and `config` show a spinner on stderr while the operation runs, then print a green success line on stderr (red on partial config rejection). Structured `-o json` / `-o yaml` output remains on stdout only.

Partial config rejection exits **2** with accepted/rejected detail on stderr.

## Exit codes

| Code | Meaning |
|------|---------|
| 0 | Success |
| 1 | Internal error |
| 2 | Invalid argument |
| 3 | Unauthorized / forbidden |
| 4 | Not found |
| 5 | Conflict |
| 6 | Not implemented |
| 10 | Daemon unavailable (`DAEMON_UNAVAILABLE`) |

When the daemon is not running, commands that require EdgeletAPI exit **10** with guidance to start `edgelet daemon` or `systemctl start edgelet`.

Remote command exit codes from `ms exec` propagate directly (e.g. container exit 42 → CLI exit 42).

## Command groups

| Group | Commands |
|-------|----------|
| `system` | `status`, `info`, `version`, `reload`, `stop`, `logs`, `prune` |
| `provision` / `deprovision` | top-level |
| `config` | `--long-flag` / `--alias` flags, `config cert`, `config switch` |
| `ms` | `ls`, `inspect`, `logs`, `exec`, `start`, `stop`, `restart`, `kill`, `rm` |
| `deploy` | `-f FILE [--dry-run] [--sourceName]` |
| `registry` / `runtimeclass` / `image` | `ls`, `inspect`, `rm` (+ image `pull`, `load`, `prune`) |
| `model` | `pull`, `ls`, `inspect`, `prune`, `rm` |
| `volume` | `ls`, `rm`, `prune` (private UUID or `--shared`; dry-run prune by default) |
| `auth` | `whoami`, `tokens`, `revoke` |

## Examples

```bash
edgelet system info
edgelet config --network-interface eth0
edgelet config cert <base64-encoded-cert-string>
edgelet config switch prod

edgelet ms ls -o json
edgelet ms logs <uuid> --follow
edgelet ms exec <uuid> -- /bin/sh

edgelet registry ls -o json
edgelet image pull docker.io/library/alpine:3.19
edgelet model ls
edgelet model pull llama-2-7b-q2k
edgelet model pull tiny-gpt2 --repo hf-internal-testing/tiny-random-gpt2 --registry 3 --files config.json

edgelet volume ls
edgelet volume prune
edgelet volume rm --shared nodered-config

edgelet auth whoami -o json
```

## Shell completion

```bash
edgelet completion bash | sudo tee /etc/bash_completion.d/edgelet
edgelet completion zsh > "${fpath[1]}/_edgelet"
edgelet completion fish > ~/.config/fish/completions/edgelet.fish
```

Regenerate packaged bash completion with `make cli-completion`.

## Documentation maintenance

```bash
make cli-docs
make cli-docs-check
make cli-help-check
```

Hidden generator: `edgelet documentation generate md|man --output DIR`
