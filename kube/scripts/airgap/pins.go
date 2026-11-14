package airgap

import (
	"bufio"
	_ "embed"
	"strings"
)

//go:embed pins.txt
var pinFile string

// KubernetesVersion is the Kubernetes patch this binary reports.
func KubernetesVersion() string {
	return pin("kubernetes")
}

// ChartVersion is the pinned chart version for name.
// name is traefik, traefik-crd, gateway-api-crd, or cert-manager.
func ChartVersion(name string) string {
	return pin(name)
}

func pin(name string) string {
	scanner := bufio.NewScanner(strings.NewReader(pinFile))
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) == 2 && fields[0] == name {
			return fields[1]
		}
	}
	return ""
}
