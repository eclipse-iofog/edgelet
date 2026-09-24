# Resource Manager

Host hardware and USB inventory posting to the controller has been **removed**. There is no Resource Manager module, no `hal/hw` / `hal/usb` client, and no `deviceScanFrequency` config key.

**Edge Guard is not this module.** Host fingerprint / deprovision attestation remains — see [../edgeguard.md](../edgeguard.md) and [edgeguard.md](edgeguard.md).

Host CPU / memory / disk sampling is **Resource Consumption**, not Resource Manager — see [resourceconsumption.md](resourceconsumption.md).

The StatusReporter module index slot that used to map to Resource Manager is unused (indexes are not reshuffled).
