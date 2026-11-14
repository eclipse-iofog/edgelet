// Package network writes the node bridge conflist.
// Write does not contact the apiserver. It finishes before kubelet creates a sandbox.
package network

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
)

// FileName is the only conflist in the CNI configuration directory.
const FileName = "10-bridge.conflist"

type conflist struct {
	CNIVersion string   `json:"cniVersion"`
	Name       string   `json:"name"`
	Plugins    []plugin `json:"plugins"`
}

type plugin struct {
	Type         string          `json:"type"`
	Bridge       string          `json:"bridge,omitempty"`
	IsGateway    bool            `json:"isGateway,omitempty"`
	IPMasq       bool            `json:"ipMasq,omitempty"`
	HairpinMode  bool            `json:"hairpinMode,omitempty"`
	IPAM         *ipam           `json:"ipam,omitempty"`
	Capabilities map[string]bool `json:"capabilities,omitempty"`
}

type ipam struct {
	Type   string     `json:"type"`
	Ranges [][]subnet `json:"ranges"`
	Routes []route    `json:"routes,omitempty"`
}

type subnet struct {
	Subnet string `json:"subnet"`
}

type route struct {
	Dst string `json:"dst"`
}

// Write writes one bridge conflist into dir.
// podCIDR is the node pod range. Hairpin stays on. The write does not contact the apiserver.
func Write(dir, podCIDR string) error {
	if _, _, err := net.ParseCIDR(podCIDR); err != nil {
		return fmt.Errorf("node pod network %q is not a network", podCIDR)
	}
	body, err := json.Marshal(conflist{
		CNIVersion: "1.0.0",
		Name:       "bridge",
		Plugins: []plugin{
			{
				Type:        "bridge",
				Bridge:      "cni0",
				IsGateway:   true,
				IPMasq:      true,
				HairpinMode: true,
				IPAM: &ipam{
					Type: "host-local",
					Ranges: [][]subnet{
						{{Subnet: podCIDR}},
					},
					Routes: []route{{Dst: "0.0.0.0/0"}},
				},
			},
			{
				Type:         "portmap",
				Capabilities: map[string]bool{"portMappings": true},
			},
		},
	})
	if err != nil {
		return err
	}
	body = append(body, '\n')
	if err := os.MkdirAll(dir, 0o755); err != nil { // #nosec G301 -- CNI reads this config directory
		return fmt.Errorf("create cni config directory: %w", err)
	}
	path := filepath.Join(dir, FileName)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, body, 0o644); err != nil { // #nosec G306 -- bridge conflist is not secret
		return fmt.Errorf("write cni config: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("write cni config: %w", err)
	}
	return nil
}
