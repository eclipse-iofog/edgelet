package edgeguard

import (
	"cmp"
	"encoding/json"
	"slices"
	"strings"
)

// FingerprintPayload is the canonical normalized structure used for hashing/signing.
type FingerprintPayload struct {
	SchemaVersion   int                        `json:"schemaVersion"`
	System          SystemIdentity             `json:"system"`
	BIOS            BIOSIdentity               `json:"bios"`
	Motherboard     MotherboardIdentity        `json:"motherboard"`
	CPU             CPUIdentity                `json:"cpu"`
	PCIDevices      []PCIDeviceIdentity        `json:"pciDevices"`
	GPUDevices      []GPUDeviceIdentity        `json:"gpuDevices,omitempty"` // derived from PCIDevices
	PlatformDevices []PlatformDeviceIdentity   `json:"platformDevices,omitempty"`
	RootDisk        DiskIdentity               `json:"rootDisk"`
	PrimaryNICs     []NICIdentity              `json:"primaryNICs"`
	USBDevices      []USBDeviceIdentity        `json:"usbDevices"`
	Optional        OptionalFingerprintSignals `json:"optional,omitempty"`
}

type SystemIdentity struct {
	UUID         string `json:"uuid,omitempty"`
	Serial       string `json:"serial,omitempty"`
	Manufacturer string `json:"manufacturer,omitempty"`
	Model        string `json:"model,omitempty"`
}

type BIOSIdentity struct {
	Manufacturer string `json:"manufacturer,omitempty"`
	Version      string `json:"version,omitempty"`
}

type MotherboardIdentity struct {
	Product string `json:"product,omitempty"`
	Serial  string `json:"serial,omitempty"`
}

type CPUIdentity struct {
	Vendor        string `json:"vendor,omitempty"`
	ModelName     string `json:"modelName,omitempty"`
	Family        string `json:"family,omitempty"`
	Model         string `json:"model,omitempty"`
	Stepping      int32  `json:"stepping,omitempty"`
	PhysicalCores int    `json:"physicalCores,omitempty"`
}

type PCIDeviceIdentity struct {
	Slot            string `json:"slot,omitempty"`
	Class           string `json:"class,omitempty"`
	VendorID        string `json:"vendorId,omitempty"`
	DeviceID        string `json:"deviceId,omitempty"`
	SubsystemVendor string `json:"subsystemVendor,omitempty"`
	SubsystemDevice string `json:"subsystemDevice,omitempty"`
}

type GPUDeviceIdentity struct {
	Slot     string `json:"slot,omitempty"`
	VendorID string `json:"vendorId,omitempty"`
	DeviceID string `json:"deviceId,omitempty"`
	Class    string `json:"class,omitempty"`
}

type PlatformDeviceIdentity struct {
	Type  string `json:"type,omitempty"`
	Name  string `json:"name,omitempty"`
	Value string `json:"value,omitempty"`
}

type DiskIdentity struct {
	DeviceID string `json:"deviceId,omitempty"`
	Serial   string `json:"serial,omitempty"`
	WWN      string `json:"wwn,omitempty"`
	Model    string `json:"model,omitempty"`
}

type NICIdentity struct {
	Name      string `json:"name,omitempty"`
	MAC       string `json:"mac,omitempty"`
	LinkState string `json:"linkState,omitempty"`
}

type USBDeviceIdentity struct {
	BusPath      string `json:"busPath,omitempty"`
	VendorID     string `json:"vendorId,omitempty"`
	ProductID    string `json:"productId,omitempty"`
	Manufacturer string `json:"manufacturer,omitempty"`
	Product      string `json:"product,omitempty"`
	Serial       string `json:"serial,omitempty"`
}

type OptionalFingerprintSignals struct {
	MemoryModules []MemoryModuleIdentity `json:"memoryModules,omitempty"`
	Firmware      FirmwareIdentity       `json:"firmware,omitempty"`
	TPM           TPMIdentity            `json:"tpm,omitempty"`
	DMIBoardUUID  string                 `json:"dmiBoardUuid,omitempty"`
	MachineID     string                 `json:"machineId,omitempty"`
}

type MemoryModuleIdentity struct {
	Locator    string `json:"locator,omitempty"`
	Serial     string `json:"serial,omitempty"`
	PartNumber string `json:"partNumber,omitempty"`
}

type FirmwareIdentity struct {
	SecureBootState string `json:"secureBootState,omitempty"`
}

type TPMIdentity struct {
	Present       bool   `json:"present,omitempty"`
	EKFingerprint string `json:"ekFingerprint,omitempty"`
	PCRPolicyHash string `json:"pcrPolicyHash,omitempty"`
}

func normalizeString(v string) string {
	return strings.ToLower(strings.TrimSpace(v))
}

func normalizeFingerprintPayload(payload FingerprintPayload) FingerprintPayload {
	payload.SchemaVersion = 1

	payload.System.UUID = normalizeString(payload.System.UUID)
	payload.System.Serial = normalizeString(payload.System.Serial)
	payload.System.Manufacturer = normalizeString(payload.System.Manufacturer)
	payload.System.Model = normalizeString(payload.System.Model)

	payload.BIOS.Manufacturer = normalizeString(payload.BIOS.Manufacturer)
	payload.BIOS.Version = normalizeString(payload.BIOS.Version)

	payload.Motherboard.Product = normalizeString(payload.Motherboard.Product)
	payload.Motherboard.Serial = normalizeString(payload.Motherboard.Serial)

	payload.CPU.Vendor = normalizeString(payload.CPU.Vendor)
	payload.CPU.ModelName = normalizeString(payload.CPU.ModelName)
	payload.CPU.Family = normalizeString(payload.CPU.Family)
	payload.CPU.Model = normalizeString(payload.CPU.Model)

	for i := range payload.PCIDevices {
		payload.PCIDevices[i].Slot = normalizeString(payload.PCIDevices[i].Slot)
		payload.PCIDevices[i].Class = normalizeString(payload.PCIDevices[i].Class)
		payload.PCIDevices[i].VendorID = normalizeString(payload.PCIDevices[i].VendorID)
		payload.PCIDevices[i].DeviceID = normalizeString(payload.PCIDevices[i].DeviceID)
		payload.PCIDevices[i].SubsystemVendor = normalizeString(payload.PCIDevices[i].SubsystemVendor)
		payload.PCIDevices[i].SubsystemDevice = normalizeString(payload.PCIDevices[i].SubsystemDevice)
	}
	slices.SortFunc(payload.PCIDevices, func(a, b PCIDeviceIdentity) int {
		return cmp.Compare(a.Slot+a.VendorID+a.DeviceID, b.Slot+b.VendorID+b.DeviceID)
	})
	payload.PCIDevices = dedupePCIDevices(payload.PCIDevices)

	for i := range payload.GPUDevices {
		payload.GPUDevices[i].Slot = normalizeString(payload.GPUDevices[i].Slot)
		payload.GPUDevices[i].VendorID = normalizeString(payload.GPUDevices[i].VendorID)
		payload.GPUDevices[i].DeviceID = normalizeString(payload.GPUDevices[i].DeviceID)
		payload.GPUDevices[i].Class = normalizeString(payload.GPUDevices[i].Class)
	}
	slices.SortFunc(payload.GPUDevices, func(a, b GPUDeviceIdentity) int {
		return cmp.Compare(a.Slot+a.VendorID+a.DeviceID, b.Slot+b.VendorID+b.DeviceID)
	})

	for i := range payload.PlatformDevices {
		payload.PlatformDevices[i].Type = normalizeString(payload.PlatformDevices[i].Type)
		payload.PlatformDevices[i].Name = normalizeString(payload.PlatformDevices[i].Name)
		payload.PlatformDevices[i].Value = normalizeString(payload.PlatformDevices[i].Value)
	}
	slices.SortFunc(payload.PlatformDevices, func(a, b PlatformDeviceIdentity) int {
		return cmp.Compare(a.Type+a.Name+a.Value, b.Type+b.Name+b.Value)
	})

	payload.RootDisk.DeviceID = normalizeString(payload.RootDisk.DeviceID)
	payload.RootDisk.Serial = normalizeString(payload.RootDisk.Serial)
	payload.RootDisk.WWN = normalizeString(payload.RootDisk.WWN)
	payload.RootDisk.Model = normalizeString(payload.RootDisk.Model)

	for i := range payload.PrimaryNICs {
		payload.PrimaryNICs[i].Name = normalizeString(payload.PrimaryNICs[i].Name)
		payload.PrimaryNICs[i].MAC = normalizeString(payload.PrimaryNICs[i].MAC)
		payload.PrimaryNICs[i].LinkState = normalizeString(payload.PrimaryNICs[i].LinkState)
	}
	slices.SortFunc(payload.PrimaryNICs, func(a, b NICIdentity) int {
		return cmp.Compare(a.MAC+a.Name, b.MAC+b.Name)
	})
	payload.PrimaryNICs = dedupeNICs(payload.PrimaryNICs)

	for i := range payload.USBDevices {
		payload.USBDevices[i].BusPath = normalizeString(payload.USBDevices[i].BusPath)
		payload.USBDevices[i].VendorID = normalizeString(payload.USBDevices[i].VendorID)
		payload.USBDevices[i].ProductID = normalizeString(payload.USBDevices[i].ProductID)
		payload.USBDevices[i].Manufacturer = normalizeString(payload.USBDevices[i].Manufacturer)
		payload.USBDevices[i].Product = normalizeString(payload.USBDevices[i].Product)
		payload.USBDevices[i].Serial = normalizeString(payload.USBDevices[i].Serial)
	}
	slices.SortFunc(payload.USBDevices, func(a, b USBDeviceIdentity) int {
		return cmp.Compare(a.BusPath+a.VendorID+a.ProductID, b.BusPath+b.VendorID+b.ProductID)
	})
	payload.USBDevices = dedupeUSBDevices(payload.USBDevices)

	payload.Optional.DMIBoardUUID = normalizeString(payload.Optional.DMIBoardUUID)
	payload.Optional.MachineID = normalizeString(payload.Optional.MachineID)
	payload.Optional.Firmware.SecureBootState = normalizeString(payload.Optional.Firmware.SecureBootState)
	payload.Optional.TPM.EKFingerprint = normalizeString(payload.Optional.TPM.EKFingerprint)
	payload.Optional.TPM.PCRPolicyHash = normalizeString(payload.Optional.TPM.PCRPolicyHash)

	for i := range payload.Optional.MemoryModules {
		payload.Optional.MemoryModules[i].Locator = normalizeString(payload.Optional.MemoryModules[i].Locator)
		payload.Optional.MemoryModules[i].Serial = normalizeString(payload.Optional.MemoryModules[i].Serial)
		payload.Optional.MemoryModules[i].PartNumber = normalizeString(payload.Optional.MemoryModules[i].PartNumber)
	}
	slices.SortFunc(payload.Optional.MemoryModules, func(a, b MemoryModuleIdentity) int {
		return cmp.Compare(a.Locator+a.Serial+a.PartNumber, b.Locator+b.Serial+b.PartNumber)
	})
	payload.Optional.MemoryModules = dedupeMemoryModules(payload.Optional.MemoryModules)

	return payload
}

func canonicalizeFingerprintPayload(payload FingerprintPayload) (string, error) {
	normalized := normalizeFingerprintPayload(payload)
	data, err := json.Marshal(normalized)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// canonicalizeIdentityFingerprint hashes the fingerprint without live carrier
// or the TPM PCR file. Those values change when the far end drops or the
// platform reboots, and they are not a hardware identity change.
func canonicalizeIdentityFingerprint(payload FingerprintPayload) (string, error) {
	identity := payload
	if len(payload.PrimaryNICs) > 0 {
		nics := make([]NICIdentity, len(payload.PrimaryNICs))
		copy(nics, payload.PrimaryNICs)
		for i := range nics {
			nics[i].LinkState = ""
		}
		identity.PrimaryNICs = nics
	}
	identity.Optional.TPM.PCRPolicyHash = ""
	return canonicalizeFingerprintPayload(identity)
}

func deriveGPUDevices(pciDevices []PCIDeviceIdentity) []GPUDeviceIdentity {
	gpus := make([]GPUDeviceIdentity, 0)
	for _, dev := range pciDevices {
		class := normalizeString(dev.Class)
		if strings.HasPrefix(class, "0x03") || strings.HasPrefix(class, "03") {
			gpus = append(gpus, GPUDeviceIdentity{
				Slot:     dev.Slot,
				VendorID: dev.VendorID,
				DeviceID: dev.DeviceID,
				Class:    dev.Class,
			})
		}
	}
	return gpus
}

func dedupePCIDevices(items []PCIDeviceIdentity) []PCIDeviceIdentity {
	seen := make(map[string]struct{}, len(items))
	out := make([]PCIDeviceIdentity, 0, len(items))
	for _, item := range items {
		key := item.Slot + "|" + item.Class + "|" + item.VendorID + "|" + item.DeviceID + "|" + item.SubsystemVendor + "|" + item.SubsystemDevice
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, item)
	}
	return out
}

func dedupeNICs(items []NICIdentity) []NICIdentity {
	seen := make(map[string]struct{}, len(items))
	out := make([]NICIdentity, 0, len(items))
	for _, item := range items {
		key := item.Name + "|" + item.MAC + "|" + item.LinkState
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, item)
	}
	return out
}

func dedupeUSBDevices(items []USBDeviceIdentity) []USBDeviceIdentity {
	seen := make(map[string]struct{}, len(items))
	out := make([]USBDeviceIdentity, 0, len(items))
	for _, item := range items {
		key := item.BusPath + "|" + item.VendorID + "|" + item.ProductID + "|" + item.Serial
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, item)
	}
	return out
}

func dedupeMemoryModules(items []MemoryModuleIdentity) []MemoryModuleIdentity {
	seen := make(map[string]struct{}, len(items))
	out := make([]MemoryModuleIdentity, 0, len(items))
	for _, item := range items {
		key := item.Locator + "|" + item.Serial + "|" + item.PartNumber
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, item)
	}
	return out
}
