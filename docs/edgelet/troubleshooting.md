# Edgelet troubleshooting

Common issues when running Edgelet on edge nodes.

## Daemon won't start

**Symptoms:** `systemctl start edgelet` fails; no listening socket on `:54321`.

**Checks:**

1. Validate config paths and permissions:

   ```bash
   ls -la /etc/edgelet/
   edgelet system info
   ```

2. Review journal logs:

   ```bash
   sudo journalctl -u edgelet -n 100 --no-pager
   tail -f /var/log/edgelet/daemon-startup.log
   ```

3. Confirm `containerEngine` is valid on this platform:

   ```bash
   edgelet system version -o json | jq '{allowedEngines, containerEngine}'
   grep containerEngine /etc/edgelet/config.yaml
   ```

   Linux allows `edgelet`, `docker`, or `podman`. Darwin/windows allow `docker` or `podman` only.

4. Check disk space:

   ```bash
   df -h /var/lib/edgelet /var/lib/edgelet-containerd
   ```

   If usage grows after deleting microservices, private `VOLUME` data remains under `/var/lib/edgelet/volumes/data/` and shared names under `/var/lib/edgelet/volumes/shared/` until you reclaim with `edgelet volume` — see [volumes.md](volumes.md). Scheduled prune and `edgelet system prune` do not delete those trees.

---

## Cgroup delegation (embedded edgelet engine)

**Symptoms:** daemon fails at startup with `controller cpu is not available`; `RunPodSandbox` errors; nested Docker deploy fails immediately.

**Checks:**

1. Confirm cgroup mode and driver:

   ```bash
   edgelet system status -o json | jq '{cgroupMode,cgroupDriver,cgroupNested,cgroupDelegatedControllers}'
   ```

2. On **cgroup v2** hosts, verify delegated controllers in the edgelet cgroup:

   ```bash
   grep -E '^(cpu|memory|pids)' /sys/fs/cgroup/cgroup.controllers
   cat /sys/fs/cgroup/cgroup.subtree_control
   ```

3. **Hybrid v1+v2:** edgelet logs a warning and prefers unified v2 — migrate the host to pure cgroup v2 when possible. See [cgroups.md](cgroups.md).

4. **Nested edgelet container in Docker:** `--privileged` is required:

   ```bash
   docker run -d --name edgelet --privileged \
     -v /var/lib/edgelet:/var/lib/edgelet \
     -v /etc/edgelet:/etc/edgelet \
     ghcr.io/eclipse-iofog/edgelet:<tag>
   ```

   Datasance mirror: `ghcr.io/datasance/edgelet:<tag>`

   Without `--privileged`, cpu/memory/pids controllers are not delegated and CRI cannot create sandboxes.

5. **Non-systemd init** (openrc, sysvinit, s6): edgelet uses the **cgroupfs** driver automatically — ensure `/sys/fs/cgroup` is writable and controllers are mounted.

6. **LXC/VM machine root** (`/.lxc`, OpenRC on OrbStack Alpine): error mentions `edgelet-cgroup-prep` or empty `/.lxc/cgroup.controllers` after cold boot:

   ```bash
   rc-status | grep edgelet-cgroup-prep
   cat /proc/self/cgroup
   cat /sys/fs/cgroup/.lxc/cgroup.controllers
   ```

   Reinstall edgelet so `install.sh` registers `edgelet-cgroup-prep` in **sysinit**, then reboot:

   ```bash
   sudo ./install.sh --version=<tag>
   sudo reboot
   ```

   After reboot, `edgelet cgroup-preflight` should pass and `rc-service edgelet-containerd start` should succeed without manual cgroup commands.

   If **`edgelet-containerd` fails after a prior successful start** with `preparing machine-root cgroup delegation` or immediate spawn failure in `/var/log/edgelet/containerd.log`, upgrade to a current beta.2 build (skips reparent when prep already ran and prepares OpenRC staging + `/edgelet` cgroups). Do not run manual Moby reparent commands if `edgelet-cgroup-prep` already ran at sysinit.

7. **`RunPodSandbox` / `sd-bus call: Invalid unit name or type`:** crun with `SystemdCgroup=true` (manual [`config.d` override](container-engine.md#containerd-configuration-edgelet-engine) or an old edgelet build). Reinstall the current edgelet build — generated config must have **`SystemdCgroup = false`** for crun; systemd bare-metal hosts must stay in `edgelet.service` with no `cgroup.path`. Verify:

   ```bash
   edgelet system status -o json | jq '{cgroupDriver,cgroupContainerdPath}'
   grep -E '^\[cgroup\]|path =|SystemdCgroup' /var/lib/edgelet-containerd/config.toml
   cat /proc/$(systemctl show edgelet -p MainPID --value)/cgroup
   ```

8. **`bpf pin to /sys/fs/bpf/crun/k8s_io/...`:** often appears when `SystemdCgroup=true` was enabled for crun. Use a current edgelet build (`SystemdCgroup = false`). If BPF is still required on your host, ensure `/sys/fs/bpf` is mounted and `/sys/fs/bpf/crun/k8s_io` exists (see [cgroups.md](cgroups.md)).

See [cgroups.md](cgroups.md) for the full matrix.

---

## Control vs data plane restart

**Symptoms:** MS disappear after `systemctl restart edgelet`; unexpected downtime during agent OTA.

**Checks:**

1. Identify which unit you restarted:

   ```bash
   systemctl status edgelet edgelet-containerd --no-pager
   ```

2. **docker/podman:** control restart should **not** drain MS (`shutdownPolicy=leave-running` default):

   ```bash
   grep shutdownPolicy /etc/edgelet/config.yaml
   edgelet system status -o json | jq '."runtime.shutdownPolicy", ."runtime.agentPhase"'
   docker ps --filter label=iofog.org/microservice-uuid
   ```

3. **embedded split:** restart **`edgelet` only** for control OTA; restart **`edgelet-containerd`** only when the fat/runtime bundle changed (expect MS stop + reconcile).

4. **Monolithic embedded** (no `edgelet-containerd` active): `restart edgelet` still drains MS — enable runtime split per [workload-continuity.md](workload-continuity.md).

5. **Cold engine change**: changing `containerEngine` always recreates MS — this is expected and unrelated to workload continuity.

6. **Legacy unit on VM:** if `systemctl cat edgelet-containerd` shows `PartOf=edgelet.service`, reinstall packaging and run `systemctl daemon-reload` — that forces data-plane stop on control restart (embedded control-only restart gate failure).

See [workload-continuity.md](workload-continuity.md) for the full OTA matrix.

---

## Embed bundle / data-plane restart

**Symptoms:** `edgelet-containerd` in a crash loop; journal shows `rename extracted bundle: file exists`; `Preparing data dir` on every start; MS stuck after repeated data-plane restarts.

**Diagnostic:**

```bash
sudo journalctl -u edgelet-containerd -n 80 --no-pager
readlink /var/lib/edgelet/data/current/bin/aux/iptables   # expect xtables-legacy-multi
ls -la /var/lib/edgelet/data/current /var/lib/edgelet/data/.lock
```

**Recovery ladder:**

1. Stop both units and clear systemd start-limit (if applicable):

   ```bash
   sudo systemctl stop edgelet edgelet-containerd
   sudo systemctl reset-failed edgelet-containerd
   ```

   OpenRC: `rc-service edgelet stop && rc-service edgelet-containerd stop`

2. **In-place repair** (aux symlink drift):

   ```bash
   sudo ln -sf xtables-legacy-multi /var/lib/edgelet/data/current/bin/aux/iptables
   ```

3. Start data plane; wait for readiness:

   ```bash
   sudo systemctl start edgelet-containerd
   sudo journalctl -u edgelet-containerd -f   # Embedded containerd is ready
   ```

   OpenRC: `rc-service edgelet-containerd start`

4. Start control plane:

   ```bash
   sudo systemctl start edgelet
   ```

5. **Nuclear** (last resort — re-extracts bundle; MS stop + reconcile):

   ```bash
   sudo systemctl stop edgelet edgelet-containerd
   sudo rm -rf /var/lib/edgelet/data/<hash-dir> /var/lib/edgelet/data/<hash-dir>-tmp /var/lib/edgelet/data/.lock
   sudo systemctl start edgelet-containerd
   sleep 5
   sudo systemctl start edgelet
   ```

   Replace `<hash-dir>` with the bundle directory name under `/var/lib/edgelet/data/` (not `current` or `previous` symlinks). After a healthy daemon start, only those two hash trees remain; leftover extracts from older upgrades are removed automatically.

Prefer **`systemctl stop` then `systemctl start`** over blind `restart` during shim upgrades — see [container-engine.md](container-engine.md). After five rapid failures within 300s, systemd stops auto-restarting `edgelet-containerd` until `reset-failed` (openrc: `respawn_max=5` per 300s window).

---

## Catalog runtime / orphan shims after data-plane restart

**Symptoms:** `device or resource busy` on sandbox or task cleanup; `dial unix:///run/edgelet/containerd.sock: timeout`; journal mentions `left-over process` or `containerd-shim-*` after `edgelet-containerd` stop; catalog workloads (WASM, spin, etc.) fail to start until manual cleanup.

**Checks:**

```bash
sudo journalctl -u edgelet-containerd -n 80 --no-pager
pgrep -af 'containerd-shim-.*edgelet/containerd.sock' || true
pgrep -af -- '--edgelet-containerd-child' || true
```

**Recovery:**

1. Stop both units:

   ```bash
   sudo systemctl stop edgelet edgelet-containerd
   ```

   OpenRC: `rc-service edgelet stop && rc-service edgelet-containerd stop`

2. Reap orphaned data-plane processes:

   ```bash
   sudo edgelet runtime reap-orphans
   ```

3. Clear systemd start-limit if the data plane was crash-looping:

   ```bash
   sudo systemctl reset-failed edgelet-containerd
   ```

4. Start data plane, then control:

   ```bash
   sudo systemctl start edgelet-containerd
   sleep 3
   sudo systemctl start edgelet
   ```

The command only targets edgelet embedded containerd (`--edgelet-containerd-child` and shims referencing `/run/edgelet/containerd.sock`). It does not kill host Docker or Podman shims.

Prefer **`stop` then `start`** on the data plane when upgrading catalog shims — see [container-engine.md](container-engine.md#data-plane-restart-and-shim-upgrades-embedded).

---

## Containerd socket (edgelet engine)

**Symptoms:** `connection refused` to `/run/edgelet/containerd.sock`; microservices stuck in pull/create.

**Checks:**

1. Verify socket exists:

   ```bash
   ls -la /run/edgelet/containerd.sock
   ```

2. Check embedded containerd logs in daemon journal output.

3. Shim load errors such as `address: no such file` usually mean stale runtime task metadata. Restart the **data plane** first (`systemctl restart edgelet-containerd`); stale task cleanup runs on the next data-plane bootstrap. Then restart control if needed:

   ```bash
   systemctl restart edgelet-containerd.service
   sleep 3
   systemctl restart edgelet.service
   ```

4. Look for orphan overlay mounts blocking cleanup:

   ```bash
   mount | grep edgelet
   ```

5. Restart daemon (graceful):

   ```bash
   sudo systemctl restart edgelet
   ```

---

## EdgeletAPI 401 / auth failures

**Symptoms:** CLI exits **3** (Unauthorized); `curl` returns 401.

**Checks:**

1. Token file present and readable:

   ```bash
   sudo ls -la /etc/edgelet/edgelet-api
   ```

2. CLI uses correct CA:

   ```bash
   edgelet auth whoami
   edgelet auth whoami -o json
   ```

3. After provisioning, ensure you are not sending an unsigned bootstrap JWT.

4. Regenerate PKI only when directed — mismatched CA breaks existing CLI sessions.

---

## Controller status 401 / unexpected deprovision

**Symptoms:** Agent deprovisions during controller restart or OTA; logs show repeated `PUT status` **401** or **503**; controller-host node loses controller SQLite.

**Context (v1.0.2+):** Edgelet no longer deprovisions on the first status **401**. Auto-deprovision runs only after **5 consecutive** status auth failures spanning at least **60 seconds**, and is suppressed while OTA reprovision is pending or `Provision()` is in flight. Status **503** and `retryable: true` responses never deprovision.

**Checks:**

1. Controller version — full structured errors and readiness `/status` semantics require **Controller ≥ v3.8.2**. Older controllers still benefit from the streak gate but may return legacy error bodies.

2. During OTA or controller restart, expect transient **401** or **503** on status POST and ping; the agent should stay provisioned and retry.

3. If deprovision still occurs after sustained auth failures, verify the provision key was revoked (`delete-node` still deprovisions immediately) or Edge Guard hardware drift (see [edgeguard.md](edgeguard.md)).

4. Inspect field agent logs for `status auth failure` deferral messages vs final deprovision after the gate opens.

---

## CLI connectivity (exit 10)

**Symptoms:** `Error[DAEMON_UNAVAILABLE]`; exit code **10**.

**Checks:**

1. Daemon running:

   ```bash
   systemctl is-active edgelet
   edgelet system status
   ```

2. Socket override — if using `--socket`, path must match `/run/edgelet/edgelet.sock`.

3. Firewall — EdgeletAPI listens on localhost/TLS; remote access is not the default operator path.

---

## External Docker / Podman

**Symptoms:** `Cannot connect to Docker daemon`; containers not starting.

```bash
sudo systemctl status docker    # or podman.socket
ls -la /var/run/docker.sock
grep containerEngineUrl /etc/edgelet/config.yaml
```

Ensure the configured `containerEngineUrl` matches the running engine socket.

---

## Leftover process holding a volume

**Symptoms:** microservice status or `errorMessage` is `volume in use by a leftover process`; a new container logs `Cannot lock file` or another exclusive-lock error and exits (databases that exclusive-lock a file often do this). Common after a fat OTA or a data-plane stop whose drain did not verify. The volume files are still on disk. A host process still has them open.

Do **not** run `edgelet volume rm` (or delete `volumes/data/` / `volumes/shared/`) to clear the lock. That destroys retained data and does not stop the process that holds the file.

**Checks:**

1. Confirm the wait text:

   ```bash
   edgelet ms inspect <uuid|namespace.name> --summary
   ```

2. Read the data-plane journal and the upgrade log. Failed drain is `module=RUNTIME_BOOTSTRAP` with `drain_verify_failed`, `drain_timeout`, or `drain_degraded`, and `data-plane drain did not verify`. `install.sh` prints `Data-plane drain did not verify; binary was not replaced` and leaves the previous thin binary installed.

   ```bash
   sudo journalctl -u edgelet-containerd -n 200 --no-pager | grep RUNTIME_BOOTSTRAP
   ```

3. Find the process that still has the volume open (default `diskDirectory` is `/var/lib/edgelet`):

   ```bash
   sudo lsof +D /var/lib/edgelet/volumes/data /var/lib/edgelet/volumes/shared
   sudo fuser -v /var/lib/edgelet/volumes/data /var/lib/edgelet/volumes/shared
   ```

**Recovery:** stop the process that holds the volume. Reconcile keeps the current runtime state and the text `volume in use by a leftover process` until that process is gone, including after an operator rebuild. The next cycle creates the container once the path is free.

On a node that is already up, stopping the holder is enough. Do not restart the data plane unless you intend to stop every workload.

When an upgrade or `stop edgelet-containerd` aborted, the journal line is `leaving containerd running`. Drain did not verify: `runtime-bootstrap` cleared the drain hold and stayed running with containerd still up (the unit did not exit, so systemd did not restart it). A leftover workload process may still hold the volume. `KillMode=process` means stop only signaled the parent; the containerd child can still be serving CRI. `edgelet runtime reap-orphans` does nothing until `/run/edgelet/drain-verified` exists. Stop the holder, then drain again while the CRI socket is up:

```bash
sudo edgelet runtime drain --direct
```

Exit 0 means verify passed. Re-run `install.sh` for an aborted upgrade, or stop and start the data plane only after that:

```bash
sudo systemctl stop edgelet-containerd
sudo systemctl start edgelet-containerd
```

OpenRC: `rc-service edgelet-containerd stop`, then `start`, after `drain --direct` exits 0.

---

## Microservice crash / restart loop

**Symptoms:** dashboard `errorMessage` empty during a crash; Docker/Podman exit looks silent; recreate hammers the node; `STUCK_IN_RESTART` after a container sat in `EXITING`. `Cannot lock file` on a persistent volume is a leftover process, not this crash loop — see [Leftover process holding a volume](#leftover-process-holding-a-volume).

**Checks:**

1. Inspect the workload. Crash fields are the same on `GET /v1/ms/{id}` and `edgelet ms inspect`:

   ```bash
   edgelet ms inspect <uuid|namespace.name> --summary
   ```

   - `errorMessage` is the **current** failure. It stays set while the workload is failing, restarting, or has been RUNNING for less than **30 seconds** after a crash. After 30 seconds of continuous RUNNING it is sent as `""`.
   - `lastError` / `lastErrorAt` is the last crash text (unix ms). It is **not** cleared on recovery. Operator **rebuild** resets `restartCount` to 0 and keeps `lastError`.
   - Docker/Podman text looks like `exitCode=N oomKilled=…` (plus `error=…` when the engine error is set). The embedded engine keeps `CRI reason=…`.

2. Crash loops delay recreate (**10s, 20s, … up to 5 minutes**). Status stays the real runtime state (`EXITING`, and so on) — there is no extra “waiting” state. Operator rebuild, catalog becoming Ready, and a single non-restartable CRI recreate skip the delay.

3. `STUCK_IN_RESTART` means **10 real restarts in 10 minutes**, not status-poll ticks. Use operator **rebuild** to retry.

4. Last crash text for controller-managed workloads is in-memory. Restarting `edgelet` may drop it until the next failure.

---

## Controller connectivity

**Symptoms:** `connectionToController` not ok in `edgelet system status`.

```bash
edgelet system status -o json | jq .connectionToController
curl -v "$(grep ^controller: /etc/edgelet/config.yaml | awk '{print $2}')/health"
edgelet config cert <base64-controller-cert>   # if TLS verification fails
```

---

## Embedded integration tests

For embedded-engine regressions on macOS, use the Lima VM pipeline:

```bash
./test/embedded/run-all.sh
```

See [../../test/embedded/README.md](../../test/embedded/README.md).

---

## Microservice exec

**Symptoms:** `Error[EXEC_START_TIMEOUT]`; attach fails with `Error[NOT_FOUND]`; controller exec works but local `ms exec` fails (or vice versa).

**Checks:**

1. Confirm the microservice container is running:

   ```bash
   edgelet ms inspect <uuid|namespace.name>
   edgelet ms ls
   ```

2. Retry after a start timeout — POST waits up to **15 seconds** for the shell:

   ```bash
   edgelet ms exec <uuid> -- /bin/sh
   ```

   See [exec-sessions.md](exec-sessions.md) for the multi-session model and local vs controller limits.

3. **Concurrent sessions:** local CLI exec is unlimited per microservice; controller exec is capped at **3** per microservice on Pot. Multiple local sessions should not block each other after v1.0.0-rc.6.

4. **Orphan containerd exec** (embedded engine): if a prior exec crashed without cleanup, retry after the process manager orphan sweep or restart the microservice:

   ```bash
   edgelet ms restart <uuid>
   ```

5. **Controller exec only:** verify agent connectivity and that Controller **v3.8.x** (multi-exec sessions) is deployed. Status should list active controller session ids:

   ```bash
   edgelet system status -o json | jq '.connectionToController'
   ```

6. **`execEnabled`:** edgelet no longer gates exec on this flag — session poll drives controller exec. Do not expect toggling `execEnabled` to fix attach issues.

---

## Collecting diagnostics

```bash
sudo journalctl -u edgelet --since "1 hour ago" > edgelet-journal.log
edgelet system status -o json > edgelet-status.json
edgelet system info -o json > edgelet-info.json
edgelet system version -o json > edgelet-version.json
```

Do not share `/etc/edgelet/edgelet-api` or private keys in support tickets.
