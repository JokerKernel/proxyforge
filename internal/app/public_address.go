package app

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"proxyforge/internal/system"
)

type interfaceAddressCandidate struct {
	name      string
	up        bool
	loopback  bool
	physical  bool
	addresses []net.IP
}

type PublicInterfaceAddress struct {
	Interface string
	Address   string
	Private   bool
}

// PhysicalPublicAddress returns a public unicast address assigned directly to
// an active physical network interface. Private/NAT, loopback, virtual and
// reserved addresses are deliberately ignored.
func PhysicalPublicAddress() (string, error) {
	addresses, err := PhysicalPublicAddresses()
	if err != nil {
		return "", err
	}
	return addresses[0].Address, nil
}

// PhysicalPublicAddresses returns every usable address, ordered with IPv4
// before IPv6 while preserving the operating system's interface order.
func PhysicalPublicAddresses() ([]PublicInterfaceAddress, error) {
	addresses, err := PhysicalInterfaceAddresses()
	if err != nil {
		return nil, err
	}
	public := make([]PublicInterfaceAddress, 0, len(addresses))
	for _, address := range addresses {
		if !address.Private {
			public = append(public, address)
		}
	}
	if len(public) != 0 {
		return public, nil
	}
	return nil, fmt.Errorf("已启用的物理网卡没有公网 IP（内网/NAT 地址不会使用）")
}

// PhysicalInterfaceAddresses returns addresses assigned to active physical
// interfaces. Public addresses are returned first, followed by private/NAT
// addresses; loopback, virtual and reserved addresses are omitted.
func PhysicalInterfaceAddresses() ([]PublicInterfaceAddress, error) {
	interfaces, err := net.Interfaces()
	if err != nil {
		return nil, fmt.Errorf("枚举物理网卡: %w", err)
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
	addresses := physicalInterfaceAddresses(candidates)
	if len(addresses) != 0 {
		return addresses, nil
	}
	return nil, fmt.Errorf("已启用的物理网卡没有可用 IP")
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

func physicalInterfaceAddresses(candidates []interfaceAddressCandidate) []PublicInterfaceAddress {
	var publicIPv4, publicIPv6, privateIPv4, privateIPv6 []PublicInterfaceAddress
	seen := make(map[string]struct{})
	for _, candidate := range candidates {
		if !candidate.up || candidate.loopback || !candidate.physical {
			continue
		}
		for _, ip := range candidate.addresses {
			if !ip.IsGlobalUnicast() || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
				continue
			}
			address := ip.String()
			if _, exists := seen[address]; exists {
				continue
			}
			seen[address] = struct{}{}
			item := PublicInterfaceAddress{Interface: candidate.name, Address: address, Private: forbiddenTargetIP(ip)}
			if item.Private {
				if ip.To4() != nil {
					privateIPv4 = append(privateIPv4, item)
				} else {
					privateIPv6 = append(privateIPv6, item)
				}
			} else if ip.To4() != nil {
				publicIPv4 = append(publicIPv4, item)
			} else {
				publicIPv6 = append(publicIPv6, item)
			}
		}
	}
	return append(append(append(publicIPv4, publicIPv6...), privateIPv4...), privateIPv6...)
}

func physicalPublicAddresses(candidates []interfaceAddressCandidate) []PublicInterfaceAddress {
	all := physicalInterfaceAddresses(candidates)
	public := make([]PublicInterfaceAddress, 0, len(all))
	for _, address := range all {
		if !address.Private {
			public = append(public, address)
		}
	}
	return public
}

func PublicAddress(ctx context.Context) (string, error) {
	// Keep discovery deliberately simple and HTTPS-only; interactive users can edit it.
	req, _ := httpRequest(ctx, "https://api.ipify.org")
	client := &netHTTPClient{timeout: 5 * time.Second}
	b, err := client.do(req)
	if err != nil {
		return "", err
	}
	address := strings.TrimSpace(string(b))
	if err := system.ValidateServer(address); err != nil {
		return "", err
	}
	return address, nil
}
