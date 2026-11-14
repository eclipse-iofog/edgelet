package control

import (
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"time"

	"github.com/eclipse-iofog/edgelet-kube/internal/config"
	utilnet "k8s.io/apimachinery/pkg/util/net"
)

// hostIPs lists addresses on this host. The serving certificate includes
// them only when tls-san-security is false.
var hostIPs = hostInterfaceIPs

// nodeIPForKubelet is the address kubelet uses as the node IP when node-ip
// is unset.
var nodeIPForKubelet = kubeletNodeIP

func advertiseAddress(srv config.Server) (string, error) {
	if srv.AdvertiseAddress != "" {
		if net.ParseIP(srv.AdvertiseAddress) == nil {
			return "", fmt.Errorf("advertise-address %q is not an IP", srv.AdvertiseAddress)
		}
		return srv.AdvertiseAddress, nil
	}
	return nodeIP(srv)
}

// nodeIP is the address published on LoadBalancer Services.
// It is node-ip when that is set, otherwise the address kubelet uses.
func nodeIP(srv config.Server) (string, error) {
	if srv.NodeIP != "" {
		ip := net.ParseIP(srv.NodeIP)
		if ip == nil {
			return "", fmt.Errorf("node-ip %q is not an IP", srv.NodeIP)
		}
		return ip.String(), nil
	}
	name, err := nodeName(srv)
	if err != nil {
		return "", err
	}
	ip, err := nodeIPForKubelet(name)
	if err != nil {
		return "", err
	}
	return ip.String(), nil
}

func nodeName(srv config.Server) (string, error) {
	if srv.NodeName != "" {
		return srv.NodeName, nil
	}
	name, err := os.Hostname()
	if err != nil || name == "" {
		return "", errors.New("node name is empty")
	}
	return name, nil
}

func servingCertificate(srv config.Server) (*x509.Certificate, error) {
	names := newNameSet()
	names.addDNS("localhost")
	names.addIP(net.ParseIP("127.0.0.1"))
	names.addIP(net.ParseIP("::1"))

	name, err := nodeName(srv)
	if err != nil {
		return nil, err
	}
	if ip := net.ParseIP(name); ip != nil {
		names.addIP(ip)
	} else {
		names.addDNS(name)
	}
	if srv.NodeIP != "" {
		if err := names.addRequiredIP("node-ip", srv.NodeIP); err != nil {
			return nil, err
		}
	} else {
		ip, err := nodeIPForKubelet(name)
		if err != nil {
			return nil, err
		}
		names.addIP(ip)
	}
	if srv.NodeExternalIP != "" {
		if err := names.addRequiredIP("node-external-ip", srv.NodeExternalIP); err != nil {
			return nil, err
		}
	}
	cidr := srv.ServiceCIDR
	if cidr == "" {
		cidr = config.DefaultServiceCIDR
	}
	serviceIP, err := config.FirstServiceAddress(cidr)
	if err != nil {
		return nil, err
	}
	if err := names.addRequiredIP("service-cidr", serviceIP); err != nil {
		return nil, err
	}
	for _, dns := range kubernetesServiceNames(srv.ClusterDomain) {
		names.addDNS(dns)
	}
	for _, san := range srv.TLSSan {
		names.addSAN(san)
	}
	if !srv.TLSSanSecurity {
		extras, err := hostIPs()
		if err != nil {
			return nil, err
		}
		for _, ip := range extras {
			names.addIP(ip)
		}
	}
	return names.certificate(), nil
}

// kubeletServingCertificate is the certificate kubelet presents on its port.
// The API verifies it with the cluster CA, so the node name and node address
// are the names the API dials.
func kubeletServingCertificate(srv config.Server) (*x509.Certificate, error) {
	names := newNameSet()
	name, err := nodeName(srv)
	if err != nil {
		return nil, err
	}
	if ip := net.ParseIP(name); ip != nil {
		names.addIP(ip)
	} else {
		names.addDNS(name)
	}
	node := srv.NodeIP
	if node == "" {
		ip, err := nodeIPForKubelet(name)
		if err != nil {
			return nil, err
		}
		node = ip.String()
	}
	if err := names.addRequiredIP("node-ip", node); err != nil {
		return nil, err
	}
	cert := names.certificate()
	cert.Subject.CommonName = name
	return cert, nil
}

func kubernetesServiceNames(domain string) []string {
	names := []string{
		"kubernetes",
		"kubernetes.default",
		"kubernetes.default.svc",
	}
	domain = strings.Trim(domain, ".")
	if domain != "" {
		names = append(names, "kubernetes.default.svc."+domain)
	}
	return names
}

func kubeletNodeIP(nodeName string) (net.IP, error) {
	if ip := net.ParseIP(nodeName); validNodeIP(ip) {
		return canonicalIP(ip), nil
	}
	if ips, err := net.LookupIP(nodeName); err == nil {
		var fallback net.IP
		for _, ip := range ips {
			if !validNodeIP(ip) {
				continue
			}
			if ip.To4() != nil {
				return canonicalIP(ip), nil
			}
			if fallback == nil {
				fallback = ip
			}
		}
		if fallback != nil {
			return canonicalIP(fallback), nil
		}
	}
	ip, err := utilnet.ChooseHostInterface()
	if err != nil {
		return nil, fmt.Errorf("node IP: %w", err)
	}
	if !validNodeIP(ip) {
		return nil, fmt.Errorf("node IP %s is not usable", ip)
	}
	return canonicalIP(ip), nil
}

func validNodeIP(ip net.IP) bool {
	return ip != nil && !ip.IsUnspecified() && !ip.IsLoopback() && !ip.IsMulticast() && !ip.IsLinkLocalUnicast()
}

func hostInterfaceIPs() ([]net.IP, error) {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return nil, fmt.Errorf("host addresses: %w", err)
	}
	var out []net.IP
	for _, addr := range addrs {
		var ip net.IP
		switch v := addr.(type) {
		case *net.IPNet:
			ip = v.IP
		case *net.IPAddr:
			ip = v.IP
		}
		if ip == nil || ip.IsLoopback() || ip.IsUnspecified() {
			continue
		}
		out = append(out, canonicalIP(ip))
	}
	return out, nil
}

func canonicalIP(ip net.IP) net.IP {
	if ip == nil {
		return nil
	}
	if v4 := ip.To4(); v4 != nil {
		return append(net.IP(nil), v4...)
	}
	return append(net.IP(nil), ip...)
}

type nameSet struct {
	dns     []string
	ips     []net.IP
	seenDNS map[string]struct{}
	seenIP  map[string]struct{}
}

func newNameSet() *nameSet {
	return &nameSet{
		seenDNS: map[string]struct{}{},
		seenIP:  map[string]struct{}{},
	}
}

func (n *nameSet) addDNS(name string) {
	name = strings.TrimSpace(name)
	if name == "" {
		return
	}
	key := strings.ToLower(name)
	if _, ok := n.seenDNS[key]; ok {
		return
	}
	n.seenDNS[key] = struct{}{}
	n.dns = append(n.dns, name)
}

func (n *nameSet) addIP(ip net.IP) {
	ip = canonicalIP(ip)
	if ip == nil {
		return
	}
	key := ip.String()
	if _, ok := n.seenIP[key]; ok {
		return
	}
	n.seenIP[key] = struct{}{}
	n.ips = append(n.ips, ip)
}

func (n *nameSet) addRequiredIP(field, raw string) error {
	ip := net.ParseIP(strings.TrimSpace(raw))
	if ip == nil {
		return fmt.Errorf("%s %q is not an IP", field, raw)
	}
	n.addIP(ip)
	return nil
}

func (n *nameSet) addSAN(raw string) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return
	}
	if ip := net.ParseIP(raw); ip != nil {
		n.addIP(ip)
		return
	}
	n.addDNS(raw)
}

func (n *nameSet) certificate() *x509.Certificate {
	return &x509.Certificate{
		Subject:     pkix.Name{CommonName: "kube-apiserver"},
		DNSNames:    append([]string(nil), n.dns...),
		IPAddresses: append([]net.IP(nil), n.ips...),
		KeyUsage:    x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		NotBefore:   time.Now().Add(-time.Hour),
		NotAfter:    time.Now().Add(10 * 365 * 24 * time.Hour),
	}
}
