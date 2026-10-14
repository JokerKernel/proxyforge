package docker

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"time"
)

var netDialer = net.Dialer{Timeout: 300 * time.Millisecond}

func hostname(value string, requireDot bool) bool {
	if value == "" || len(value) > 253 || strings.TrimSpace(value) != value || strings.ContainsAny(value, "/:@_\r\n") {
		return false
	}
	if requireDot && !strings.Contains(value, ".") {
		return false
	}
	for _, label := range strings.Split(strings.TrimSuffix(value, "."), ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, c := range label {
			if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-') {
				return false
			}
		}
	}
	return true
}
func validServer(server string) bool { return net.ParseIP(server) != nil || hostname(server, true) }
func validateTarget(ctx context.Context, target, sni, server string) error {
	host, p, err := net.SplitHostPort(target)
	if err != nil || host == "" {
		return fmt.Errorf("REALITY target 必须为 host:port")
	}
	port, err := strconv.Atoi(p)
	if err != nil || port < 1 || port > 65535 {
		return fmt.Errorf("REALITY target 端口无效")
	}
	if !hostname(sni, true) {
		return fmt.Errorf("SNI 必须为有效域名")
	}
	ctx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil || len(ips) == 0 {
		return fmt.Errorf("REALITY target DNS 解析失败: %v", err)
	}
	serverIPs, _ := net.DefaultResolver.LookupIPAddr(ctx, server)
	local, _ := net.InterfaceAddrs()
	for _, item := range ips {
		ip := item.IP
		addr, ok := netip.AddrFromSlice(ip)
		if !ok || !addr.IsGlobalUnicast() || addr.IsPrivate() || addr.IsLoopback() || addr.IsLinkLocalUnicast() || forbiddenAddress(addr) {
			return fmt.Errorf("REALITY target 解析到私网/本机/保留地址 %s", ip)
		}
		for _, own := range serverIPs {
			if ip.Equal(own.IP) {
				return fmt.Errorf("REALITY target 不能是服务器自身地址")
			}
		}
		for _, a := range local {
			localIP, _, err := net.ParseCIDR(a.String())
			if err == nil && ip.Equal(localIP) {
				return fmt.Errorf("REALITY target 不能是本机地址")
			}
		}
	}
	results := make(chan error, len(ips))
	for _, item := range ips {
		go func(ip net.IP) {
			conn, err := (&net.Dialer{Timeout: 6 * time.Second}).DialContext(ctx, "tcp", net.JoinHostPort(ip.String(), p))
			if err != nil {
				results <- err
				return
			}
			tlsConn := tls.Client(conn, &tls.Config{ServerName: sni, MinVersion: tls.VersionTLS12, NextProtos: []string{"h2", "http/1.1"}})
			defer tlsConn.Close()
			results <- tlsConn.HandshakeContext(ctx)
		}(item.IP)
	}
	for range ips {
		select {
		case err := <-results:
			if err == nil {
				return nil
			}
		case <-ctx.Done():
			return fmt.Errorf("REALITY target TCP/TLS/SNI 校验失败: %w", ctx.Err())
		}
	}
	return fmt.Errorf("REALITY target 所有地址的 TCP/TLS/SNI 校验均失败")
}
func forbiddenAddress(addr netip.Addr) bool {
	addr = addr.Unmap()
	for _, s := range blockedRanges {
		prefix := netip.MustParsePrefix(s)
		if prefix.Contains(addr) {
			return true
		}
	}
	return false
}

var blockedRanges = []string{"0.0.0.0/8", "10.0.0.0/8", "100.64.0.0/10", "127.0.0.0/8", "169.254.0.0/16", "172.16.0.0/12", "192.0.0.0/24", "192.0.2.0/24", "192.168.0.0/16", "198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24", "224.0.0.0/4", "240.0.0.0/4", "::/128", "::1/128", "64:ff9b:1::/48", "100::/64", "2001:db8::/32", "fc00::/7", "fe80::/10", "ff00::/8"}
