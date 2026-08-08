package app

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
)

type interfaceAddressCandidate struct {
	name      string
	up        bool
	loopback  bool
	physical  bool
	addresses []net.IP
}

// PhysicalPublicAddress returns a public unicast address assigned directly to
// an active physical network interface. Private/NAT, loopback, virtual and
// reserved addresses are deliberately ignored.
func PhysicalPublicAddress() (string, error) {
	interfaces, err := net.Interfaces()
	if err != nil {
		return "", fmt.Errorf("枚举物理网卡: %w", err)
	}
	candidates := make([]interfaceAddressCandidate, 0, len(interfaces))
	for _, item := range interfaces {
		candidate := interfaceAddressCandidate{
			name: item.Name, up: item.Flags&net.FlagUp != 0,
			loopback: item.Flags&net.FlagLoopback != 0,
			physical: physicalNetworkInterface(item.Name),
		}
		addresses, addressErr := item.Addrs()
		if addressErr != nil {
			continue
		}
		for _, address := range addresses {
			if ip := interfaceIP(address); ip != nil {
				candidate.addresses = append(candidate.addresses, ip)
			}
		}
		candidates = append(candidates, candidate)
	}
	if address := preferredPhysicalPublicAddress(candidates); address != "" {
		return address, nil
	}
	return "", fmt.Errorf("已启用的物理网卡没有公网 IP（内网/NAT 地址不会使用）")
}

func physicalNetworkInterface(name string) bool {
	_, err := os.Stat(filepath.Join("/sys/class/net", name, "device"))
	return err == nil
}

func interfaceIP(address net.Addr) net.IP {
	switch value := address.(type) {
	case *net.IPNet:
		return value.IP
	case *net.IPAddr:
		return value.IP
	default:
		ip, _, err := net.ParseCIDR(address.String())
		if err == nil {
			return ip
		}
		return net.ParseIP(address.String())
	}
}

func preferredPhysicalPublicAddress(candidates []interfaceAddressCandidate) string {
	var ipv6 string
	for _, candidate := range candidates {
		if !candidate.up || candidate.loopback || !candidate.physical {
			continue
		}
		for _, ip := range candidate.addresses {
			if forbiddenTargetIP(ip) {
				continue
			}
			if ip.To4() != nil {
				return ip.String()
			}
			if ipv6 == "" {
				ipv6 = ip.String()
			}
		}
	}
	return ipv6
}
