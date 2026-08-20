# Edgelet

[![CI](https://github.com/eclipse-iofog/edgelet/actions/workflows/ci.yml/badge.svg)](https://github.com/eclipse-iofog/edgelet/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/eclipse-iofog/edgelet?include_prereleases)](https://github.com/eclipse-iofog/edgelet/releases)
[![Go](https://img.shields.io/badge/Go-1.26.6-blue.svg)](https://go.dev/)
[![License](https://img.shields.io/badge/License-EPL--2.0-blue.svg)](LICENSE)
[![Binary size](https://img.shields.io/badge/linux%20thin-~35%20MiB-informational)](https://github.com/eclipse-iofog/edgelet/releases)
[![govulncheck](https://github.com/eclipse-iofog/edgelet/actions/workflows/govulncheck.yml/badge.svg)](https://github.com/eclipse-iofog/edgelet/actions/workflows/govulncheck.yml)
[![OpenSSF Scorecard](https://api.securityscorecards.dev/projects/github.com/eclipse-iofog/edgelet/badge)](https://securityscorecards.dev/viewer/?uri=github.com/eclipse-iofog/edgelet)
[![OpenSSF Best Practices](https://www.bestpractices.dev/projects/13141/badge)](https://www.bestpractices.dev/en/projects/13141)
[![codecov](https://img.shields.io/badge/codecov-post--beta-lightgrey)](https://codecov.io/gh/eclipse-iofog/edgelet)

[![Linux amd64](https://img.shields.io/badge/linux--amd64-supported-2ea44f?style=flat&logo=linux&logoColor=white)](https://github.com/eclipse-iofog/edgelet/releases)
[![Linux arm64](https://img.shields.io/badge/linux--arm64-supported-2ea44f?style=flat&logo=linux&logoColor=white)](https://github.com/eclipse-iofog/edgelet/releases)
[![Linux armv7](https://img.shields.io/badge/linux--armv7-supported-2ea44f?style=flat&logo=linux&logoColor=white)](https://github.com/eclipse-iofog/edgelet/releases)
[![Linux riscv64](https://img.shields.io/badge/linux--riscv64-supported-2ea44f?style=flat&logo=linux&logoColor=white)](https://github.com/eclipse-iofog/edgelet/releases)
[![Container (Eclipse)](https://img.shields.io/badge/ghcr.io-eclipse--iofog%2Fedgelet-2496ED?style=flat&logo=docker&logoColor=white)](https://ghcr.io/eclipse-iofog/edgelet)
[![Container (Datasance)](https://img.shields.io/badge/ghcr.io-datasance%2Fedgelet-2496ED?style=flat&logo=docker&logoColor=white)](https://ghcr.io/datasance/edgelet)

[![macOS arm64](https://img.shields.io/badge/macos--arm64-supported-2ea44f?style=flat&logo=apple&logoColor=white)](https://github.com/eclipse-iofog/edgelet/releases)
[![macOS amd64](https://img.shields.io/badge/macos--amd64-supported-2ea44f?style=flat&logo=apple&logoColor=white)](https://github.com/eclipse-iofog/edgelet/releases)
[![Windows amd64](https://img.shields.io/badge/windows--amd64-supported-2ea44f?style=flat&logo=windows&logoColor=white)](https://github.com/eclipse-iofog/edgelet/releases)

**Upstream:** [eclipse-iofog/edgelet](https://github.com/eclipse-iofog/edgelet) · **Datasance distribution:** [Datasance/edgelet](https://github.com/Datasance/edgelet)

**Lightweight container runtime for far-edge devices and ioFog/PoT node agent.**

Edgelet is a single `edgelet` binary per platform. On Linux it ships a thin CLI wrapper with an embedded container runtime. On macOS and Windows it runs as a monolithic binary against an external Docker or Podman engine. The daemon manages microservice workloads, syncs with the Controller, and exposes the on-device **EdgeletAPI** for local administration.

**Documentation:** [docs/edgelet/README.md](docs/edgelet/README.md)

## Platforms

| Platform | Binary model | Container engine | Role in beta |
|----------|--------------|------------------|--------------|
| **Linux** (amd64, arm64, arm, riscv64) | Thin wrapper (~31 MiB) + embedded fat runtime | `edgelet` (default), `docker`, `podman` | Production far-edge / device-edge node agent |
| **macOS** (amd64, arm64) | Monolithic ELF | `docker`, `podman` only — Docker Desktop, Podman Machine, or OrbStack | Supported **development platform** (not a production edge node OS) |
| **Windows** (amd64) | Monolithic `.exe` | `docker`, `podman` only | **Tier 2** — CLI and cross-compile verified; no Windows integration-test matrix |

Linux thin binary installs to `/usr/local/bin/edgelet`, lazy-extracts the fat runtime to `/var/lib/edgelet/data/current/`, and starts the daemon with `edgelet daemon` or `systemctl start edgelet`. Desktop platforms do not embed a runtime; point `containerEngineUrl` at your local engine socket.

## macOS development

Use an external engine and the desktop monolithic build:

```bash
make install-dev start-dev
export SNAP_COMMON=$(pwd)/dev
edgelet system status
edgelet system info
```

Stop and inspect logs:

```bash
make stop-dev
tail -f dev/var/log/edgelet/daemon-startup.log
```

CLI reference: [docs/cli/README.md](docs/cli/README.md) · [output schemas](docs/cli/output-schemas.md) · [CLI migration from legacy ioFog Agent](docs/edgelet/migration-from-iofog-agent-cli.md)

## Project structure

```
.
├── cmd/edgelet/             # Multicall entry (CLI, daemon, containerd child)
├── internal/
│   ├── auth/                # TLS / JWT / EdgeletAPI PKI
│   ├── edgeletapi/          # EdgeletAPI HTTP/WebSocket (:54321)
│   ├── fieldagent/          # Controller communication
│   ├── processmanager/      # Container reconciliation
│   ├── store/               # SQLite persistence
│   └── supervisor/          # Module orchestration
├── pkg/
│   ├── containerd/          # In-process containerd (edgelet engine)
│   └── engine/              # ContainerEngine + docker/podman/edgelet adapters
├── install.sh               # Binary installer (multi-OS)
├── uninstall.sh             # Clean uninstaller
├── packaging/               # systemd unit, config templates
└── docs/edgelet/            # Operator documentation
```

## Prerequisites

| Tool | Version | Notes |
|------|---------|-------|
| Go | **1.26.6+** | See `go.mod` |
| Make | any | GNU Make |
| Docker | 26.10+ | Required for macOS release builds and embed CI |
| Docker / Podman | 26.10+ | When `containerEngine` is `docker` or `podman` |
| golangci-lint | v2.12.2 | Auto-installed by `make lint` |

On **Linux** native embed builds, install cross-compilers:

```bash
sudo apt-get install -y \
  gcc gcc-aarch64-linux-gnu gcc-arm-linux-gnueabihf gcc-riscv64-linux-gnu \
  musl-tools musl-dev
```

## Container engine

Runtime selection via `containerEngine` in config (validated per GOOS):

| Platform | Allowed `containerEngine` | Default |
|----------|---------------------------|---------|
| **linux** | `edgelet`, `docker`, `podman` | **`edgelet`** |
| **darwin / windows** | `docker`, `podman` | `docker` |

```yaml
profiles:
  production:
    containerEngine: edgelet
    containerEngineUrl: unix:///run/edgelet/containerd.sock
    pruningFrequency: 24
    watchdogEnabled: true
```

Details: [docs/edgelet/container-engine.md](docs/edgelet/container-engine.md)

## Building

**macOS (release matrix):** use the Docker embed loop — do **not** use `make build-all-archs` (requires native Linux cross-toolchains):

```bash
./test/release/build-all.sh    # all 7 release binaries
make build-release-matrix      # alias for build-all.sh
```

After changing embed build dependencies, refresh CI images:

```bash
RELEASE_FRESH_CI_IMAGE=1 ./test/release/build-all.sh
```

**Linux:** `./test/release/build-all.sh` or `make build-all-archs`, plus desktop targets:

```bash
make build-desktop-darwin build-desktop-windows
scripts/release-binaries.sh v1.0.2
```

Local / single-target builds:

```bash
make build                    # host OS: linux thin or desktop monolithic
make build-edgelet-linux      # unified linux thin (ARCH=amd64 default)
make deps                     # embed pipeline before linux thin build
make build-linux-amd64        # deps + thin for amd64
make build-linux-arm64        # deps + thin for arm64
make build-desktop-darwin     # darwin monolithic
make release-binaries VERSION=v1.0.2
```

## Testing

```bash
make test
make test-unit
make test-coverage
```

Embedded-engine integration (Lima VM on macOS): [test/embedded/README.md](test/embedded/README.md)

Quality gates before contributing:

```bash
make lint
make security-code
make vulncheck
make fmt
```

## Installation

Binary-only releases (no DEB/RPM, no release `.tar.gz` bundles). Default Linux engine: **edgelet**.

| Channel | GitHub repo | Container image |
|---------|-------------|-----------------|
| **Eclipse (canonical)** | [eclipse-iofog/edgelet](https://github.com/eclipse-iofog/edgelet) | `ghcr.io/eclipse-iofog/edgelet:<tag>` |
| **Datasance mirror** | [Datasance/edgelet](https://github.com/Datasance/edgelet) | `ghcr.io/datasance/edgelet:<tag>` |

Identical builds and tags; choose the channel that matches your fleet docs. Override install source at runtime with `EDGELET_GITHUB_REPO`.

### Eclipse (canonical)

```bash
curl -fsSL https://github.com/eclipse-iofog/edgelet/releases/download/v1.0.2/install.sh -o install.sh
chmod +x install.sh
sudo ./install.sh --version=v1.0.2
# dev / CI: sudo ./install.sh --bin-path=build/edgelet-linux-amd64 --version=dev
```

### Datasance mirror

```bash
curl -fsSL https://github.com/Datasance/edgelet/releases/download/v1.0.2/install.sh -o install.sh
chmod +x install.sh
sudo ./install.sh --version=v1.0.2
```

Release artifacts per tag: seven binaries (`edgelet-linux-<arch>`, `edgelet-darwin-<arch>`, `edgelet-windows-amd64.exe`), `SHA256SUMS`, `install.sh`, `uninstall.sh`, and config/CA samples.

### Install paths

| Purpose | Linux | macOS | Windows |
|---------|-------|-------|---------|
| Binary | `/usr/local/bin/edgelet` | `/usr/local/bin/edgelet` | `%ProgramFiles%\Edgelet\edgelet.exe` |
| Config | `/etc/edgelet/config.yaml` | `/etc/edgelet/config.yaml` | `%ProgramData%\Edgelet\config\config.yaml` |
| Data | `/var/lib/edgelet/` | `/var/lib/edgelet/` | `%ProgramData%\Edgelet\data\` |
| Runtime | `/run/edgelet/` | `/var/run/edgelet/` | `%ProgramData%\Edgelet\run\` |
| Logs | `/var/log/edgelet/` | `/var/log/edgelet/` | `%ProgramData%\Edgelet\log\` |
| Scripts | `/usr/share/edgelet/` | `/usr/local/share/edgelet/` | `%ProgramData%\Edgelet\scripts\` |

```bash
sudo edgelet init-config                     # default config if missing
edgelet daemon                               # foreground
systemctl start edgelet                      # production (linux)
edgelet config --a <iofog/pot controller-api-endpoint> # configure which controller edgelet is going to connect (post-install)
edgelet provision <key>                      # register with Controller (post-install)
```

Installation: [docs/edgelet/installation.md](docs/edgelet/installation.md) · Deployment topology: [docs/edgelet/deployment.md](docs/edgelet/deployment.md)

Local IT container tag: `edgelet:local` (`EDGELET_IMAGE=edgelet:local`).

### Uninstall

```bash
sudo sh uninstall.sh
sudo sh uninstall.sh --remove-data
```

## EdgeletAPI

On-device operator API (daemon↔CLI):

| Item | Value |
|------|--------|
| HTTPS | `https://127.0.0.1:54321` |
| Routes | `/v1/...` |
| CLI token | `/etc/edgelet/edgelet-api` |
| TLS CA | `/etc/edgelet/edgeletapi-ca.crt` |

Guide: [docs/edgelet/edgelet-api-v1.md](docs/edgelet/edgelet-api-v1.md) · OpenAPI: [docs/edgelet/edgelet-api-v1-openapi.yaml](docs/edgelet/edgelet-api-v1-openapi.yaml)

Controller REST (field agent) uses `/api/v3/...` on the controller URL — separate from EdgeletAPI.

## CI

| Workflow | Purpose |
|----------|---------|
| [`.github/workflows/ci.yml`](.github/workflows/ci.yml) | Build, lint, unit tests |
| [`.github/workflows/govulncheck.yml`](.github/workflows/govulncheck.yml) | Dependency vulnerability scan |
| [`.github/workflows/release.yml`](.github/workflows/release.yml) | Release matrix on tag push |

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md).

## License

[EPL-2.0](LICENSE)
