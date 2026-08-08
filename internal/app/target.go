package app

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"strings"
	"time"

	"proxyforge/internal/system"
)

type TargetValidator interface {
	Validate(context.Context, string, string, string) ([]string, error)
}

type NetworkTargetValidator struct{ Timeout time.Duration }

func (v NetworkTargetValidator) Validate(ctx context.Context, target, sni, server string) ([]string, error) {
	if err := system.ValidateTarget(target); err != nil {
		return nil, err
	}
	host, _, _ := net.SplitHostPort(target)
	timeout := v.Timeout
	if timeout == 0 {
		timeout = 8 * time.Second
	}
	r := net.Resolver{}
	ips, err := r.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, fmt.Errorf("解析 REALITY target %s: %w", host, err)
	}
	if len(ips) == 0 {
		return nil, fmt.Errorf("REALITY target 未解析到地址")
	}
	serverIPs, _ := r.LookupIPAddr(ctx, server)
	local := localIPs()
	for _, item := range ips {
		ip := item.IP
		if forbiddenTargetIP(ip) {
			return nil, fmt.Errorf("REALITY target %s 解析到私网/本机/保留地址 %s", host, ip)
		}
		for _, own := range local {
			if own.Equal(ip) {
				return nil, fmt.Errorf("REALITY target 解析到本机地址 %s", ip)
			}
		}
		for _, own := range serverIPs {
			if own.IP.Equal(ip) {
				return nil, fmt.Errorf("REALITY target 解析到服务器自身地址 %s", ip)
			}
		}
	}
	dialer := &net.Dialer{Timeout: timeout}
	conn, err := tls.DialWithDialer(dialer, "tcp", target, &tls.Config{ServerName: sni, MinVersion: tls.VersionTLS12})
	if err != nil {
		return nil, fmt.Errorf("REALITY target TLS/证书名称校验失败: %w", err)
	}
	defer conn.Close()
	if err := conn.VerifyHostname(sni); err != nil {
		return nil, fmt.Errorf("REALITY target 证书不包含 SNI %s: %w", sni, err)
	}
	warnings := []string{"REALITY 会把未认证的回落连接转发到 target；若目标使用 CDN，流量可能到达第三方基础设施"}
	if len(ips) > 2 {
		warnings = append(warnings, "target 解析到多个地址，可能使用 CDN；REALITY 回落流量可能被转发到未认证的第三方站点")
	}
	if strings.HasPrefix(strings.ToLower(conn.ConnectionState().NegotiatedProtocol), "h3") {
		warnings = append(warnings, "target 偏好 HTTP/3；请确认 TCP TLS 回落长期可用")
	}
	return warnings, nil
}

func localIPs() []net.IP {
	addrs, _ := net.InterfaceAddrs()
	result := make([]net.IP, 0, len(addrs))
	for _, addr := range addrs {
		if ip, _, err := net.ParseCIDR(addr.String()); err == nil {
			result = append(result, ip)
		}
	}
	return result
}

func forbiddenTargetIP(ip net.IP) bool {
	if !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsUnspecified() {
		return true
	}
	reserved := []string{"100.64.0.0/10", "192.0.0.0/24", "192.0.2.0/24", "198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24", "240.0.0.0/4", "2001:db8::/32"}
	for _, raw := range reserved {
		_, block, _ := net.ParseCIDR(raw)
		if block.Contains(ip) {
			return true
		}
	}
	return false
}
