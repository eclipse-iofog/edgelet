package config

import (
	"fmt"
	"net"
)

// applyNetwork fills cluster addresses the files left unset and checks that
// cluster DNS lies in the service network. The node pod range and the derived
// DNS address are stored on the server for later callers.
func (s *Server) applyNetwork() error {
	if !s.present["cluster-cidr"] {
		s.ClusterCIDR = DefaultClusterCIDR
	}
	if !s.present["service-cidr"] {
		s.ServiceCIDR = DefaultServiceCIDR
	}
	if !s.present["cluster-domain"] {
		s.ClusterDomain = DefaultClusterDomain
	}
	if !s.present["service-node-port-range"] {
		s.ServiceNodePortRange = DefaultServiceNodePortRange
	}
	pod, err := nodePodCIDR(s.ClusterCIDR)
	if err != nil {
		return err
	}
	s.NodePodCIDR = pod
	if !s.present["cluster-dns"] {
		dns, err := tenthAddress(s.ServiceCIDR)
		if err != nil {
			return err
		}
		s.ClusterDNS = dns
	}
	return clusterDNSInService(s.ClusterDNS, s.ServiceCIDR)
}

// nodePodCIDR is the first /24 of cluster. The mask stays /24.
func nodePodCIDR(cluster string) (string, error) {
	_, network, err := net.ParseCIDR(cluster)
	if err != nil {
		return "", fmt.Errorf("cluster-cidr %q is not a network", cluster)
	}
	ip := network.IP.To4()
	if ip == nil {
		return "", fmt.Errorf("cluster-cidr %q is not an IPv4 network", cluster)
	}
	mask := net.CIDRMask(24, 32)
	pod := ip.Mask(mask)
	if pod == nil {
		return "", fmt.Errorf("cluster-cidr %q is not an IPv4 network", cluster)
	}
	return (&net.IPNet{IP: pod, Mask: mask}).String(), nil
}

// FirstServiceAddress is the first host address of cidr.
// Kubernetes assigns that address to the kubernetes API service.
// The default service network 10.43.0.0/16 yields 10.43.0.1.
func FirstServiceAddress(cidr string) (string, error) {
	ip, network, err := offsetAddress(cidr, 1)
	if err != nil {
		return "", err
	}
	if !network.Contains(ip) {
		return "", fmt.Errorf("service-cidr %q has no service address", cidr)
	}
	return ip.String(), nil
}

// tenthAddress is the address ten steps from the start of cidr.
// The default service network 10.43.0.0/16 yields 10.43.0.10.
func tenthAddress(cidr string) (string, error) {
	ip, network, err := offsetAddress(cidr, 10)
	if err != nil {
		return "", err
	}
	if !network.Contains(ip) {
		return "", fmt.Errorf("service-cidr %q has no 10th address", cidr)
	}
	return ip.String(), nil
}

func offsetAddress(cidr string, steps int) (net.IP, *net.IPNet, error) {
	_, network, err := net.ParseCIDR(cidr)
	if err != nil {
		return nil, nil, fmt.Errorf("service-cidr %q is not a network", cidr)
	}
	ip := make(net.IP, len(network.IP))
	copy(ip, network.IP)
	for i := 0; i < steps; i++ {
		if !incIP(ip) {
			return nil, nil, fmt.Errorf("service-cidr %q has no service address", cidr)
		}
	}
	return ip, network, nil
}

func clusterDNSInService(dns, cidr string) error {
	_, network, err := net.ParseCIDR(cidr)
	if err != nil {
		return fmt.Errorf("service-cidr %q is not a network", cidr)
	}
	ip := net.ParseIP(dns)
	if ip == nil || !network.Contains(ip) {
		return fmt.Errorf("cluster-dns %q is outside service-cidr %q", dns, cidr)
	}
	return nil
}

func incIP(ip net.IP) bool {
	for i := len(ip) - 1; i >= 0; i-- {
		ip[i]++
		if ip[i] != 0 {
			return true
		}
	}
	return false
}
