package app

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"strings"
	"sync"
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
	IPv4            FamilyLatency
	IPv6            FamilyLatency
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
	hasIPv4, hasIPv6 := ipFamilies(ips)
	inspection.IPv4.Present = hasIPv4
	inspection.IPv6.Present = hasIPv6

	type familyResult struct {
		latency time.Duration
		state   *tls.ConnectionState
		err     error
	}
	var v4, v6 familyResult
	var wg sync.WaitGroup
	if hasIPv4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			v4.latency, v4.state, v4.err = probeTLSFamily(ctx, "tcp4", target, sni, timeout)
		}()
	}
	if hasIPv6 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			v6.latency, v6.state, v6.err = probeTLSFamily(ctx, "tcp6", target, sni, timeout)
		}()
	}
	wg.Wait()

	if hasIPv4 && v4.err == nil {
		inspection.IPv4.OK = true
		inspection.IPv4.Latency = v4.latency
	}
	if hasIPv6 && v6.err == nil {
		inspection.IPv6.OK = true
		inspection.IPv6.Latency = v6.latency
	}
	state := pickFamilyTLSState(v4.state, v4.latency, v6.state, v6.latency)
	if state == nil {
		return inspection, combineFamilyProbeErrors(hasIPv4, v4.err, hasIPv6, v6.err)
	}
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

func probeTLSFamily(ctx context.Context, network, target, sni string, timeout time.Duration) (time.Duration, *tls.ConnectionState, error) {
	started := time.Now()
	dialer := &net.Dialer{Timeout: timeout}
	rawConn, err := dialer.DialContext(ctx, network, target)
	if err != nil {
		return 0, nil, fmt.Errorf("REALITY target TCP 连接失败: %w", err)
	}
	conn := tls.Client(rawConn, &tls.Config{
		ServerName: sni,
		MinVersion: tls.VersionTLS12,
		NextProtos: []string{"h2", "http/1.1"},
	})
	defer conn.Close()
	if err := conn.HandshakeContext(ctx); err != nil {
		return 0, nil, fmt.Errorf("REALITY target TLS/证书名称校验失败: %w", err)
	}
	if err := conn.VerifyHostname(sni); err != nil {
		return 0, nil, fmt.Errorf("REALITY target 证书不包含 SNI %s: %w", sni, err)
	}
	state := conn.ConnectionState()
	return time.Since(started), &state, nil
}

func ipFamilies(ips []net.IPAddr) (hasIPv4, hasIPv6 bool) {
	for _, item := range ips {
		if item.IP == nil {
			continue
		}
		if item.IP.To4() != nil {
			hasIPv4 = true
			continue
		}
		if item.IP.To16() != nil {
			hasIPv6 = true
		}
	}
	return hasIPv4, hasIPv6
}

func pickFamilyTLSState(v4 *tls.ConnectionState, v4Latency time.Duration, v6 *tls.ConnectionState, v6Latency time.Duration) *tls.ConnectionState {
	switch {
	case v4 != nil && v6 != nil:
		if v4Latency <= v6Latency {
			return v4
		}
		return v6
	case v4 != nil:
		return v4
	default:
		return v6
	}
}

func combineFamilyProbeErrors(hasIPv4 bool, ipv4Err error, hasIPv6 bool, ipv6Err error) error {
	switch {
	case hasIPv4 && hasIPv6:
		return fmt.Errorf("REALITY target IPv4：%v；IPv6：%v", ipv4Err, ipv6Err)
	case hasIPv4:
		return ipv4Err
	case hasIPv6:
		return ipv6Err
	default:
		return fmt.Errorf("REALITY target 未解析到可探测的 IPv4/IPv6 地址")
	}
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
