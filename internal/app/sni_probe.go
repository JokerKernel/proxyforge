package app

import (
	"context"
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
// validation, together with the observed validation latency.
type SNICandidate struct {
	Domain  string
	Latency time.Duration
}

type sniProbeFunc func(context.Context, string, string) (time.Duration, error)

// ProbeSNICandidates validates candidates concurrently and returns the fastest
// successful targets. Failed DNS, TLS, certificate, local and reserved targets
// are omitted.
func ProbeSNICandidates(ctx context.Context, candidates []string, server string, limit int) ([]SNICandidate, error) {
	if limit <= 0 {
		limit = defaultSNIProbeLimit
	}
	probe := func(ctx context.Context, domain, server string) (time.Duration, error) {
		probeCtx, cancel := context.WithTimeout(ctx, defaultSNIProbeTimeout)
		defer cancel()
		started := time.Now()
		_, err := (NetworkTargetValidator{Timeout: defaultSNIProbeTimeout}).Validate(
			probeCtx,
			net.JoinHostPort(domain, "443"),
			domain,
			server,
		)
		return time.Since(started), err
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
				latency, err := probe(ctx, domain, server)
				if err == nil {
					results <- SNICandidate{Domain: domain, Latency: latency}
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
