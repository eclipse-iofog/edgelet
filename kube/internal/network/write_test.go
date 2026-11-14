package network

import (
	"bytes"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/containernetworking/plugins/plugins/ipam/host-local/backend/allocator"
	"github.com/containernetworking/plugins/plugins/ipam/host-local/backend/disk"
)

func TestSandboxAddressInNodePodCIDR(t *testing.T) {
	dir := t.TempDir()
	const podCIDR = "10.20.0.0/24"
	if err := Write(dir, podCIDR); err != nil {
		t.Fatal(err)
	}
	if err := Write(dir, podCIDR); err != nil {
		t.Fatal(err)
	}
	files := conflistFiles(t, dir)
	if len(files) != 1 || files[0] != FileName {
		t.Fatalf("conflists %v", files)
	}

	body, err := os.ReadFile(filepath.Join(dir, FileName))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(body, []byte(`"hairpinMode":true`)) {
		t.Fatalf("hairpinMode %s", body)
	}
	if bytes.Contains(body, []byte("flannel")) {
		t.Fatalf("second network %s", body)
	}
	var doc conflist
	if err := json.Unmarshal(body, &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Plugins) != 2 {
		t.Fatalf("plugins %d", len(doc.Plugins))
	}
	bridge := doc.Plugins[0]
	if bridge.Type != "bridge" || !bridge.IsGateway || !bridge.IPMasq || !bridge.HairpinMode {
		t.Fatalf("bridge %+v", bridge)
	}
	if bridge.IPAM == nil || bridge.IPAM.Type != "host-local" || len(bridge.IPAM.Ranges) != 1 || len(bridge.IPAM.Ranges[0]) != 1 {
		t.Fatalf("ipam %+v", bridge.IPAM)
	}
	if bridge.IPAM.Ranges[0][0].Subnet != podCIDR {
		t.Fatalf("range %s", bridge.IPAM.Ranges[0][0].Subnet)
	}
	portmap := doc.Plugins[1]
	if portmap.Type != "portmap" || !portmap.Capabilities["portMappings"] {
		t.Fatalf("portmap %+v", portmap)
	}

	_, podNet, err := net.ParseCIDR(podCIDR)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(bridge)
	if err != nil {
		t.Fatal(err)
	}
	ipam, _, err := allocator.LoadIPAMConfig(raw, "")
	if err != nil {
		t.Fatal(err)
	}
	store, err := disk.New(doc.Name, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	assigned, err := allocator.NewIPAllocator(&ipam.Ranges[0], store, 0).Get("sandbox", "eth0", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !podNet.Contains(assigned.Address.IP) {
		t.Fatalf("sandbox %s outside %s", assigned.Address.IP, podCIDR)
	}
}

func TestWriteRejectsPodNetwork(t *testing.T) {
	if err := Write(t.TempDir(), "not-a-network"); err == nil {
		t.Fatal("accepted a pod network that is not a CIDR")
	}
}

func conflistFiles(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasSuffix(name, ".conflist") || strings.HasSuffix(name, ".conf") {
			names = append(names, name)
		}
	}
	slices.Sort(names)
	return names
}
