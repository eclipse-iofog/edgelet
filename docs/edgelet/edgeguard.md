# Edge Guard (hardware attestation)

Edge Guard periodically fingerprints the host hardware and compares it to a baseline stored in SQLite. A mismatch triggers a controller warning, agent deprovision, and baseline reset.

**Implementation:** `internal/edgeguard/`  
**Persistence:** `agent_edgeguard_signature` table — see [persistence.md](persistence.md)

---

## Configuration

| Key | Profile / YAML | Meaning |
|-----|----------------|---------|
| `edgeGuardFrequency` | seconds | Attestation interval; **`0` disables** Edge Guard |

Example:

```yaml
profiles:
  production:
    edgeGuardFrequency: 3600
```

Edge Guard requires a **provisioned** agent (`iofogUuid` + private key in SQLite). If unprovisioned, runtime forces `edgeGuardFrequency` to **`0`**.

---

## How attestation works

1. Collect a **stable hardware fingerprint** (platform-specific; see below).
2. Canonicalize JSON and compute a **SHA-256 hash** (base64).
3. Sign the hash into an Edge Guard **JWT** (`hash` claim + standard time claims).
4. On first run, store the JWT in `agent_edgeguard_signature`.
5. On later runs, compare the **new hash** to the **`hash` claim** in the stored JWT.
6. If hashes match, optionally refresh the JWT (time claims rotate); **no deprovision**.
7. If hashes differ, treat as hardware change (see [On mismatch](#on-mismatch)).

**Important:** Comparison uses the stable **`hash` claim**, not the full JWT string. Each signing produces a new JWT (`iat`, `exp`, `jti`) even when hardware is unchanged.

---

## Fingerprint sources (linux)

Collected in `internal/edgeguard/fingerprint_linux.go`:

| Category | Examples |
|----------|----------|
| System / DMI | product UUID, serial, vendor, model |
| BIOS | vendor, version |
| CPU | vendor, model, core count (gopsutil) |
| PCI devices | slot, class, vendor/device IDs |
| Root disk | device id |
| Primary NICs | name, MAC, link state |
| USB devices | bus path, vendor/product id |
| Optional | machine-id, TPM, secure boot, memory modules |

Darwin and Windows use platform-specific collectors (`fingerprint_darwin.go`, `fingerprint_windows.go`).

Link state, included interface names, and which field changes deprovision the agent are described in [What counts as a hardware change](modules/edgeguard.md#what-counts-as-a-hardware-change).

---

## On mismatch

When the fingerprint hash changes:

1. Supervisor status → **WARNING**, `warningMessage`: **`HW signature changed`**
2. Status POST to controller (immediate)
3. Agent **deprovision** (`Deprovision(false)` — credentials cleared via normal deprovision path)
4. Stored baseline JWT **deleted**

Operators must investigate physical changes before reprovisioning. A NIC link-state change (`1`, `0`, or `unknown`), USB plug or unplug, disk change, or VM identity drift is enough to mismatch. See [What counts as a hardware change](modules/edgeguard.md#what-counts-as-a-hardware-change).

---

## On reprovision

When provisioning succeeds:

- Edge Guard baseline row is **deleted** (new private key / identity)
- Supervisor **warning message cleared** (`""`) and daemon status restored from WARNING → RUNNING when applicable
- Next attestation cycle creates a **new baseline** if `edgeGuardFrequency > 0`

Ensure status reaches the controller after reprovision so the dashboard clears the warning.

---

## Enable / disable

| Action | Behavior |
|--------|----------|
| Set frequency **> 0** (provisioned) | Start ticker; establish baseline on first check |
| Set frequency **0** | Stop ticker; **delete** baseline from SQLite |
| Deprovision | Frequency forced to **0**; delete baseline + credentials |
| Config change **0 → N** | Reset baseline; new baseline on first check |

---

## Development and VMs

- Use a **reasonable interval** in production (e.g. 3600s). Very short intervals (e.g. 10s) are for testing only.
- **Lima / Apple Virtualization:** virtual USB and PCI devices are part of the fingerprint; stable across reboots if the VM definition is unchanged.
- Embedded IT may set `edgeGuardFrequency: 0` when attestation is not under test.

---

## Validation checklist

After deploy or Edge Guard changes:

1. Provision agent; confirm `agent_credentials` row (`id=1`) with non-empty `private_key_b64`.
2. Set `edgeGuardFrequency > 0`; confirm `agent_edgeguard_signature` row after first attestation.
3. Wait **two** attestation intervals — agent must **remain provisioned** if hardware unchanged.
4. Set `edgeGuardFrequency: 0`; confirm signature row deleted.
5. Unprovisioned agent + `edgeGuardFrequency > 0` → runtime normalizes to **0**.
6. Reprovision after mismatch → warning cleared in local status; controller receives empty `warningMessage` on next status POST.
7. Deprovision → frequency **0**, `agent_credentials` and `agent_edgeguard_signature` empty.

---

## Related

- [deployment.md](deployment.md) — provisioning
- [persistence.md](persistence.md) — SQLite tables
- [troubleshooting.md](troubleshooting.md) — daemon and controller connectivity
