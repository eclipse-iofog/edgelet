# Edgelet init systems (operator guide)

Production-grade Linux init integration: ordering, cgroup delegation, engine dependencies, and a shared control-plane stop path.

---

## Support tiers

| Tier | Init systems | SLA |
|------|--------------|-----|
| **Tier 1 (production)** | **systemd**, **openrc** (Alpine/Gentoo), **procd** (OpenWrt) | Full IT (`test/init/`) or manual checklist ([OpenWrt procd gate](../../test/init/openwrt-procd-checklist.md)) |
| **Tier 2 (best effort)** | sysvinit, s6, runit, upstart | Hardened templates; same preflight/shutdown helpers; documented limits |

---

## Distro × init × engine matrix

| Distro family | Typical init | Tier | `containerEngine=edgelet` | `docker` / `podman` |
|---------------|--------------|------|---------------------------|---------------------|
| Ubuntu / Debian / RHEL / Fedora | systemd | 1 | Recommended (DelegateSubgroup) | Drop-in orders after engine unit |
| Alpine / Gentoo | openrc | 1 | Allowed (cgroupfs; **static embed** required on musl) | `need docker` / `need podman` in `depend()` |
| OpenWrt | procd | 1 | Allowed (static embed; cgroupfs) | Operator wires docker/podman separately |
| RHEL legacy / old appliances | sysvinit | 2 | Allowed; cgroupfs driver | No engine unit ordering in LSB script |
| Container / minimal | s6, runit | 2 | Allowed | Operator wires engine separately |
| Ubuntu 14–16 era | upstart | 2 | Allowed | `pre-start` preflight only |

**Production SLA:** prefer **systemd** for embedded (`edgelet`) so cgroup v2 delegation uses `DelegateSubgroup`. Non-systemd embedded is supported (cgroupfs driver) but documented as best-effort for tier 2.

---

## Canonical packaging layout

| Item | Path |
|------|------|
| systemd control unit | `packaging/init/systemd/edgelet.service` |
| systemd data-plane stub | `packaging/init/systemd/edgelet-containerd.service` |
| Engine drop-ins | `packaging/init/systemd/edgelet.service.d/{docker,podman}.conf` |
| openrc / procd / sysv / s6 / runit / upstart | `packaging/init/{openrc,procd,sysvinit,s6,runit,upstart}/` |
| Shutdown helper | `scripts/edgelet-shutdown` → `/usr/libexec/edgelet/edgelet-shutdown` |
| Shipped templates on node | `/usr/share/edgelet/init/` |

Install uses **`packaging/init/` only** — there is no `packaging/systemd/` install path (CI-guarded).

---

## Shared helpers

| Helper | Command / path | When |
|--------|----------------|------|
| **Preflight** | `edgelet cgroup-preflight` | `start_pre` (openrc), sysv/s6/runit/upstart start, before daemon |
| **Shutdown** | `/usr/libexec/edgelet/edgelet-shutdown` → `edgelet shutdown` | systemd `ExecStop`, all init `stop` paths |
| **Data-plane orphan reap** | `edgelet runtime reap-orphans` | systemd `ExecStopPost` on `edgelet-containerd`; OpenRC `stop` hook. Runs only after `/run/edgelet/drain-verified` exists |

Preflight on the **thin** `/usr/local/bin/edgelet` uses a procfs/cgroupfs-only probe (`DetectPreflight`); full cgroup subtree setup stays in the **fat** runtime (`Detect` / `Bootstrap` with `containerd/cgroups`).

`edgelet shutdown` tries EdgeletAPI graceful stop, then SIGTERM/SIGKILL fallback. **Drain / leave-running policy** is defined in [workload-continuity.md](workload-continuity.md); init templates only define the stop entry.

`TimeoutStopSec=120` on systemd is `shutdownGracePeriodSeconds` (90) plus a 30 second buffer. The data-plane unit uses the same 120 second budget for CRI drain and, after verify, stopping containerd and shim reap. `systemctl stop edgelet-containerd` and `rc-service edgelet-containerd stop` send SIGTERM to `edgelet runtime-bootstrap`. That process drains labeled workloads through CRI and verifies before it stops containerd. Verify failure leaves containerd running and does not reap shims. procd, sysvinit, s6, runit, and upstart have no separate containerd unit; `install.sh` signals the same `runtime-bootstrap` process when a fat upgrade must stop the data plane.

`edgelet-containerd` runs `edgelet runtime reap-orphans` after stop (systemd `ExecStopPost`, OpenRC stop hook). Reap runs only when `/run/edgelet/drain-verified` exists — an incomplete drain does not reap shims. Embedded engine uses `edgelet.service.d/edgelet.conf` drop-in with `EDGELET_RUNTIME_SPLIT=1`.

---

## systemd (Tier 1)

| Setting | Value |
|---------|--------|
| `Delegate` | `yes` |
| `DelegateSubgroup` | `supervisor` (avoids cgroup v2 EBUSY on restart) |
| `KillMode` | `process` on `edgelet` and `edgelet-containerd`. systemd does not kill the cgroup |
| `ExecStop` | `/usr/libexec/edgelet/edgelet-shutdown` |
| Engine ordering | Install selects `edgelet.service.d/docker.conf` or `podman.conf` — not `sed` on the base unit |

```bash
systemctl cat edgelet
systemctl show edgelet -p DelegateSubgroup,TimeoutStopSec
```

Enable `edgelet-containerd.service` before `edgelet.service` for embedded split.

**Data-plane stop:** `KillMode=process` stays on `edgelet-containerd.service`. Stopping that unit signals `runtime-bootstrap`, which drains and verifies before containerd stops. A failed verify does not reap shims and does not exit the parent (containerd keeps serving). See [workload-continuity.md](workload-continuity.md#embedded-engine-runtime-split).

**Unexpected containerd child exit:** if the embedded containerd process dies while `runtime-bootstrap` is running, the parent logs the exit and exits with status 1. systemd `Restart=always` on `edgelet-containerd.service` starts a new data plane. That path is separate from a failed drain verify (which leaves the unit running).

**Data-plane crash-loop guard:** `edgelet-containerd.service` uses `StartLimitIntervalSec=300`, `StartLimitBurst=5`, and `RestartSec=5s`. After five rapid failures within 300s, systemd stops auto-restarting until `systemctl reset-failed edgelet-containerd`. OpenRC stub uses `respawn_max=5`, `respawn_period=300`, `respawn_delay=5` (parity).

Split embedded units are **siblings**: `Wants`/`After` on `edgelet.service` only orders **start**. There is no `PartOf` — `systemctl stop edgelet` does not stop `edgelet-containerd`. Full teardown stops both units (see [workload-continuity.md](workload-continuity.md)).

**Monolithic embedded:** the control unit omits `ProtectSystem=strict` until the data-plane unit owns containerd (embedded needs `/etc/cni`, `/run`, `/opt`, etc.). `init-edgelet.sh` creates `/etc/cni/net.d`, `/run/edgelet`, and `/run/containerd` before `systemctl start`.

---

## openrc (Tier 1)

- `depend()`: `net` + optional `docker` / `podman` (install-time)
- `start_pre`: `edgelet cgroup-preflight`
- `stop`: `edgelet-shutdown`
- Stub: `/etc/init.d/edgelet-containerd` (runtime split chain)

```bash
rc-service edgelet-containerd restart   # need edgelet-containerd also restarts edgelet
rc-service edgelet stop
```

**Split embedded:** `need edgelet-containerd` means `rc-service edgelet-containerd restart`
stops and restarts `edgelet` automatically. Do **not** chain `rc-service edgelet restart`
immediately after — that double-cycles the control plane and can fail attach.
For control-only restart (MS survive): `rc-service edgelet restart` alone.
OpenRC units wait for `/run/edgelet/containerd.sock` in `start_post` / `start_pre`; control
plane uses `supervise-daemon` respawn (`respawn_max=0`, parity with systemd `Restart=always`).
**Data-plane stub** (`edgelet-containerd`): `supervise-daemon` with `respawn_max=5`,
`respawn_period=300`, `respawn_delay=5` — stops respawning after five failures in 300s.

### Logging (openrc)

| Log | Source |
|-----|--------|
| `/var/log/edgelet/daemon.log` | OpenRC `output_log` / `error_log` — thin wrapper and pre-exec errors |
| `/var/log/edgelet/edgelet.0.log` (rotated) | Fat daemon after `edgelet daemon` starts (`logDirectory`) |

---

## procd (Tier 1 — OpenWrt)

- Template: `packaging/init/procd/edgelet` (`USE_PROCD=1`)
- `start_service`: `edgelet cgroup-preflight` then `edgelet daemon` under procd respawn
- `stop_service`: `/usr/libexec/edgelet/edgelet-shutdown`
- Detection: **procd before openrc** when `/sbin/procd` and `/etc/rc.common` exist

```bash
/etc/init.d/edgelet enable
/etc/init.d/edgelet start
/etc/init.d/edgelet stop
```

On some images the binary lives at `/usr/sbin/edgelet`; install.sh still defaults to `/usr/local/bin/edgelet` — adjust paths in the template if your image requires it.

Manual gate: [test/init/openwrt-procd-checklist.md](../../test/init/openwrt-procd-checklist.md).

---

## Portable embedded engine (static fat)

Fat `edgelet` in the embed tar is **statically linked by default** so musl hosts (Alpine, OpenWrt) can `exec` the runtime without glibc `ld-linux`. Build with `make build-linux-<arch>`; opt out via `STATIC_BUILD=false` for faster local builds only.

---

## Tier 2 limits

| Topic | Tier 2 behavior |
|-------|-----------------|
| `DelegateSubgroup` | **Not available** — no systemd delegation |
| Embedded cgroup driver | **cgroupfs** on non-systemd |
| Engine ordering | Manual / site-specific (except openrc tier 1) |
| MS survival on control restart | Document monolithic behavior until runtime split |

All tier-2 templates call the same **`edgelet-shutdown`** helper as systemd.

---

## Install

```bash
sudo ./install.sh --bin-path=build/edgelet-linux-amd64 --container-engine=docker
```

Init detection: `scripts/lib/init-detect.sh` (OpenRC when the supervisor is active — `rc-status` or `/etc/inittab` — not merely when `openrc-run` exists; Alpine links `/sbin/init` to busybox). Unit install: `scripts/lib/init-edgelet.sh`.

---

## Integration tests

See [test/init/README.md](../../test/init/README.md): systemd install smoke, Alpine openrc init/runtime, RHEL sysv checklist, OpenWrt procd checklist.

---

## Related docs

- [cgroups.md](cgroups.md) — cgroup driver detection for embedded engine
- [workload-continuity.md](workload-continuity.md) — control/data plane split
- [installation.md](installation.md) — install paths
- [deployment.md](deployment.md) — production topology
