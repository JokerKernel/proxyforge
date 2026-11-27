package app

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
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

func PublicAddress(ctx context.Context) (string, error) {
	// Keep discovery deliberately simple and HTTPS-only; interactive users can edit it.
	return discoverPublicAddress(ctx, &http.Client{Timeout: 5 * time.Second})
}

func discoverPublicAddress(ctx context.Context, client *http.Client) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.ipify.org", nil)
	if err != nil {
		return "", fmt.Errorf("创建公网地址探测请求: %w", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("探测公网地址: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("探测公网地址返回 %s", resp.Status)
	}
	const maxAddressSize = 128
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxAddressSize+1))
	if err != nil {
		return "", fmt.Errorf("读取公网地址响应: %w", err)
	}
	if len(b) > maxAddressSize {
		return "", fmt.Errorf("公网地址响应超过 %d bytes", maxAddressSize)
	}
	address := strings.TrimSpace(string(b))
	if net.ParseIP(address) == nil {
		return "", fmt.Errorf("公网地址响应不是有效 IP")
	}
	return address, nil
}
