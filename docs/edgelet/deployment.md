# Edgelet deployment

Production deployment topology for Edgelet on **linux**, **darwin**, and **windows**.

**Installation, upgrade, rollback, and OTA readiness:** see **[installation.md](installation.md)**.

## Install script distribution

`install.sh` and `uninstall.sh` are **self-contained monoliths**. Online install, airgap staging, and GitHub Releases need only those scripts plus the platform binary (or `install.sh --bin-path=…`). No co-located helper directories (`scripts/lib/`, `packaging/init/`, or similar) are required beside the scripts.

After install on **linux**, `/usr/share/edgelet/` holds:

| Path | Role |
|------|------|
| `install.sh`, `uninstall.sh` | OTA lifecycle copies (same self-contained monoliths) |
| `edgelet-config.yaml.sample` | Default config template |
| `edgelet-controller-ca.crt.sample` | Lab bootstrap CA sample |

Init units and `edgelet-shutdown` are written directly to system paths (`/etc/systemd/system/`, `/usr/libexec/edgelet/`, etc.) at install time — not stored under `/usr/share/edgelet/lib/` or `/usr/share/edgelet/init/`.

## Product model

| Platform | Binary | Embedded engine | `containerEngine` | Default engine |
|----------|--------|-----------------|-------------------|----------------|
| **linux** | Thin wrapper (~30 MB) + fat in zstd embed | Yes | `edgelet`, `docker`, `podman` | `edgelet` |
| **darwin** | Monolithic | No | `docker`, `podman` | `docker` |
| **windows** | Monolithic `.exe` | No | `docker`, `podman` | `docker` |

Linux thin path: `/usr/local/bin/edgelet` → lazy extract → `/var/lib/edgelet/data/current/bin/edgelet` (fat).

## Prerequisites

| Platform | Requirements |
|----------|----------------|
| **linux** | Root/sudo; Ubuntu 20.04+, RHEL 8+, Debian 11+ (or equivalent); network to Controller for online install |
| **darwin** | Admin for `/usr/local/bin`; Docker or Podman for `containerEngine` |
| **windows** | Admin; Docker or Podman |
| **edgelet engine (linux)** | No external container runtime |
| **docker / podman** | Docker 26.10+ or Podman 3.0+ on host |

## potctl / iofogctl contract

Orchestrators must **not** pass provision keys to `install.sh`.

1. SSH to the edge host.
2. **Online:** run `install.sh` on the remote (see [installation.md](installation.md)).
3. **Airgap:** verify `SHA256SUMS` on the orchestrator → SCP binary → `install.sh --airgap --bin-path=…`.
4. **Provision:** SSH `edgelet provision <key>` (and `edgelet config …` as needed).

When deploying a **local ControlPlane** controller on a system fog, create that fog on Controller with **`isSystem: true`** so Edgelet can register the controller microservice after provision.

## Daemon and systemd

Production linux uses the **thin** binary as the service entry point:

```bash
edgelet daemon          # extract fat bundle if needed → exec runtime
systemctl start edgelet # ExecStart=/usr/local/bin/edgelet daemon
```

Unit templates: `packaging/init/systemd/edgelet.service`. Init matrix: [init-systems.md](init-systems.md).

For **docker** or **podman**, systemd installs a drop-in `After=docker.service` (or podman). The daemon retries socket connection with backoff before staying in WARNING.

### Embedded runtime layout (`containerEngine: edgelet`)

| Path | Role |
|------|------|
| `/var/lib/edgelet/data/<hash>/` | Content-addressed fat bundle extract |
| `data/current` | Symlink to active hash |
| `data/previous` | Last `current` before rotation (manual / coordinated rollback). Older hash directories are pruned after a successful extract |

Operator CLI (`edgelet ms`, `edgelet deploy`, …) runs in the **thin** process and does not trigger extract.

Thin-binary and fat-bundle OTA: [installation.md](installation.md#layer-2--fat-embed-daemon). Workload impact: [workload-continuity.md](workload-continuity.md).

## Runtime engine selection

```yaml
# /etc/edgelet/config.yaml
currentProfile: production
profiles:
  production:
    containerEngine: edgelet   # linux default
    containerEngineUrl: unix:///run/edgelet/containerd.sock
    pruningFrequency: 24       # hours between image + unused-local-model prune cycles (not persistent VOLUME data)
    watchdogEnabled: true      # orphan container cleanup; also disables local models
    arch: auto
    upgradeScanFrequency: 24   # hours between OTA readiness scans
```

| `containerEngine` | Linux extract? | Host requirement |
|-------------------|----------------|------------------|
| **edgelet** | Yes (first daemon start) | None |
| `docker` | No | Docker socket |
| `podman` | No | Podman socket |

Verify:

```bash
edgelet --version
edgelet system version -o json | jq '{embeddedEngine, allowedEngines, containerEngine}'
```

Engine details: [container-engine.md](container-engine.md).

## EdgeletAPI PKI

Daemon creates `/etc/edgelet/edgelet-api` and TLS material on first start. CLI uses bearer token + `edgeletapi-ca.crt`.

## Provisioning

```bash
edgelet provision <provisioning-key>
edgelet system status
edgelet deprovision
```

## Container image (linux)

| Item | Value |
|------|--------|
| Image | `ghcr.io/eclipse-iofog/edgelet:<tag>` (multi-arch manifest) |
| Datasance mirror | `ghcr.io/datasance/edgelet:<tag>` (same manifest, different registry) |
| Dockerfile | Root `Dockerfile` (scratch + CA certs layer) |
| Entrypoint | `edgelet daemon` |
| Env | `EDGELET_DAEMON=container` |
| Volumes | `/var/lib/edgelet`, `/etc/edgelet` (optional override) |
| OTA | Orchestrator replaces image tag (not `install.sh`) |

Local IT tag: `edgelet:local` (`EDGELET_IMAGE=edgelet:local` in nested-docker smokes).

## FogType (`provision.type`)

| ID | Architecture |
|----|--------------|
| 1 | amd64 |
| 2 | arm64 |
| 3 | riscv64 |
| 4 | arm (32-bit) |

Config `arch` values: `auto`, `amd64`, `arm64`, `arm`, `riscv64`.

## See also

- [installation.md](installation.md) — install.sh, OTA, readiness, upgrade/rollback
- [architecture.md](architecture.md) — thin/fat model
- [container-engine.md](container-engine.md) — engine matrix
- [troubleshooting.md](troubleshooting.md) — service teardown
- [packaging/PACKAGING-STRUCTURE.md](../../packaging/PACKAGING-STRUCTURE.md)
