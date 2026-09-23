# Controller handoff — host status (capacity + OS)

For the **Pot / Datasance Controller** team. Additive keys on agent **`PUT status`** (fog status). Same keys appear on Edgelet local **`GET /v1/system/status`**. Older controllers must **ignore unknown fields** and must **not** fail `PUT status`.

Agent release: **`v1.1.0-rc.5`** (Wave 2). No new `getChanges` flags. No new REST routes.

---

## Host capacity (bytes and CPU count)

| Key | Type | Unit / semantics |
|-----|------|------------------|
| `systemCpus` | integer | Logical CPU count |
| `systemTotalMemory` | number | Host RAM **capacity** (bytes) |
| `systemAvailableMemory` | number | Host RAM **available** (bytes) |
| `systemTotalDisk` | number | Filesystem size for agent `diskDirectory` (bytes) |
| `systemAvailableDisk` | number | Free space on that filesystem (bytes) |
| `systemTotalCpu` | number | Host CPU **busy** **0–100%** (not core count) |

`diskUsage` on status remains **Edgelet data directory usage in GiB**, not host total disk.

---

## Host OS identity

Sampled **once** when the resource consumption manager starts. Values are stable until the agent process restarts.

| Key | Type | Semantics |
|-----|------|-----------|
| `systemOs` | string | OS **family**: `linux`, `darwin`, or `windows` (agent `GOOS`) |
| `systemOsVersion` | string | Human display string: Linux **`PRETTY_NAME`** from `/etc/os-release` when present, else gopsutil platform + version (e.g. `Ubuntu 24.04`). May be `""` if unknown |
| `systemKernelVersion` | string | Linux kernel release from gopsutil. **`""` on non-Linux** (key still present in JSON) |

Informational only — not Edge Guard attestation and not HAL hardware/USB inventory.

### Example (Linux / Ubuntu)

```json
{
  "systemCpus": 2,
  "systemOs": "linux",
  "systemOsVersion": "Ubuntu 24.04.4 LTS",
  "systemKernelVersion": "6.8.0-45-generic",
  "systemTotalMemory": 2050000000,
  "systemTotalCpu": 1.99
}
```

### Example (macOS dev)

```json
{
  "systemOs": "darwin",
  "systemOsVersion": "14.6.1",
  "systemKernelVersion": ""
}
```

---

## Controller checklist

- [ ] Persist and display host capacity keys (`systemCpus`, memory/disk bytes, `systemTotalCpu`).
- [ ] Persist and display `systemOs`, `systemOsVersion`, `systemKernelVersion`.
- [ ] Filter “Linux fleet” with `systemOs == "linux"`; use `systemOsVersion` for distro/release display.
- [ ] Treat `systemKernelVersion: ""` as N/A on non-Linux.
- [ ] Do **not** reject `PUT status` when these keys are present.
- [ ] **`cpuLimit`**: allow **5–400** (default **80**); stack CPU in **cores×100**, same unit as `cpuUsage`.

---

## Related docs

- [modules/resourceconsumption.md](modules/resourceconsumption.md)
- [../cli/output-schemas.md](../cli/output-schemas.md)
- [CONTROLLER-HANDOFF-MODELS.md](CONTROLLER-HANDOFF-MODELS.md) (broader fog status)
