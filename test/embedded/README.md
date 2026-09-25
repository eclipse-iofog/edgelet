# Embedded Containerd Integration Tests

End-to-end integration tests for **edgelet** with the embedded containerd engine
(`containerEngine: edgelet`). All tests run inside a Lima Linux VM on macOS.

## Directory Structure

```
test/embedded/
├── run-all.sh              # Master runner — setup → build → VM → install → test (cgroups v2)
├── run-all-cgroup-v1.sh    # Hybrid cgroup v1 suite — iofog-test-v1 + lima-ubuntu-v1.yaml
├── setup.sh                # Install macOS prerequisites via Homebrew
├── build.sh                # Embed pipeline + cross-compile unified linux/arm64 (or amd64) binary
├── vm-start.sh             # Create / start the Lima Ubuntu VM
├── vm-install.sh           # install.sh split install (edgelet-containerd + edgelet)
├── vm-test.sh              # Run all test assertions inside VM
├── vm-test-cgroup-v1.sh    # Hybrid v1 gate: host probe, status, deploy smoke
├── vm-stop.sh              # Stop (and optionally delete) the VM
├── lima-ubuntu.yaml        # Lima VM definition (Ubuntu 24.04, cgroups v2, overlayfs)
├── lima-ubuntu-v1.yaml     # Lima VM definition (hybrid cgroup v1)
└── lib/
    ├── log.sh              # Shared colour logging + assert helpers
    └── cgroup-v1-host.sh   # cgroup_v1_hybrid_host_ready (shared with vm-test-cgroup-v1.sh)
```

## Quick Start

```bash
# From the repository root:
# From the repository root:

# Full pipeline (first run ~5-10 minutes including VM boot + image pull):
./test/embedded/run-all.sh

# On subsequent runs (VM already running, binaries already built):
./test/embedded/run-all.sh --skip-setup --skip-build --skip-start
# or just rerun the test script directly:
./test/embedded/vm-test.sh

# Hybrid cgroup v1 suite (separate VM):
./test/embedded/run-all-cgroup-v1.sh
```

## Prerequisites

Installed automatically by `setup.sh`:

| Tool | Purpose |
|---|---|
| [Lima](https://github.com/lima-vm/lima) 0.14+ | Linux VM manager for macOS (vzNAT requires 0.14+; macOS 13.0+ for VZ) |
| `jq` | Host-only: Lima `limactl list --json` parsing (guest tests use `grep` / `edgelet` CLI) |
| `aarch64-unknown-linux-gnu-gcc` | CGO cross-compiler (Apple Silicon) |
| `x86_64-unknown-linux-gnu-gcc` | CGO cross-compiler (Intel Mac) |

**Networking**: The Lima config uses `vzNAT` so the VM gets a host-reachable IP without socket_vmnet or sudoers. Requires Lima 0.14+ and macOS 13.0+ when using `vmType: vz`.

## Options

```
./test/embedded/run-all.sh [options]

  --skip-setup      Skip Homebrew prerequisite installation
  --skip-build      Skip cross-compile (reuse build/edgelet-linux-*)
  --skip-start      Skip VM creation/start (VM must already be running)
  --delete-vm       Delete the VM after tests complete
  --vm-name=NAME    Lima VM name (default: edgelet-test)
  --arch=ARCH       Target Linux arch: arm64 | amd64 (default: auto-detect from host)
  --timeout=N       Seconds to wait for VM readiness (default: 300)
  --ci              CI mode — deletes VM on failure
```

## Hybrid cgroup v1 suite (`embedded-cgroup-v1`)

Separate from the default v2 embedded matrix. Uses **`iofog-test-v1`** and
`lima-ubuntu-v1.yaml` (`systemd.unified_cgroup_hierarchy=0`).

```bash
./test/embedded/run-all-cgroup-v1.sh
./test/embedded/run-all-cgroup-v1.sh --skip-setup --skip-build --skip-start
./test/embedded/vm-test-cgroup-v1.sh --vm-name=iofog-test-v1
```

| Item | v2 (`run-all.sh`) | v1 (`run-all-cgroup-v1.sh`) |
|------|-------------------|-----------------------------|
| VM | `iofog-test` | `iofog-test-v1` |
| Lima YAML | `lima-ubuntu.yaml` | `lima-ubuntu-v1.yaml` |
| Test script | `vm-test.sh` (full matrix) | `vm-test-cgroup-v1.sh` (bootstrap + deploy) |

Both embedded suites use **`install.sh` split** install via `test/lima/lib/install-split.sh`.
See [docs/edgelet/workload-continuity.md](../../docs/edgelet/workload-continuity.md).

## Data-plane drain (fat embed change)

Host checks (docs and `KillMode=process`) do not need a VM:

```bash
./test/embedded/ota-dataplane-drain.sh --docs
```

Live checks run inside the embedded Lima VM after `vm-install.sh`. They deploy a private volume workload, restart control while the ready embed hash matches (shim processes stay), then lock that volume and drain through CRI. Pass `--upgrade-bin` when a second thin binary has a different embed hash. That fat replace remounts `/run` `noexec` for the upgrade and expects the drain runtime under the data directory.

```bash
./test/embedded/ota-dataplane-drain.sh --vm-name=iofog-test
./test/embedded/ota-dataplane-drain.sh --vm-name=iofog-test --upgrade-bin=build/edgelet-linux-arm64
```

Unified orchestrator:

```bash
./test/run-all.sh --suite=embedded
./test/run-all.sh --suite=embedded-cgroup-v1
./test/run-all.sh --suite=nested-docker
```

### Nested Docker suite (`nested-docker`)

Runs on the **Mac host** with Docker (no Lima). Builds root `Dockerfile`, then deploy + engine-switch smokes. Each smoke **prunes its named Docker volumes** at start so stale lib/etc state from a prior failed run cannot flake the suite.

```bash
./test/embedded/run-all-nested-docker.sh
./test/embedded/run-all-nested-docker.sh --skip-build --image=edgelet:local
./test/run-all.sh --suite=nested-docker --skip-build
```

## Build pipeline

`build.sh` uses the two-layer embed pipeline:

1. `scripts/download`
2. `scripts/build-embedded`
3. `scripts/build-edgelet fat` — fat runtime → `build/bin/edgelet`
4. `scripts/package-data` — zstd tar with fat in `bin/edgelet`
5. `scripts/build-edgelet` — thin wrapper embeds tar

Output: **`build/edgelet-linux-<arch>`** — unified linux thin binary (CLI + embed + `daemon` dispatch). Systemd `edgelet daemon` lazy-extracts and execs the fat runtime from `/var/lib/edgelet/data/current/bin/edgelet`.

## Test Phases

| Phase | What is tested |
|---|---|
| 1 | Extracted embedded binaries (shims, crun, CNI plugins) |
| 2 | containerd socket, health check, `k8s.io` namespace |
| 3 | Managed + local CNI conflists written, network names, bridge names, system symlinks |
| 4 | EdgeletAPI v1 and CLI checks (`ms ls` table output, `auth whoami`, local `deploy -f`) |
| 5 | Container run, IP forwarding, crun version |
| 6 | CLI: `version`, `info` shows engine=edgelet |
| 7 | Chaos gates (restart storm + child crash recovery) |
| 8 | RuntimeClass dual-shim flow (Spin + Edgelet), restart convergence, availableRuntimes, runtime-pinned workloads |
| 9 | Built-in registries (ids 1–3 immutable); tiny HF pull (`hf-internal-testing/tiny-random-gpt2`); tiny OCI pull (`ai/smollm2` 135m, ~101 MiB) |
| 10 | Catalog bind (`{bindPath}/{name}/`); unknown-name apply reject; `model rm` refuse while bound; in-place add item; bindPath recreate; container fields (cpus, memory, shm, tmpfs, sysctls, ulimits, devices, runAsGroup, read-only root) |
| 11 | Persistent `VOLUME` retain: private vs shared vs BIND; restart + `pruningFrequency` + `system prune`; `ms rm` keep; in-use `volume rm` abort; explicit reclaim |
| 12 | Knowledge HF dataset pull (`hf-internal-testing/dataset_with_data_files`); catalog bind (`{bindPath}/{name}/`); `knowledge rm` refuse while bound; in-place add; bindPath recreate; unknown-name reject; both catalogs Ready; `system prune` leaves Knowledge trees |

## RuntimeClass dual-shim coverage (Lima arm64)

`vm-test.sh` validates external shim activation through RuntimeClass using these artifacts:

- Spin shim:
  - `https://github.com/spinframework/containerd-shim-spin/releases/download/v0.24.0/containerd-shim-spin-v2-linux-aarch64.tar.gz`
- Edgelet WASM shim (handler **`edgelet-wasmtime`**, `io.containerd.edgelet.v2`):
  - `https://github.com/Datasance/containerd-shim-edgelet/releases/download/v0.1.0/containerd-shim-edgelet-wasm-v2-aarch64-linux-gnu.tar.gz`

Coverage includes:

- shim binary install into embedded runtime bin directory
- RuntimeClass validate/apply via CLI/API (sync success or async accepted + poll to terminal)
- controlled containerd restart convergence after apply
- `availableRuntimes` visibility for class and class-local entries
- runtime-pinned local workloads running for each RuntimeClass
- RuntimeClass delete guard rejection while runtime-pinned workload exists
- RuntimeClass delete success after removing dependent workloads (sync success or async accepted + poll to terminal)
- runtime entries removed from effective runtime map after delete convergence

## Individual Scripts

```bash
# Just install prerequisites
./test/embedded/setup.sh

# Just build binaries (auto-detects Apple Silicon → linux/arm64)
./test/embedded/build.sh
./test/embedded/build.sh --arch=amd64

# Just start the VM
./test/embedded/vm-start.sh

# Just install edgelet in the VM
./test/embedded/vm-install.sh

# Just run tests (VM must already have edgelet installed)
./test/embedded/vm-test.sh

# Stop the VM (keep data)
./test/embedded/vm-stop.sh

# Stop and delete the VM
./test/embedded/vm-stop.sh --delete
```

## Using the Desktop App Instead

If the desktop app is running with the Lima VM already active, you can run
the test script against the same VM:

```bash
./test/embedded/vm-test.sh --vm-name=edgelet
```

## Connecting to the VM directly

```bash
# Open a shell
limactl shell edgelet-test

# View daemon logs
limactl shell edgelet-test -- sudo journalctl -fu edgelet

# Direct containerd access via ctr
limactl shell edgelet-test -- sudo ctr \
    --address /run/edgelet/containerd.sock \
    --namespace k8s.io \
    images list
```

## Naming (Edgelet greenfield)

| Item | Value |
|---|---|
| Binary | `edgelet` (single multicall) |
| systemd unit | `edgelet.service` + `edgelet-containerd.service` (split) |
| Data paths | `/var/lib/edgelet`, `/run/edgelet`, `/var/lib/edgelet-containerd` |
| Config | `/etc/edgelet/config.yaml` |
| `containerEngine` | `edgelet` |
| Edgelet API | `/v1/…` |
| Deploy manifests | `apiVersion: edgelet.iofog.org/v1` |

Pot controller REST remains **`/api/v3/…`** (unchanged).
