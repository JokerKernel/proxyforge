package docker

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/netip"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	sniProbeConcurrency = 20
	sniProbeTimeout     = 4 * time.Second
)

type familyLatency struct {
	Present bool
	OK      bool
	Latency time.Duration
}

type sniCandidate struct {
	Domain          string
	Latency         time.Duration
	IPv4            familyLatency
	IPv6            familyLatency
	TLSVersion      string
	ALPN            string
	CertificateSANs []string
	CDN             string
}

func (m *Manager) probeSNICandidates(ctx context.Context, candidates []string, server string, limit int) ([]sniCandidate, error) {
	if m.probeSNI != nil {
		return m.probeSNI(ctx, candidates, server, limit)
	}
	return probeSNICandidates(ctx, candidates, server, limit)
}

func probeSNICandidates(ctx context.Context, candidates []string, server string, limit int) ([]sniCandidate, error) {
	unique := uniqueDomains(candidates)
	if len(unique) == 0 {
		return nil, fmt.Errorf("SNI 候选列表为空")
	}
	if limit <= 0 {
		limit = len(unique)
	}
	concurrency := sniProbeConcurrency
	if concurrency > len(unique) {
		concurrency = len(unique)
	}
	jobs := make(chan string)
	results := make(chan sniCandidate, len(unique))
	var workers sync.WaitGroup
	for range concurrency {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for domain := range jobs {
				candidate, err := probeOneSNI(ctx, domain, server)
				if err == nil {
					candidate.Domain = domain
					results <- candidate
				}
			}
		}()
	}
	go func() {
		defer close(jobs)
		for _, domain := range unique {
			select {
			case jobs <- domain:
			case <-ctx.Done():
				return
			}
		}
	}()
	go func() {
		workers.Wait()
		close(results)
	}()
	valid := make([]sniCandidate, 0, len(unique))
	for candidate := range results {
		valid = append(valid, candidate)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(valid) == 0 {
		return nil, fmt.Errorf("没有候选域名通过 DNS、TLS 和证书名称校验")
	}
	sortSNICandidates(valid, true)
	if len(valid) > limit {
		valid = valid[:limit]
	}
	return valid, nil
}

func probeOneSNI(ctx context.Context, domain, server string) (sniCandidate, error) {
	probeCtx, cancel := context.WithTimeout(ctx, sniProbeTimeout)
	defer cancel()
	if !hostname(domain, true) {
		return sniCandidate{}, fmt.Errorf("SNI 必须为有效域名")
	}
	target := net.JoinHostPort(domain, "443")
	resolver := net.Resolver{}
	ips, err := resolver.LookupIPAddr(probeCtx, domain)
	if err != nil || len(ips) == 0 {
		return sniCandidate{}, fmt.Errorf("解析 REALITY target %s: %w", domain, err)
	}
	serverIPs, _ := resolver.LookupIPAddr(probeCtx, server)
	local := localIPs()
	for _, item := range ips {
		ip := item.IP
		addr, ok := netip.AddrFromSlice(ip)
		if !ok || !addr.IsGlobalUnicast() || addr.IsPrivate() || addr.IsLoopback() || addr.IsLinkLocalUnicast() || forbiddenAddress(addr.Unmap()) {
			return sniCandidate{}, fmt.Errorf("REALITY target %s 解析到私网/本机/保留地址 %s", domain, ip)
		}
		for _, own := range local {
			if own.Equal(ip) {
				return sniCandidate{}, fmt.Errorf("REALITY target 解析到本机地址 %s", ip)
			}
		}
		for _, own := range serverIPs {
			if own.IP.Equal(ip) {
				return sniCandidate{}, fmt.Errorf("REALITY target 解析到服务器自身地址 %s", ip)
			}
		}
	}
	hasIPv4, hasIPv6 := ipFamilies(ips)
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
			v4.latency, v4.state, v4.err = probeTLSFamily(probeCtx, "tcp4", target, domain)
		}()
	}
	if hasIPv6 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			v6.latency, v6.state, v6.err = probeTLSFamily(probeCtx, "tcp6", target, domain)
		}()
	}
	wg.Wait()
	candidate := sniCandidate{Domain: domain, IPv4: familyLatency{Present: hasIPv4}, IPv6: familyLatency{Present: hasIPv6}}
	if hasIPv4 && v4.err == nil {
		candidate.IPv4.OK = true
		candidate.IPv4.Latency = v4.latency
	}
	if hasIPv6 && v6.err == nil {
		candidate.IPv6.OK = true
		candidate.IPv6.Latency = v6.latency
	}
	state := pickFamilyTLSState(v4.state, v4.latency, v6.state, v6.latency)
	if state == nil {
		return sniCandidate{}, combineFamilyProbeErrors(hasIPv4, v4.err, hasIPv6, v6.err)
	}
	candidate.Latency = ipv4SortLatency(candidate.IPv4, candidate.IPv6)
	candidate.TLSVersion = tlsVersionLabel(state.Version)
	candidate.ALPN = state.NegotiatedProtocol
	if len(state.PeerCertificates) > 0 {
		certificate := state.PeerCertificates[0]
		candidate.CertificateSANs = append(candidate.CertificateSANs, certificate.DNSNames...)
		for _, ip := range certificate.IPAddresses {
			candidate.CertificateSANs = append(candidate.CertificateSANs, ip.String())
		}
	}
	if canonical, lookupErr := resolver.LookupCNAME(probeCtx, domain); lookupErr == nil {
		candidate.CDN = detectCDN(strings.TrimSuffix(strings.ToLower(canonical), "."), domain, len(ips))
	} else {
		candidate.CDN = detectCDN("", domain, len(ips))
	}
	return candidate, nil
}

func probeTLSFamily(ctx context.Context, network, target, sni string) (time.Duration, *tls.ConnectionState, error) {
	started := time.Now()
	raw, err := (&net.Dialer{}).DialContext(ctx, network, target)
	if err != nil {
		return 0, nil, fmt.Errorf("REALITY target TCP 连接失败: %w", err)
	}
	conn := tls.Client(raw, &tls.Config{ServerName: sni, MinVersion: tls.VersionTLS12, NextProtos: []string{"h2", "http/1.1"}})
	defer conn.Close()
	if err = conn.HandshakeContext(ctx); err != nil {
		return 0, nil, fmt.Errorf("REALITY target TLS/证书名称校验失败: %w", err)
	}
	if err = conn.VerifyHostname(sni); err != nil {
		return 0, nil, fmt.Errorf("REALITY target 证书不包含 SNI %s: %w", sni, err)
	}
	state := conn.ConnectionState()
	return time.Since(started), &state, nil
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

func ipFamilies(ips []net.IPAddr) (hasIPv4, hasIPv6 bool) {
	for _, item := range ips {
		if item.IP == nil {
			continue
		}
		if item.IP.To4() != nil {
			hasIPv4 = true
		} else if item.IP.To16() != nil {
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

func ipv4SortLatency(ipv4, ipv6 familyLatency) time.Duration {
	if ipv4.OK {
		return ipv4.Latency
	}
	if ipv6.OK {
		return ipv6.Latency
	}
	return 0
}

func sortSNICandidates(candidates []sniCandidate, ipv4 bool) {
	sort.SliceStable(candidates, func(i, j int) bool {
		leftKey, leftOK := sniFamilySortKey(candidates[i], ipv4)
		rightKey, rightOK := sniFamilySortKey(candidates[j], ipv4)
		if leftOK != rightOK {
			return leftOK
		}
		if leftKey != rightKey {
			return leftKey < rightKey
		}
		return candidates[i].Domain < candidates[j].Domain
	})
}

func sniFamilySortKey(candidate sniCandidate, ipv4 bool) (time.Duration, bool) {
	family := candidate.IPv6
	if ipv4 {
		family = candidate.IPv4
	}
	if family.OK {
		return family.Latency, true
	}
	if ipv4 && !candidate.IPv4.Present && !candidate.IPv6.Present && candidate.Latency > 0 {
		return candidate.Latency, true
	}
	return 0, false
}

func tlsVersionLabel(version uint16) string {
	switch version {
	case tls.VersionTLS13:
		return "1.3"
	case tls.VersionTLS12:
		return "1.2"
	case tls.VersionTLS11:
		return "1.1"
	case tls.VersionTLS10:
		return "1.0"
	default:
		return "未知"
	}
}

func uniqueDomains(candidates []string) []string {
	seen := make(map[string]struct{}, len(candidates))
	result := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		domain := strings.ToLower(strings.TrimSpace(candidate))
		if domain == "" {
			continue
		}
		if _, ok := seen[domain]; ok {
			continue
		}
		seen[domain] = struct{}{}
		result = append(result, domain)
	}
	return result
}

func detectCDN(canonicalName, domain string, ipCount int) string {
	type cdnProvider struct {
		name     string
		suffixes []string
	}
	providers := []cdnProvider{
		{name: "Akamai", suffixes: []string{"akamai.net", "akamaiedge.net", "akamaihd.net", "akamaized.net", "edgekey.net", "edgesuite.net", "akadns.net", "akamaitech.net", "akamai.com"}},
		{name: "AWS CloudFront", suffixes: []string{"cloudfront.net"}},
		{name: "Cloudflare", suffixes: []string{"cloudflare.com", "cloudflare.net", "cdn.cloudflare.net", "pages.dev", "trycloudflare.com", "workers.dev", "r2.dev"}},
		{name: "Fastly", suffixes: []string{"fastly.net", "fastlylb.net", "fastly.com"}},
		{name: "Microsoft/Azure CDN", suffixes: []string{"azureedge.net", "azurefd.net", "msedge.net", "trafficmanager.net", "gallerycdn.vsassets.io", "aspnetcdn.com", "office.net"}},
		{name: "Google CDN", suffixes: []string{"gstatic.com", "googleusercontent.com", "1e100.net", "googlehosted.com", "googlevideo.com"}},
		{name: "Apple CDN", suffixes: []string{"mzstatic.com", "cdn-apple.com", "apple-dns.net", "icloud-content.com"}},
		{name: "阿里云 CDN", suffixes: []string{"alicdn.com", "kunlun.com", "kunlunar.com", "kunlunle.com", "kunlunpi.com", "cdngslb.com", "taobaocdn.com", "tbcdn.cn"}},
		{name: "腾讯云 CDN", suffixes: []string{"qcloudcdn.com", "cdn.dnsv1.com", "tcdn.qq.com", "gdtimg.com", "qpic.cn", "myqcloud.com"}},
		{name: "CDN77", suffixes: []string{"cdn77.org", "cdn77.com"}},
		{name: "Bunny CDN", suffixes: []string{"b-cdn.net"}},
		{name: "Amazon 图片 CDN", suffixes: []string{"images-amazon.com", "ssl-images-amazon.com", "media-amazon.com"}},
	}
	normalizedDomain := strings.TrimSuffix(strings.ToLower(domain), ".")
	normalizedCNAME := strings.TrimSuffix(strings.ToLower(canonicalName), ".")
	for _, provider := range providers {
		for _, suffix := range provider.suffixes {
			if hostHasSuffix(normalizedDomain, suffix) || hostHasSuffix(normalizedCNAME, suffix) {
				if normalizedCNAME != "" && normalizedCNAME != normalizedDomain {
					return provider.name + "（CNAME）"
				}
				return provider.name
			}
		}
	}
	features := make([]string, 0, 3)
	if normalizedCNAME != "" && normalizedCNAME != normalizedDomain {
		features = append(features, "CNAME")
	}
	if ipCount > 1 {
		features = append(features, fmt.Sprintf("%d 个地址", ipCount))
	}
	if hasCDNDomainLabel(normalizedDomain) {
		features = append(features, "资源域名")
	}
	if len(features) == 0 {
		return "未发现明显特征"
	}
	return "疑似（" + strings.Join(features, "、") + "）"
}

func hostHasSuffix(host, suffix string) bool {
	return host == suffix || strings.HasSuffix(host, "."+suffix)
}

func hasCDNDomainLabel(domain string) bool {
	for _, label := range strings.Split(domain, ".") {
		lower := strings.ToLower(label)
		if lower == "cdn" || lower == "edge" || lower == "static" || lower == "assets" || strings.HasPrefix(lower, "cdn-") || strings.HasSuffix(lower, "-cdn") {
			return true
		}
	}
	return false
}
