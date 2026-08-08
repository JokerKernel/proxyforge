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

type targetTLSInspection struct {
	TLSVersion      uint16
	ALPN            string
	CertificateSANs []string
	IPs             []net.IPAddr
	CanonicalName   string
}

func (v NetworkTargetValidator) Validate(ctx context.Context, target, sni, server string) ([]string, error) {
	inspection, err := inspectNetworkTarget(ctx, target, sni, server, v.Timeout)
	if err != nil {
		return nil, err
	}
	warnings := []string{"REALITY 会把未认证的回落连接转发到 target；若目标使用 CDN，流量可能到达第三方基础设施"}
	if len(inspection.IPs) > 2 {
		warnings = append(warnings, "target 解析到多个地址，可能使用 CDN；REALITY 回落流量可能被转发到未认证的第三方站点")
	}
	return warnings, nil
}

func inspectNetworkTarget(ctx context.Context, target, sni, server string, timeout time.Duration) (targetTLSInspection, error) {
	var inspection targetTLSInspection
	if err := system.ValidateTarget(target); err != nil {
		return inspection, err
	}
	host, _, _ := net.SplitHostPort(target)
	if timeout == 0 {
		timeout = 8 * time.Second
	}
	probeCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	ctx = probeCtx
	r := net.Resolver{}
	ips, err := r.LookupIPAddr(ctx, host)
	if err != nil {
		return inspection, fmt.Errorf("解析 REALITY target %s: %w", host, err)
	}
	if len(ips) == 0 {
		return inspection, fmt.Errorf("REALITY target 未解析到地址")
	}
	inspection.IPs = ips
	serverIPs, _ := r.LookupIPAddr(ctx, server)
	local := localIPs()
	for _, item := range ips {
		ip := item.IP
		if forbiddenTargetIP(ip) {
			return inspection, fmt.Errorf("REALITY target %s 解析到私网/本机/保留地址 %s", host, ip)
		}
		for _, own := range local {
			if own.Equal(ip) {
				return inspection, fmt.Errorf("REALITY target 解析到本机地址 %s", ip)
			}
		}
		for _, own := range serverIPs {
			if own.IP.Equal(ip) {
				return inspection, fmt.Errorf("REALITY target 解析到服务器自身地址 %s", ip)
			}
		}
	}
	dialer := &net.Dialer{Timeout: timeout}
	rawConn, err := dialer.DialContext(ctx, "tcp", target)
	if err != nil {
		return inspection, fmt.Errorf("REALITY target TCP 连接失败: %w", err)
	}
	conn := tls.Client(rawConn, &tls.Config{
		ServerName: sni,
		MinVersion: tls.VersionTLS12,
		NextProtos: []string{"h2", "http/1.1"},
	})
	defer conn.Close()
	if err := conn.HandshakeContext(ctx); err != nil {
		return inspection, fmt.Errorf("REALITY target TLS/证书名称校验失败: %w", err)
	}
	if err := conn.VerifyHostname(sni); err != nil {
		return inspection, fmt.Errorf("REALITY target 证书不包含 SNI %s: %w", sni, err)
	}
	state := conn.ConnectionState()
	inspection.TLSVersion = state.Version
	inspection.ALPN = state.NegotiatedProtocol
	if len(state.PeerCertificates) > 0 {
		certificate := state.PeerCertificates[0]
		inspection.CertificateSANs = append(inspection.CertificateSANs, certificate.DNSNames...)
		for _, ip := range certificate.IPAddresses {
			inspection.CertificateSANs = append(inspection.CertificateSANs, ip.String())
		}
	}
	if canonicalName, lookupErr := r.LookupCNAME(ctx, host); lookupErr == nil {
		inspection.CanonicalName = strings.TrimSuffix(strings.ToLower(canonicalName), ".")
	}
	return inspection, nil
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
