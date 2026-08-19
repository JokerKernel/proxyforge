package system

import (
	"net"
	"os"
	"strings"
)

var (
	resolvConfFile   = "/etc/resolv.conf"
	resolvedConfFile = "/run/systemd/resolve/resolv.conf"
)

// ResolverAddresses returns the host DNS servers from resolv.conf.
// If only systemd-resolved's stub listener is listed, it follows the
// resolved upstream file so the card can show the real addresses.
func ResolverAddresses() []string {
	addrs := nameserversFromFile(resolvConfFile)
	if len(addrs) == 0 || onlyStubResolvers(addrs) {
		if upstream := nameserversFromFile(resolvedConfFile); len(upstream) > 0 && !onlyStubResolvers(upstream) {
			return upstream
		}
	}
	return addrs
}

func nameserversFromFile(path string) []string {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var addrs []string
	seen := make(map[string]struct{})
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 || !strings.EqualFold(fields[0], "nameserver") {
			continue
		}
		addr := strings.Trim(fields[1], "[]")
		if net.ParseIP(addr) == nil {
			continue
		}
		if _, exists := seen[addr]; exists {
			continue
		}
		seen[addr] = struct{}{}
		addrs = append(addrs, addr)
	}
	return addrs
}

func onlyStubResolvers(addrs []string) bool {
	if len(addrs) == 0 {
		return false
	}
	for _, addr := range addrs {
		if !isStubResolver(addr) {
			return false
		}
	}
	return true
}

func isStubResolver(addr string) bool {
	switch addr {
	case "127.0.0.53", "127.0.0.54", "::1":
		return true
	default:
		return false
	}
}
