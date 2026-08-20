# Changelog

All notable changes to Edgelet are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [1.0.3] - August 2026

### Changed

- **Toolchain:** Go **1.26.6** in `go.mod`, CI workflows, Dockerfiles, and quality scripts.
- **Security / vulncheck:** stdlib fixes for **GO-2026-5026**, **GO-2026-5972**, **GO-2026-6089**, **GO-2026-6090**, and **GO-2026-6218** (`net/url`, `crypto/tls`, `net/http`, `encoding/asn1`); bump `github.com/cilium/ebpf` to **v0.22.0** (**GO-2026-6238**, BTF parsing on linux cgroup setup); `make vulncheck` passes with zero documented exceptions ([SECURITY.md](SECURITY.md)).
- **Go dependencies:** minor bumps — `klauspost/compress` 1.19.2, `gopsutil/v4` 4.26.7, `google.golang.org/grpc` 1.83.0, `modernc.org/sqlite` 1.56.0.

## [1.0.2] - July 2026

### Added

- **Controller structured errors:** Field Agent parses top-level `error`, `code`, `message`, and `retryable` from Controller agent routes (`/api/v3/agent/*`) and readiness `/status` responses.
- **Status auth deprovision gate:** `PostStatusHelper` defers auto-deprovision until **5 consecutive** status auth failures over at least **60 seconds**; streak resets on successful status POST or `Provision()`.
- **OTA / provision suppress:** status-auth deprovision is suppressed while OTA reprovision is pending or `Provision()` is in flight.
- **Containerd operator docs:** [container-engine.md](docs/edgelet/container-engine.md) documents embedded containerd `config.d` drop-ins, CDI devices (GPU/accelerators), and optional `config.toml.tmpl`; manifest reference and example updated for `cdiDevices`.

### Changed

- **Controller ping readiness:** `GET /api/v3/status` **503** means not ready (no certificate-style `verificationFailed`); **200** means connected. Pair with Controller **≥ v3.8.2** for full readiness and structured agent errors.
- **Status 503 / retryable errors:** never trigger auto-deprovision; legacy `{name, message}` 401 bodies use the same streak gate as structured auth failures.
- **Embedded containerd:** k3s fork **v2.2.3-k3s1 → v2.3.2-k3s2** (`containerd/api v1.11.1`, `k8s.io/cri-api v1.36.2-k3s2`); embed pin scripts updated (`check-containerd-fork.sh`, `version.sh`).
- **Containerd config v4:** generated `config.toml` uses **version 4** with `io.containerd.server.v1.grpc` / `io.containerd.server.v1.ttrpc` plugin blocks instead of legacy top-level `[grpc]`.
- **OTA install script (linux, `containerEngine: edgelet`):** `install.sh --upgrade` and `--rollback` stop/start `edgelet-containerd` only when the new binary’s embed hash differs from `data/current`; thin-only OTAs restart `edgelet` only. Fixes upgrade abort when invoked from `/usr/share/edgelet/install.sh`.
- **Encrypted OCI deps:** pin `github.com/containers/ocicrypt v1.3.2` and `github.com/containerd/imgcrypt/v2 v2.0.3` (ProtonMail `go-crypto/openpgp`).
- **Security / vulncheck:** remove **GO-2026-5932** exception; `make vulncheck` passes with zero documented exceptions ([SECURITY.md](SECURITY.md)).

### Fixed

- **First-hit status 401 deprovision:** transient auth or readiness failures during controller restart or OTA no longer immediately call `Deprovision(true)` and wipe local state (including controller-host volume mounts).
- **Microservice memory limit units:** controller sync and local deploy YAML `memoryLimit` values are interpreted as **MiB** (Pot/ioFog wire format) and converted to bytes before cgroup enforcement on all engines (`edgelet`, `docker`, `podman`). Fixes containers failing to start with crun errors such as “memory limit could be too low” when limits like `1024` were intended as 1 GiB.
- **ControlPlane register state on deprovision:** deprovision clears `system_control_plane.controller_registered` and `initial_rebuild_skipped` in SQLite (not only in-memory state), so reprovision re-registers the local controller microservice with Pot after an accidental deprovision.
- **Embedded containerd socket:** register containerd 2.3 server plugins (`grpc`, `ttrpc`, `metrics`, `debug`) so `/run/edgelet/containerd.sock` is created; fixes `edgelet-containerd` crash loop / health-check timeout after the 2.3 bump.
- **CRI env mapping:** `KeyValue.Value` is `[]byte` in cri-api v1.36.2-k3s2 (embedded engine build/runtime fix).
- **Interactive exec TERM:** TTY exec sessions inject `TERM=xterm-256color` when the container has none, on edgelet and docker/podman engines; controller exec default shell aligned with local interactive exec (no bare `clear` preamble).

## [1.0.1] - July 2026

### Added

- **Volume reference docs:** operator guide for CONFIGMAP/SECRET mounts, host paths, and persistence in `docs/edgelet/volumes.md`.
- **EdgeletAPI OpenAPI:** `GET /v1/controlplane/apply/{operationId}` documents `ControlPlaneApplyOperationResponse` with running, succeeded, and failed examples.

### Changed

- **Pot config key names:** local YAML and controller wire keys aligned with Pot `agent-service.js` — `diskLimit`, `logLimit`, `logDirectory`, `memoryLimit`, `cpuLimit` (CLI short codes `d`, `l`, `ld`, `m`, `p` unchanged). Legacy YAML aliases `logDiskDirectory` and `logDiskLimit` remain readable on load.
- **Controller config sync:** `GET config` applies only keys whose values differ from the in-memory config, avoiding unnecessary rewrites and validation on unchanged fields.
- **Disk limit ceiling:** maximum `diskLimit` and `logLimit` raised to **100 TB** (was 100 GB).
- **Image load (embedded engine):** `edgelet image load -f` accepts gzip-compressed `.tar.gz` archives as well as plain `.tar` on `containerEngine: edgelet`, matching `docker load` / `podman load` behavior (`docker` / `podman` engines unchanged).
- **Image remove selectors:** `edgelet image rm` resolves repository:tag (including short names such as `alpine:3.19`), full or short image IDs from `image ls`, and unique image ID prefixes (minimum 3 hex characters), with ambiguous prefix errors listing candidates.
- **Toolchain:** Go **1.26.5** in `go.mod`, CI workflows, Dockerfiles, and quality scripts.
- **Go dependencies:** minor bumps — `klauspost/compress` 1.19.0, `gopsutil/v4` 4.26.6, `golang.org/x/sys` 0.47.0, `golang.org/x/term` 0.45.0.

### Fixed

- **Controller config key mismatch:** Pot `GET config` snapshots no longer fail to map or spuriously reload when wire keys used legacy names (`diskConsumptionLimit`, `logDiskDirectory`, etc.) that did not match Edgelet's config model.
- **Embed bundle extract:** idempotent when hash dir exists; fixes `edgelet-containerd` crash loop on restart (`rename extracted bundle: file exists`).
- **Embed bundle aux readiness:** pin legacy `iptables` aux symlink after `EnsureExtracted`; log bundle readiness failures.
- **Data-plane shutdown:** remove `EnsureExtracted` from runtime-bootstrap SIGTERM shutdown path.
- **Data-plane recovery docs:** operator troubleshooting ladder; systemd `StartLimit` and openrc respawn guard on `edgelet-containerd`.
- **Coordinated MS drain** before data-plane stop on runtime split (`edgelet runtime drain`).
- **Catalog-agnostic shim/child teardown** with aligned 120s stop budget.
- **EBUSY-safe stale task cleanup** on data-plane bootstrap.
- **Data-plane orphan reap** (`edgelet runtime reap-orphans`) and operator recovery docs.
- **Containerd watchdog during data-plane drain:** control plane no longer self-restarts when the CRI socket is intentionally down during coordinated `edgelet-containerd` stop.
- **Control-plane resume after data-plane restart:** CRI usability probe before clearing quiesce; bootstrap drain CLI retries API/socket for 45s; `engine_ready_resume` wakes reconcile when `edgelet-containerd` returns.
- **Bootstrap drain outcome:** retry nested `edgelet runtime drain` on unix `EOF` after long drains; align CLI HTTP timeout with `--timeout` so server drain can exceed 60s.
- **Data-plane drain teardown:** removes drained workload containers so catalog runtimes recreate cleanly after restart.
- **Stale task cleanup:** recurse into `k8s.io` namespace dirs instead of deleting the namespace root.

### Known limitations

- ~~**govulncheck exception (embedded engine):** **GO-2026-5932** documents transitive `golang.org/x/crypto/openpgp` from containerd encrypted-image support (`ocicrypt` / `imgcrypt`); no upstream fix version.~~ Resolved in **1.0.2** (`ocicrypt v1.3.2` / `imgcrypt v2.0.3` pins).

## [1.0.0] - July 2026

### Added

- **Microservice control WebSocket signals:** when controller `microserviceConfig` changes, Edgelet pushes opcode `0xC` on `/v1/microservices/control` so workloads can fetch `GET /v1/microservices/config`. Agent hot reload pushes opcode `0xF` for resource-limit changes.
- **`edgelet system status` resource breakdown:** `agentCpuPercent`, `agentMemoryMiB`, optional embedded `runtime*` fields, and `edgeletTotal*` stack totals on `GET /v1/system/status`.

### Fixed

- **Agent log WebSocket idle disconnect:** quiet `follow=true` streams no longer drop at ~60s with `i/o timeout` and Controller close code 1006. Agent log sockets now use exec-parity WS ping/pong keepalive, a 120s pending read deadline aligned with Controller `logPendingTimeoutMs`, and no read deadline while actively streaming. Intentional session stop sends WebSocket close **1000** with reason `session stopped`. Pair with Controller WS ping on agent log/exec sockets.
- **Agent exec WebSocket idle policy:** exec agent sockets now match log keepalive (120s pending, no read deadline while active, graceful **1000** on intentional stop). Controller exec shell sessions use a **24-hour** agent-side max duration (was 30 minutes).

### Changed

- **Controller log/exec session cap:** agent log streaming stops after **24 hours** with close reason `max session duration` (edge safety cap; Controller session TTL may apply sooner).

- **Agent resource metrics:** `cpuUsage` / `memoryUsage` now report edgelet stack totals using process RSS and smoothed CPU (control plane + embedded containerd child when present). Controller `PUT status` keys are unchanged; reported values are more accurate (especially memory). External `docker` / `podman` engines remain agent-only totals.

## [1.0.0-rc.6] — June 2026

Release candidate introducing multi-session exec (Controller Plan 17 parity), local concurrent `edgelet ms exec`, and control-plane restart. Pair with Controller **v3.8.x** including Plan 17 multi-exec sessions.

### Added

- **Multi-exec session model:** field agent polls `GET /api/v3/agent/exec/sessions` and attaches per `sessionId` over session-scoped agent WebSocket; MessagePack `execId` equals `sessionId`.
- **Local concurrent exec:** unlimited concurrent `edgelet ms exec` sessions per microservice with owner-safe runtime ids and attach teardown.
- **Exec start gate:** `POST /v1/ms/{id}/exec/sessions` blocks up to 15s; timeout returns **`EXEC_START_TIMEOUT`** (CLI maps to a retry hint).
- **Status `execSessionIds[]`:** reports local controller attachment session ids per microservice (not local CLI sessions).
- **ControlPlane restart:** `edgelet controlplane restart [--pull]` and `POST /v1/system/controlplane/restart` bounce the local controller container while provisioned; preserves UUID and volumes.
- **Race detector Make targets:** `make test-race` (full unit-test tree on host), `make test-linux-race` (+ `-arm64` / `-amd64` for Linux Docker parity with CI tag matrix).

### Changed

- **Removed legacy agent exec WebSocket path** (`WS /agent/exec/{microserviceUuid}`) and init MessagePack pairing; exec is no longer gated on `execEnabled`.
- **ProcessManager exec registry:** owner-aware sessions on all engines; `StopExecSession` waits before delete with orphan sweep.

### Fixed

- **Local vs controller exec collision:** deterministic `{containerID}-exec` id removed; local and controller sessions no longer cross-kill.
- **Dead attach after POST 200:** sync start gate prevents returning a session id before the shell is ready.
- **Network manager boot retry:** no longer recurses on missing IPv4 at boot; degraded continue with background retry.
- **Embedded containerd stale tasks:** orphaned runtime task dirs cleaned on data-plane bootstrap without wiping image cache.
- **Controller reconnect reconcile:** field agent live-reconciles from Pot on reconnect; deduplicated init reload vs getChanges.

## [1.0.0-rc.4] — June 2026

Release candidate fixing controller exec/log WebSocket pairing after failed or timed-out sessions, and decoupling local API startup from Pot controller reachability. Pair with Controller **v3.8** (exec teardown raises `execSessions: true` on all disable paths).

### Changed

- **Exec WebSocket lifecycle:** each new exec session calls `Reset()` then a fresh dial to `GET /api/v3/agent/exec/{microserviceUuid}`; stale `Pending`/`Active` handler state no longer skips `Connect()`.
- **Log WebSocket lifecycle:** same reset-and-reconnect hygiene for log session handlers (`Reset()` before `Connect()` on session start; `Disconnect()` fully clears state).
- **Exec session reconcile:** `HandleExecSessions` treats a finished shell (`GetExecSessionStatus` → `false`) as not running; recreates exec when the process is gone or the controller WebSocket is disconnected.
- **Boot controller sync:** provisioned startup loads registries, volume mounts, and microservices from SQLite first via async `bootstrapControllerSync()`; live Pot refresh runs after ping succeeds without blocking supervisor startup.
- **Supervisor startup order:** Edgelet API starts after container engine and process-manager wire so `edgelet ms ls` and local status work before optional modules finish; `OnProcessManagerReady()` notifies PM when cache bootstrap completes before PM exists.
- **`IsControllerConnected`:** live paths use cached reachability from ping workers only (no inline controller ping from worker or sync code); cache reads (`fromFile=true`) unchanged.
- **JWT vs controller status:** valid credentials at boot set `NOT_CONNECTED` / `controllerVerified=false` until a successful ping; JWT success alone no longer implies Pot is reachable.
- **`/health/ready`:** ready when the local API is listening and daemon status is RUNNING or WARNING; not gated on provisioned state or controller connectivity.
- **CLI startup errors:** distinguish `LOCAL_API_STARTING` (daemon up, API not ready), `DAEMON_UNAVAILABLE` (no daemon), and `CONTROLLER_OFFLINE` (local API up, Pot unreachable); replaces the generic “still initializing” message for connection-refused cases.
- **Offline volume mounts at boot:** cache bootstrap skips live `loadVolumeMounts()` when the controller is unreachable; uses SQLite-backed volume-mount state instead.

### Fixed

- **Controller TLS for log/exec WebSocket sessions:** WSS dials load `controllerCert` from file paths (same as HTTPS), fixing `failed to decode PEM block` and `certificate signed by unknown authority` when `controllerCert` points at a PEM file.
- **`secureMode` config parsing:** values `false`, `0`, and `no` (including from `edgelet config --sec off`) now correctly disable secure mode instead of being treated as enabled.
- **Stale exec WebSocket after failed pairing:** after user timeout or controller cleanup, retrying exec without restarting edgelet no longer logs “skipping Connect (state=3)” while the controller sees no agent upgrade.
- **`Disconnect()` state leak:** exec and log handlers force `Disconnected` on teardown instead of leaving `Pending`/`Active` in memory (cached per-UUID handler map).
- **Orphan exec cleanup:** `execEnabled=false` always stops local exec and resets the WebSocket handler, even when session maps are empty.
- **Cancelled context on reconnect:** handler `ctx` is recreated on `Reset()` so ping/read workers survive a second connect.
- **Slow CLI after restart with Pot down:** `edgelet system info` and `edgelet ms ls` no longer wait on multi-minute sync controller I/O (ping, fog config, live volume mounts) before the local unix socket accepts connections.
- **Misleading “API still initializing”:** connection refused on `/run/edgelet/edgelet.sock` while the daemon process exists now reports local API startup (`LOCAL_API_STARTING`) instead of implying controller sync is in progress.
- **False controller-up at boot:** JWT initialization no longer sets `ControllerStatusOK` before the first successful ping, avoiding live Pot fetches during unreachable-controller startup.

### Known limitations (rc)

- **Pre-release:** `v1.0.0-rc.3` is a release candidate, not production GA.
- **Re-init on live socket:** controller supports init-frame reuse on an open agent socket; edgelet defaults to a fresh dial per new `execId` (optional optimization deferred).
- **Controller sync SLA:** local API target ≤15s with containerd up and Pot down; full microservice reconcile after Pot returns remains best-effort (no hard SLA).
- **Boot vs getChanges init:** SQLite-first bootstrap runs in parallel with the existing `initialization` / `config/changes` handshake; duplicate reconcile when Pot comes back may occur (follow-up).
- **Runtime restart order (split):** manual `systemctl restart edgelet && restart edgelet-containerd` can race attach; restart `edgelet-containerd` first, then `edgelet`.
- **Follow-up (not in this rc):** network manager unbounded retry on total IPv4 failure; automated stale containerd shim/task-dir cleanup; Pot REST / getChanges semantics unchanged.

## [1.0.0-rc.1] — mid-June 2026

Release candidate after provision lifecycle hardening, runtime observability fixes, and supply-chain follow-up. Pair with Controller **v3.8.0-beta.1** or newer v3.8 line.

### Added

- **Lifecycle dev gates:** `make pre-it` (vet + lint + lifecycle unit tests); optional `make test-lifecycle-race` and `make test-deadlock` for concurrency audits.
- **Deadlock audit doc:** operator-facing lock inventory and fixes in `docs/edgelet/deadlock-audit.md`.
- **Healthcheck shell helper:** shared `ShellCommandForScript` (bash → sh → busybox) for `CMD-SHELL` healthchecks and default exec sessions on minimal images.

### Changed

- **Live reprovision:** `edgelet system provision` on a running daemon reloads registries, volume mounts, microservices, and config without restarting background workers.
- **Split log basenames:** `edgelet.service` writes `edgelet.*.log`; `edgelet-containerd.service` writes `edgelet-containerd.*.log` under the same `logDirectory` (60/40 rotation budget when runtime split is enabled). See `docs/edgelet/logging.md`.
- **Disk usage metric:** `edgelet system status` `diskUsage` walks the full configured `diskDirectory` instead of legacy `messages/archive` + `volumes/` partial sums; automatic archive prune removed (greenfield).
- **Image drift (embedded engine):** reconcile compares images with `imageref.Match` so controller short names (e.g. `user/repo:tag`) match runtime `docker.io/` prefixes without endless `config_drift` UPDATE loops.
- **Deprovision cleanup:** volume-mount index cleanup no longer holds locks across slow filesystem work that blocked the process-manager monitor during background deprovision.
- **Changes worker resilience:** panic recovery and per-cycle timeout so `config/changes` polling continues at `changeFrequency` after handler failures.
- **Go dependencies:** minor bumps — `containerd/typeurl/v2` 2.3.0, `fsnotify` 1.10.1, `docker/go-connections` 0.7.0, `golang.org/x/crypto` 0.53.0, `pflag` 1.0.10.
- **Supply-chain CI:** SHA-pinned GitHub Actions bumps (checkout v6, docker setup-buildx/qemu v4, action-gh-release v3, cosign-installer 4.1.2) across ci, release, codeql, govulncheck, and scorecard workflows.
- **CI lint:** `quality-linux.sh` runs vet plus a single full golangci pass; CI lint job aligned (no redundant staticcheck chain).

### Fixed

- **Volume mount deadlock:** CONFIGMAP/SECRET version bumps during `config/changes` no longer re-acquire `indexLock` and freeze the changes worker.
- **Live reprovision gap:** reprovision without daemon restart now loads controller microservices (same path as cold `Start()`).
- **Deprovision monitor stall:** process-manager container monitoring continues through deprovision volume cleanup.
- **False-positive image drift:** embedded-engine reconcile no longer recreates microservices when only the Docker Hub hostname prefix differs.
- **Misleading disk metric:** status `diskUsage` now reflects deployed workload data under `/var/lib/edgelet/` when volumes and MS data are present.
- **Log file confusion:** control-plane traffic lands in `edgelet.0.log` instead of being rotated into secondary files while `edgelet-containerd` bootstrap stays in its own series.
- **`CMD-SHELL` healthchecks:** succeed on images with bash but no `/bin/sh` when bash is present.

### Known limitations (rc)

- **Pre-release:** `v1.0.0-rc.1` is a release candidate, not production GA.
- **Host-network DNS:** `edgelet.default.svc.bridge.local` injection remains an accepted limitation for host-network microservices.
- **Provisioned guard IT:** blocked `ms rm` / `controlplane delete` when provisioned still deferred (requires live system fog).

## [1.0.0-beta.3] — mid-June 2026

Production hardening release paired with Controller **v3.8.0-beta.1**. Deploy Edgelet and Controller v3.8 together; greenfield ControlPlane YAML only.

### Added

- **Controller microservice register-once:** after provision and local ControlPlane deploy, Edgelet calls `POST /api/v3/agent/controller/register` once and retries until success; no re-register on spec drift.
- **OTA controller semver:** `GET /api/v3/agent/version` `semver` field normalized to `vX.Y.Z` for `install.sh`; `provisionKey` and `expirationTime` (Unix ms) drive post-OTA reprovision (private key rotation only; stable `iofogUuid`).
- **Per-OS install paths:** documented linux, darwin, and windows directory tables in README and `docs/edgelet/installation.md`; embedded `uninstall.sh` in the install monolith; linux `/usr/share/edgelet/` ships both scripts after curl-only install.

### Changed

- **ControlPlane manifest v3.8:** OIDC auth, EdgeOps Console, TLS, and `controller.publicUrl` / `trustProxy`; canonical env projection (`AUTH_*`, `OIDC_*`, `CONSOLE_*`, `TLS_*`, `INTERMEDIATE_CERT`); host ports **51121** (API) and **80** → console.
- **Config hot-reload:** PATCH `/v1/config` and `edgelet system reload` apply log level without service restart; shared reload path for SIGHUP, PATCH, and POST reload; `logging.InstanceConfigUpdated` on hot reload.
- **Moby SDK (docker/podman engine):** replaced legacy `github.com/docker/docker` with `github.com/moby/moby/client@v0.4.1` and `github.com/moby/moby/api@v1.54.2`; removed govulncheck exceptions **GO-2026-4887** / **GO-2026-4883**.
- **`edgelet system reload` UX:** human success output (spinner model) when not using structured `-o`.

### Fixed

- **Curl-only linux install:** `/usr/share/edgelet/uninstall.sh` now present after pipe-to-bash install (embedded in assemble pipeline).
- **macOS install:** darwin uses `/var/run/edgelet` (not `/run`) and `/usr/local/share/edgelet/` for bundled scripts; avoids failures on default macOS layout.

### Breaking

- **Legacy ControlPlane YAML rejected:** keys such as `auth.url`, `ecnViewer*`, `spec.https`, Keycloak/viewer/ssl-era fields fail validation; use v3.8 schema and examples under `docs/edgelet/examples/controlplane.yaml`.

### Known limitations (beta)

- **Pre-release:** `v1.0.0-beta.3` is not production GA; pair with Controller **3.8.0-beta.1** or newer v3.8 line.
- **Provisioned guard IT:** blocked `ms rm` / `controlplane delete` when provisioned deferred (requires live system fog); unprovisioned lifecycle covered in CP IT.

## [1.0.0-beta.2] — mid-June 2026

### Fixed

- **Cgroup preflight on LXC/VM machine roots:** OpenRC hosts with `/.lxc` cgroup layout (e.g. OrbStack Alpine) no longer fail cold boot with a misleading docker `--privileged` error. Machine-root paths are distinguished from workload-nested containers; `edgelet cgroup-preflight` performs light mount/mode checks only; strict delegation validation runs after bootstrap prep.
- **LXC/VM machine OpenRC containerd restart:** `/.lxc/init` staging cgroups are treated as machine-root boundaries (not workload-nested). Runtime bootstrap skips Moby-style reparent when `edgelet-cgroup-prep` already delegated at the unified root and `/.lxc`, then prepares the OpenRC/service staging cgroup and `/edgelet` before spawning embedded containerd (OrbStack Alpine cold boot).
- **OpenRC sysinit prep:** `edgelet-cgroup-prep` reparents unified root and `/.lxc` cgroups and delegates `cpu`/`memory`/`pids` before `edgelet-containerd` starts (Moby/dind-style).

### Changed

- **`cgroupNested` status:** `true` only for workload containers (Docker/k8s dev deploy), not LXC/VM machine boundaries.

## [1.0.0-beta.1] — mid-June 2026
Hotfix pre-release after `v1.0.0-beta.0`. GitHub Releases remain binary-only; `install.sh` is now self-contained.
### Fixed
- **Curl / fresh-host install:** `install.sh` no longer requires co-located `scripts/lib/` or `packaging/init/` on the release download path. A single `install.sh` from GitHub Releases embeds init helpers and unit templates (assemble pipeline); fixes `Missing init helper scripts` on first install.
- **Post-install layout:** `/usr/share/edgelet/` ships self-contained `install.sh` / `uninstall.sh` and config samples only — not `lib/` or `init/` trees.
### Changed
- **Supply-chain CI:** Dependabot (gomod + github-actions), CodeQL for Go, pinned Actions and Docker base images, `read-all` workflow defaults, keyless cosign signatures on release assets, bounded fuzz smoke in CI, `SCORECARD_TOKEN` for OpenSSF Scorecard branch-protection checks.
- **Dependencies:** `golang.org/x/net` bumped (vulnerability reduction).
### Known limitations (beta)
- **Pre-release:** `v1.0.0-beta.1` is not production GA; expect refinements before 1.0.0.

## [1.0.0-beta.0] — mid-June 2026

First public pre-release of **Edgelet** — a greenfield edge runtime and ioFog/PoT node agent (`github.com/eclipse-iofog/edgelet`). GitHub Releases are marked **Pre-release** and ship binary-only artifacts (no DEB/RPM, no release tarballs).

### Added

- **Single `edgelet` binary** per platform: Linux thin wrapper (~31 MiB download) with k3s-style zstd embed and lazy extract to `/var/lib/edgelet/data/current/`; macOS and Windows monolithic builds for external Docker/Podman only.
- **Embedded Linux runtime** (`containerEngine: edgelet`, default on Linux): in-process containerd (k3s fork), CRI socket at `/run/edgelet/containerd.sock`, static `crun` 1.28 in the embed bundle.
- **Multi-engine support on Linux:** `edgelet`, `docker`, or `podman` via config; desktop platforms support `docker` and `podman` only.
- **EdgeletAPI** on-device operator API at `https://127.0.0.1:54321` with routes under `/v1/...`, RBAC group `edgelet.iofog.org/v1`, TLS/PKI under `/etc/edgelet/`.
- **Field agent** for Controller sync; Pot REST remains at `/api/v3/...` on the controller URL (unchanged contract).
- **Operator CLI** (Cobra): grouped commands (`system`, `ms`, `image`, `registry`, `runtimeclass`, `config`, `deploy`, `provision`), structured `-o json|yaml`, shell completion, generated docs under `docs/cli/`.
- **`edgelet init-config`:** write default config from template when missing (idempotent).
- **Binary-only install/OTA:** `install.sh` / `uninstall.sh` for Linux, macOS, and Windows; `--upgrade` / `--rollback` with receipt under `/var/backups/edgelet/`; six Linux init system templates.
- **Release packaging:** `scripts/release-binaries.sh` produces seven binaries + `SHA256SUMS` + config/CA samples.
- **Seven release binaries:** `edgelet-linux-{amd64,arm64,arm,riscv64}`, `edgelet-darwin-{amd64,arm64}`, `edgelet-windows-amd64.exe`.
- **Dual publish:** Eclipse upstream at [eclipse-iofog/edgelet](https://github.com/eclipse-iofog/edgelet); Datasance mirror at [Datasance/edgelet](https://github.com/Datasance/edgelet) with identical tags (separate GHCR namespaces).
- **Container image:** `ghcr.io/eclipse-iofog/edgelet:<tag>` (scratch base, `EDGELET_DAEMON=container`); Datasance mirror: `ghcr.io/datasance/edgelet:<tag>`.
- **macOS release build path:** `test/release/build-all.sh` (Docker embed loop) for developers without native Linux cross-toolchains.
- **Arch smoke scripts:** `test/release/smoke-linux-{arm,riscv64}.sh` for post-build daemon, version, and CRI socket checks.
- **Security gates:** `make security-code` (gosec), `make vulncheck` (govulncheck), CI workflow for vulnerability scanning; see [SECURITY.md](SECURITY.md).
- **SQLite persistence** for local state, deploy manifests, and runtime metadata.
- **Local deploy manifest** uses single `spec.image` (no per-arch image maps).
- **FogType / arch mapping:** amd64, arm64, riscv64, arm (32-bit) for provision and status display.

### Changed

- **Product identity:** greenfield rebrand to Edgelet — paths under `/var/lib/edgelet`, `/etc/edgelet`, `/run/edgelet`; labels and env vars use `edgelet.iofog.org/*` and `EDGELET_*`.
- **Daemon entry:** bare `edgelet` invokes the CLI; start the daemon with `edgelet daemon` or `systemctl start edgelet`.
- **Provision payload** includes configured `containerEngine` and build metadata sent to the Controller.
- **CLI redesign (breaking vs legacy ioFog Agent CLI):** command groups and flags replaced flat `iofog-agent` verbs; see [docs/edgelet/migration-from-iofog-agent-cli.md](docs/edgelet/migration-from-iofog-agent-cli.md). Daemon unreachable returns exit code **10**.
- **Documentation:** operator docs under `docs/edgelet/` (architecture, deployment, EdgeletAPI, container engine).
- **Toolchain:** Go 1.26.x; containerd **v2.2.3-k3s1** with pinned CRI API replacements.
- **Quality tooling:** golangci-lint v2 (govet, revive, staticcheck, errcheck, formatters, misspell, errorlint); gosec run separately from lint.

### Fixed

- **Embed packaging:** single `.tar.zst` per arch in `go:embed` (prevents multi-arch artifact accumulation inflating binary size).
- **CRI lifecycle:** microservice restart and stop/start on the embedded engine use remove+create+start instead of failing with non-restartable container errors.
- **Logging on arm32:** size cap avoids int overflow on 32-bit platforms.
- **Embed cross-build on macOS:** host-arch tooling and zlib dev packages for arm/riscv64 fat-runtime link.
- **gosec and vulnerability findings** addressed across `cmd/`, `internal/`, and `pkg/`; documented exceptions only where noted in [SECURITY.md](SECURITY.md).

### Known limitations (beta)

- **Pre-release:** `v1.0.0-beta.0` is not a production GA; expect API and packaging refinements before 1.0.0.
- **Windows (Tier 2):** `edgelet-windows-amd64.exe` is built and published; there is no Windows integration-test matrix or Windows service installer in this release.
- **macOS:** supported as a **development platform** with external Docker/Podman only — not positioned as a production far-edge node OS.
- **linux/arm smoke depth:** arm32 (`edgelet-linux-arm`) builds in the release matrix; full arch smoke is validated on Linux with binfmt or native hardware. Running arm smoke under macOS Docker/QEMU may segfault after embed extract even when the binary build succeeds.
- **linux/riscv64:** release build and smoke script pass on macOS Docker; fleet validation on real riscv64 hardware is limited in beta.
- **Binary-only distribution:** no DEB/RPM packages and no release `.tar.gz` bundles; use `install.sh` or copy the raw binary.
- **OTA depth:** one previous release for thin-binary rollback; fat embed bundle keeps `current` / `previous` symlinks only.
- **Codecov:** coverage upload not wired; badge is a placeholder until post-beta CI work.
- **Dependency exceptions (docker/podman engine):** govulncheck documents two accepted findings in the pinned `github.com/docker/docker` client SDK (Moby AuthZ plugin advisories **GO-2026-4887** / **GO-2026-4883**, CVE-2026-34040). Edgelet uses the SDK as a **client** to local engines; typical edge deployments do not enable AuthZ plugins. Operators should run a patched Docker Engine (≥ 29.3.1) or equivalent Podman. Full rationale and fix timeline: [SECURITY.md](SECURITY.md).

### Binary size (linux thin download gate)

| Arch          | Thin binary | ≤ 55 MiB |
|---------------|-------------|----------|
| linux/amd64   | ~34.7 MiB   | yes      |
| linux/arm64   | ~31.7 MiB   | yes      |
| linux/riscv64 | ~32.2 MiB   | yes      |
| linux/arm     | ~31.8 MiB   | yes      |