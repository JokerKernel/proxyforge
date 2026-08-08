package app

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	defaultSNIProbeLimit       = 10
	defaultSNIProbeConcurrency = 20
	defaultSNIProbeTimeout     = 4 * time.Second
)

// SNICandidate is a REALITY target that completed DNS and TLS certificate
// validation, together with metadata observed during that validation.
type SNICandidate struct {
	Domain          string
	Latency         time.Duration
	TLSVersion      string
	ALPN            string
	CertificateSANs []string
	CDN             string
}

type sniProbeFunc func(context.Context, string, string) (SNICandidate, error)

// ProbeSNICandidates validates candidates concurrently and returns the fastest
// successful targets. Failed DNS, TLS, certificate, local and reserved targets
// are omitted.
func ProbeSNICandidates(ctx context.Context, candidates []string, server string, limit int) ([]SNICandidate, error) {
	if limit <= 0 {
		limit = defaultSNIProbeLimit
	}
	probe := func(ctx context.Context, domain, server string) (SNICandidate, error) {
		probeCtx, cancel := context.WithTimeout(ctx, defaultSNIProbeTimeout)
		defer cancel()
		started := time.Now()
		inspection, err := inspectNetworkTarget(
			probeCtx,
			net.JoinHostPort(domain, "443"),
			domain,
			server,
			defaultSNIProbeTimeout,
		)
		if err != nil {
			return SNICandidate{}, err
		}
		return SNICandidate{
			Domain:          domain,
			Latency:         time.Since(started),
			TLSVersion:      tlsVersionLabel(inspection.TLSVersion),
			ALPN:            inspection.ALPN,
			CertificateSANs: inspection.CertificateSANs,
			CDN:             detectCDN(inspection.CanonicalName, domain, len(inspection.IPs)),
		}, nil
	}
	return probeSNICandidates(ctx, candidates, server, limit, defaultSNIProbeConcurrency, probe)
}

func probeSNICandidates(ctx context.Context, candidates []string, server string, limit, concurrency int, probe sniProbeFunc) ([]SNICandidate, error) {
	unique := uniqueDomains(candidates)
	if len(unique) == 0 {
		return nil, fmt.Errorf("SNI 候选列表为空")
	}
	if concurrency <= 0 {
		concurrency = 1
	}
	if concurrency > len(unique) {
		concurrency = len(unique)
	}

	jobs := make(chan string)
	results := make(chan SNICandidate, len(unique))
	var workers sync.WaitGroup
	for range concurrency {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for domain := range jobs {
				candidate, err := probe(ctx, domain, server)
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

	valid := make([]SNICandidate, 0, len(unique))
	for candidate := range results {
		valid = append(valid, candidate)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(valid) == 0 {
		return nil, fmt.Errorf("没有候选域名通过 DNS、TLS 和证书名称校验")
	}
	sort.Slice(valid, func(i, j int) bool {
		if valid[i].Latency == valid[j].Latency {
			return valid[i].Domain < valid[j].Domain
		}
		return valid[i].Latency < valid[j].Latency
	})
	if len(valid) > limit {
		valid = valid[:limit]
	}
	return valid, nil
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

func detectCDN(canonicalName, domain string, ipCount int) string {
	type cdnProvider struct {
		name     string
		suffixes []string
	}
	providers := []cdnProvider{
		{name: "Akamai", suffixes: []string{"akamai.net", "akamaiedge.net", "akamaihd.net", "akamaized.net", "edgekey.net", "edgesuite.net"}},
		{name: "AWS CloudFront", suffixes: []string{"cloudfront.net"}},
		{name: "Cloudflare", suffixes: []string{"cloudflare.net", "cdn.cloudflare.net"}},
		{name: "Fastly", suffixes: []string{"fastly.net", "fastlylb.net"}},
		{name: "Microsoft/Azure CDN", suffixes: []string{"azureedge.net", "azurefd.net", "msedge.net", "trafficmanager.net", "gallerycdn.vsassets.io"}},
		{name: "Google CDN", suffixes: []string{"gstatic.com", "googleusercontent.com", "1e100.net"}},
		{name: "Apple CDN", suffixes: []string{"mzstatic.com", "cdn-apple.com"}},
		{name: "CDN77", suffixes: []string{"cdn77.org", "cdn77.com"}},
		{name: "Bunny CDN", suffixes: []string{"b-cdn.net"}},
		{name: "Imperva", suffixes: []string{"incapdns.net"}},
		{name: "Vercel", suffixes: []string{"vercel-dns.com"}},
		{name: "Netlify", suffixes: []string{"netlify.app", "netlify.com"}},
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
