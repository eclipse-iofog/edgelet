package control

import (
	"net"
	"os"
	"strings"
)

const (
	hostResolvPath    = "/etc/resolv.conf"
	systemdResolvPath = "/run/systemd/resolve/resolv.conf"
)

// hostResolvConf is the resolver file kubelet copies into pods.
// An explicit path is kept. Otherwise a file whose nameservers are only
// loopback addresses is the systemd stub, and CoreDNS would forward to
// itself, so the upstream file is used when it names a real resolver.
func hostResolvConf(configured string) string {
	return chooseResolvConf(configured, hostResolvPath, systemdResolvPath)
}

func chooseResolvConf(configured, host, upstream string) string {
	if configured != "" {
		return configured
	}
	if nameserversAreLoopback(host) && hasNonLoopbackNameserver(upstream) {
		return upstream
	}
	return host
}

func nameserversAreLoopback(path string) bool {
	servers, ok := nameservers(path)
	if !ok || len(servers) == 0 {
		return false
	}
	for _, ip := range servers {
		if !ip.IsLoopback() {
			return false
		}
	}
	return true
}

func hasNonLoopbackNameserver(path string) bool {
	servers, ok := nameservers(path)
	if !ok {
		return false
	}
	for _, ip := range servers {
		if !ip.IsLoopback() {
			return true
		}
	}
	return false
}

func nameservers(path string) ([]net.IP, bool) {
	body, err := os.ReadFile(path) // #nosec G304 -- path is the host or configured resolv.conf
	if err != nil {
		return nil, false
	}
	var servers []net.IP
	for _, line := range strings.Split(string(body), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 || fields[0] != "nameserver" {
			continue
		}
		ip := net.ParseIP(fields[1])
		if ip == nil {
			continue
		}
		servers = append(servers, ip)
	}
	return servers, true
}
