package edgeguard

import "testing"

func TestCanonicalizationDeterministicOrdering(t *testing.T) {
	payloadA := FingerprintPayload{
		SchemaVersion: 1,
		System: SystemIdentity{
			UUID: "UUID-A",
		},
		PCIDevices: []PCIDeviceIdentity{
			{Slot: "0000:00:0b.0", VendorID: "1AF4", DeviceID: "1059", Class: "0x040100"},
			{Slot: "0000:00:0a.0", VendorID: "1AF4", DeviceID: "1059", Class: "0x030000"},
		},
		PrimaryNICs: []NICIdentity{
			{Name: "eth1", MAC: "aa:bb:cc:dd:ee:ff", LinkState: "1"},
			{Name: "eth0", MAC: "11:22:33:44:55:66", LinkState: "1"},
		},
		USBDevices: []USBDeviceIdentity{
			{BusPath: "1-2", VendorID: "05ac", ProductID: "8106"},
			{BusPath: "1-1", VendorID: "05ac", ProductID: "8105"},
		},
	}
	payloadB := FingerprintPayload{
		SchemaVersion: 1,
		System: SystemIdentity{
			UUID: "uuid-a", // case/whitespace normalization
		},
		PCIDevices: []PCIDeviceIdentity{
			{Slot: "0000:00:0a.0", VendorID: "1af4", DeviceID: "1059", Class: "0x030000"},
			{Slot: "0000:00:0b.0", VendorID: "1af4", DeviceID: "1059", Class: "0x040100"},
		},
		PrimaryNICs: []NICIdentity{
			{Name: "ETH0", MAC: "11:22:33:44:55:66", LinkState: "1"},
			{Name: "ETH1", MAC: "AA:BB:CC:DD:EE:FF", LinkState: "1"},
		},
		USBDevices: []USBDeviceIdentity{
			{BusPath: "1-1", VendorID: "05AC", ProductID: "8105"},
			{BusPath: "1-2", VendorID: "05AC", ProductID: "8106"},
		},
	}

	canonA, err := canonicalizeFingerprintPayload(payloadA)
	if err != nil {
		t.Fatalf("canonicalize payload A: %v", err)
	}
	canonB, err := canonicalizeFingerprintPayload(payloadB)
	if err != nil {
		t.Fatalf("canonicalize payload B: %v", err)
	}
	if canonA != canonB {
		t.Fatalf("expected canonical payloads to match\nA: %s\nB: %s", canonA, canonB)
	}
}

func TestDeriveGPUDevicesFromPCI(t *testing.T) {
	pci := []PCIDeviceIdentity{
		{Slot: "0000:00:01.0", Class: "0x020000", VendorID: "8086", DeviceID: "15f3"},
		{Slot: "0000:01:00.0", Class: "0x030000", VendorID: "10de", DeviceID: "1eb8"},
	}
	gpus := deriveGPUDevices(pci)
	if len(gpus) != 1 {
		t.Fatalf("expected 1 gpu, got %d", len(gpus))
	}
	if gpus[0].Slot != "0000:01:00.0" {
		t.Fatalf("unexpected gpu slot: %s", gpus[0].Slot)
	}
}

func TestOptionalSignalsMissingVsPresent(t *testing.T) {
	base := FingerprintPayload{
		System:      SystemIdentity{UUID: "uuid"},
		RootDisk:    DiskIdentity{DeviceID: "/dev/sda"},
		PrimaryNICs: []NICIdentity{{Name: "eth0", MAC: "aa:bb:cc:dd:ee:ff", LinkState: "1"}},
	}
	withOptional := base
	withOptional.Optional = OptionalFingerprintSignals{
		Firmware: FirmwareIdentity{SecureBootState: "enabled"},
		TPM:      TPMIdentity{Present: true, EKFingerprint: "abc"},
	}

	canonBase, err := canonicalizeFingerprintPayload(base)
	if err != nil {
		t.Fatalf("canonicalize base: %v", err)
	}
	canonOptional, err := canonicalizeFingerprintPayload(withOptional)
	if err != nil {
		t.Fatalf("canonicalize optional: %v", err)
	}
	if canonBase == canonOptional {
		t.Fatal("expected payloads to differ when optional enforced-if-present fields are present")
	}
}

func TestTamperSensitiveFieldsChangeCanonicalFingerprint(t *testing.T) {
	base := FingerprintPayload{
		System:      SystemIdentity{UUID: "uuid"},
		RootDisk:    DiskIdentity{DeviceID: "/dev/sda", Serial: "disk-1"},
		PrimaryNICs: []NICIdentity{{Name: "eth0", MAC: "aa:bb:cc:dd:ee:ff", LinkState: "1"}},
		USBDevices:  []USBDeviceIdentity{{BusPath: "1-1", VendorID: "05ac", ProductID: "8105"}},
	}
	nicTampered := base
	nicTampered.PrimaryNICs = []NICIdentity{{Name: "eth0", MAC: "aa:bb:cc:dd:ee:ff", LinkState: "0"}}
	usbTampered := base
	usbTampered.USBDevices = []USBDeviceIdentity{{BusPath: "1-1", VendorID: "05ac", ProductID: "8106"}}

	baseCanonical, err := canonicalizeFingerprintPayload(base)
	if err != nil {
		t.Fatalf("canonicalize base: %v", err)
	}
	nicCanonical, err := canonicalizeFingerprintPayload(nicTampered)
	if err != nil {
		t.Fatalf("canonicalize nic tampered: %v", err)
	}
	usbCanonical, err := canonicalizeFingerprintPayload(usbTampered)
	if err != nil {
		t.Fatalf("canonicalize usb tampered: %v", err)
	}
	if baseCanonical == nicCanonical {
		t.Fatal("expected NIC link-state change to alter the full canonical fingerprint")
	}
	if baseCanonical == usbCanonical {
		t.Fatal("expected USB identity change to alter canonical fingerprint")
	}

	baseIdentity, err := canonicalizeIdentityFingerprint(base)
	if err != nil {
		t.Fatalf("identity base: %v", err)
	}
	nicIdentity, err := canonicalizeIdentityFingerprint(nicTampered)
	if err != nil {
		t.Fatalf("identity nic: %v", err)
	}
	usbIdentity, err := canonicalizeIdentityFingerprint(usbTampered)
	if err != nil {
		t.Fatalf("identity usb: %v", err)
	}
	if baseIdentity != nicIdentity {
		t.Fatal("expected NIC link-state change to leave the identity fingerprint unchanged")
	}
	if baseIdentity == usbIdentity {
		t.Fatal("expected USB identity change to alter the identity fingerprint")
	}
}

func TestIdentityFingerprintIgnoresPCRAndKeepsEK(t *testing.T) {
	base := FingerprintPayload{
		System: SystemIdentity{UUID: "uuid"},
		Optional: OptionalFingerprintSignals{
			TPM: TPMIdentity{Present: true, EKFingerprint: "ek-1", PCRPolicyHash: "pcr-a"},
		},
	}
	pcrChanged := base
	pcrChanged.Optional.TPM.PCRPolicyHash = "pcr-b"
	ekChanged := base
	ekChanged.Optional.TPM.EKFingerprint = "ek-2"
	macChanged := base
	macChanged.PrimaryNICs = []NICIdentity{{Name: "eth0", MAC: "11:22:33:44:55:66", LinkState: "1"}}

	baseIdentity, err := canonicalizeIdentityFingerprint(base)
	if err != nil {
		t.Fatalf("identity base: %v", err)
	}
	pcrIdentity, err := canonicalizeIdentityFingerprint(pcrChanged)
	if err != nil {
		t.Fatalf("identity pcr: %v", err)
	}
	ekIdentity, err := canonicalizeIdentityFingerprint(ekChanged)
	if err != nil {
		t.Fatalf("identity ek: %v", err)
	}
	macIdentity, err := canonicalizeIdentityFingerprint(macChanged)
	if err != nil {
		t.Fatalf("identity mac: %v", err)
	}
	if baseIdentity != pcrIdentity {
		t.Fatal("expected PCR file hash change to leave the identity fingerprint unchanged")
	}
	if baseIdentity == ekIdentity {
		t.Fatal("expected EK certificate change to alter the identity fingerprint")
	}
	if baseIdentity == macIdentity {
		t.Fatal("expected NIC MAC change to alter the identity fingerprint")
	}
}
