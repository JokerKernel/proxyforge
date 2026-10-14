package docker

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type publicAddress struct {
	Interface string
	Address   string
	Private   bool
}

func (m *Manager) listPhysicalAddresses() ([]publicAddress, error) {
	if m.physicalIPs != nil {
		return m.physicalIPs()
	}
	return physicalPublicAddresses()
}

func (m *Manager) discoverPublicAddress(ctx context.Context) (string, error) {
	if m.externalIP != nil {
		return m.externalIP(ctx)
	}
	return m.onlinePublicAddress(ctx)
}

func (m *Manager) onlinePublicAddress(ctx context.Context) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.ipify.org", nil)
	if err != nil {
		return "", err
	}
	client := m.http
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 128))
	if err != nil {
		return "", err
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("在线探测返回 %s", resp.Status)
	}
	address := strings.TrimSpace(string(body))
	if !validServer(address) {
		return "", fmt.Errorf("在线探测结果无效")
	}
	return address, nil
}

func physicalPublicAddresses() ([]publicAddress, error) {
	interfaces, err := net.Interfaces()
	if err != nil {
		return nil, fmt.Errorf("枚举物理网卡: %w", err)
	}
	var publicIPv4, publicIPv6, privateIPv4, privateIPv6 []publicAddress
	seen := map[string]struct{}{}
	for _, item := range interfaces {
		if item.Flags&net.FlagUp == 0 || item.Flags&net.FlagLoopback != 0 || !physicalNetworkInterface(item.Name) {
			continue
		}
		addresses, addressErr := item.Addrs()
		if addressErr != nil {
			continue
		}
		for _, address := range addresses {
			ip := interfaceIP(address)
			if ip == nil || !ip.IsGlobalUnicast() || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
				continue
			}
			text := ip.String()
			if _, exists := seen[text]; exists {
				continue
			}
			seen[text] = struct{}{}
			addr, ok := netip.AddrFromSlice(ip)
			private := !ok || !addr.IsGlobalUnicast() || addr.IsPrivate() || addr.IsLoopback() || addr.IsLinkLocalUnicast() || forbiddenAddress(addr.Unmap())
			entry := publicAddress{Interface: item.Name, Address: text, Private: private}
			switch {
			case private && ip.To4() != nil:
				privateIPv4 = append(privateIPv4, entry)
			case private:
				privateIPv6 = append(privateIPv6, entry)
			case ip.To4() != nil:
				publicIPv4 = append(publicIPv4, entry)
			default:
				publicIPv6 = append(publicIPv6, entry)
			}
		}
	}
	ordered := append(append(append(publicIPv4, publicIPv6...), privateIPv4...), privateIPv6...)
	if len(ordered) == 0 {
		return nil, fmt.Errorf("已启用的物理网卡没有可用 IP")
	}
	return ordered, nil
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
