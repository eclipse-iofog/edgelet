# Packaging Structure — Edgelet

## Overview

Edgelet uses **binary-only** GitHub Releases (Plan 8). **Linux** ships a thin download ELF with embedded zstd fat runtime; **darwin/windows** ship a monolithic binary.

| Platform | Release artifact | `containerEngine` | Notes |
|----------|------------------|-------------------|--------|
| **linux** | `edgelet-linux-<arch>` | `edgelet` (default), `docker`, `podman` | Thin OTA binary + lazy fat extract |
| **darwin** | `edgelet-darwin-<arch>` | `docker`, `podman` | Monolithic; no embed |
| **windows** | `edgelet-windows-amd64.exe` | `docker`, `podman` | Monolithic; no embed |

No DEB/RPM. Install via **`install.sh`**; remove via **`uninstall.sh`**.

---

## Distribution artifacts

### GitHub Release (per tag)

| File | Purpose |
|------|---------|
| `edgelet-linux-{amd64,arm64,arm,riscv64}` | Linux thin binaries |
| `edgelet-darwin-{amd64,arm64}` | macOS binaries |
| `edgelet-windows-amd64.exe` | Windows binary |
| `SHA256SUMS` | Checksums for all of the above + samples + SBOMs |
| `LICENSE-3RD-PARTY.md` | Third-party license texts |
| `edgelet-<os>-<arch>[.exe].cdx.json` | CycloneDX SBOM for that binary |
| `edgelet-<os>-<arch>[.exe].spdx.json` | SPDX SBOM for that binary |
| `edgelet-config.yaml.sample` | Default config template |
| `edgelet-controller-ca.crt.sample` | Lab bootstrap CA |
| `install.sh` / `uninstall.sh` | **Self-contained** on-node lifecycle monoliths (curl-safe; no sibling helper tree) |

**Not published:** `.tar.gz` bundles, `install-minimal.sh`, DEB/RPM.

Build:

```bash
make build-all-archs
make build-desktop-darwin build-desktop-windows
make release-binaries VERSION=v1.0.0   # → dist/
```

Script: `scripts/release-binaries.sh` (replaces `release-tarballs.sh`).

---

## Install paths

| Item | Linux | macOS | Windows |
|------|-------|-------|---------|
| Binary | `/usr/local/bin/edgelet` | `/usr/local/bin/edgelet` | `Program Files\Edgelet\edgelet.exe` |
| Config | `/etc/edgelet/config.yaml` | `/etc/edgelet/config.yaml` | `%ProgramData%\Edgelet\config.yaml` |
| OTA | `/var/backups/edgelet/` | — | — |
| Scripts + samples | `/usr/share/edgelet/install.sh`, `uninstall.sh`, `edgelet-config.yaml.sample`, `edgelet-controller-ca.crt.sample` | optional | optional |

---

## install.sh

| Flag | Meaning |
|------|---------|
| `--version=` | Release tag for download |
| `--arch=` | Override detected arch |
| `--bin-path=` | Local binary (dev / airgap) |
| `--airgap` | Offline; requires `--bin-path` |
| `--expected-sha256=` | Verify staged binary |
| `--upgrade` / `--rollback` | Thin-binary OTA |
| `--force-config` | Replace config from sample |
| `--with-sample-ca` | Install sample CA if missing |
| `--container-engine=` | `edgelet`, `docker`, `podman` |

**Removed:** `--flavor`, `--provision-key`, `--non-interactive`, `--tarball-path`.

Provision after install: `edgelet provision <key>` (potctl SSH).

---

## Config templates (repo)

| File | Use |
|------|-----|
| [config.default.yaml](edgelet/etc/edgelet/config.default.yaml) | Release default → `edgelet-config.yaml.sample` |
| [controller-ca.sample.crt](edgelet/etc/edgelet/controller-ca.sample.crt) | Lab CA sample |
| [config_new.yaml](edgelet/etc/edgelet/config_new.yaml) | **Dev / embedded IT only** (multi-profile) |

**Deleted:** `config_full.yaml`, `config_lite.yaml`.

CLI: `edgelet init-config` writes default config if missing (no overwrite).

---

## Init templates (linux)

**Canonical tree:** `packaging/init/` only (Plan 10). Legacy `packaging/systemd/` removed; CI: `scripts/check-init-packaging.sh`.

Under `packaging/init/`:

| Init | Template |
|------|----------|
| systemd | `systemd/edgelet.service`, `edgelet-containerd.service` (stub), `edgelet.service.d/{docker,podman}.conf` |
| openrc | `openrc/edgelet.init`, `openrc/edgelet-containerd.init` (stub) |
| procd | `procd/edgelet` (OpenWrt `USE_PROCD=1`) |
| sysvinit | `sysvinit/edgelet.init` |
| upstart | `upstart/edgelet.conf` |
| s6 | `s6/run`, `s6/finish` |
| runit | `runit/run`, `runit/finish` |

**Install-time delivery:** `install.sh` embeds init templates and writes units to the detected init system. Shutdown helper → `/usr/libexec/edgelet/edgelet-shutdown`. Authoring inputs: `scripts/lib/*.sh`, `packaging/init/**`, `scripts/edgelet-shutdown` (assembled into monolithic `install.sh`; **not** copied to `/usr/share/edgelet/`). CLI: `edgelet shutdown`, `edgelet cgroup-preflight`.

Operator matrix: `docs/edgelet/init-systems.md` (Plan 10). IT: `test/init/README.md`.

---

## Container image

| Item | Value |
|------|--------|
| Image | `ghcr.io/eclipse-iofog/edgelet:<tag>` |
| Datasance mirror | `ghcr.io/datasance/edgelet:<tag>` |
| Dockerfile | `Dockerfile` |
| Entry | `edgelet daemon` with `EDGELET_DAEMON=container` |

---

## CI

| Target | Purpose |
|--------|---------|
| `scripts/ci` | Linux embed → build → test → size gate (≤55 MB thin) |
| `.github/workflows/ci.yml` | Unit tests + embed job |
| `test/install/*.sh` | Install / OTA script smoke (linux root) |
| `test/embedded/run-all.sh` | Lima VM embedded engine IT |
