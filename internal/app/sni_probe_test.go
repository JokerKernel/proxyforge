package app

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestProbeSNICandidatesDeduplicatesFiltersAndSorts(t *testing.T) {
	latencies := map[string]time.Duration{
		"slow.example.com": 30 * time.Millisecond,
		"fast.example.com": 5 * time.Millisecond,
		"mid.example.com":  15 * time.Millisecond,
	}
	probe := func(_ context.Context, domain, server string) (SNICandidate, error) {
		if server != "server.example.com" {
			t.Fatalf("server=%q", server)
		}
		latency, ok := latencies[domain]
		if !ok {
			return SNICandidate{}, errors.New("TLS validation failed")
		}
		return SNICandidate{Latency: latency, TLSVersion: "1.3", ALPN: "h2"}, nil
	}

	got, err := probeSNICandidates(
		context.Background(),
		[]string{"slow.example.com", "FAST.EXAMPLE.COM", "bad.example.com", "fast.example.com", "mid.example.com"},
		"server.example.com",
		2,
		3,
		probe,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Domain != "fast.example.com" || got[1].Domain != "mid.example.com" {
		t.Fatalf("candidates=%#v", got)
	}
	if got[0].TLSVersion != "1.3" || got[0].ALPN != "h2" {
		t.Fatalf("probe metadata was lost: %#v", got[0])
	}
}

func TestProbeSNICandidatesRejectsNoValidTargets(t *testing.T) {
	_, err := probeSNICandidates(
		context.Background(),
		[]string{"bad.example.com"},
		"server.example.com",
		10,
		1,
		func(context.Context, string, string) (SNICandidate, error) {
			return SNICandidate{}, errors.New("failed")
		},
	)
	if err == nil {
		t.Fatal("expected no-valid-target error")
	}
}

func TestBestFamilyLatencyPrefersFasterSuccessfulFamily(t *testing.T) {
	got := bestFamilyLatency(
		FamilyLatency{Present: true, OK: true, Latency: 20 * time.Millisecond},
		FamilyLatency{Present: true, OK: true, Latency: 8 * time.Millisecond},
	)
	if got != 8*time.Millisecond {
		t.Fatalf("best=%s", got)
	}
	ipv4Only := bestFamilyLatency(
		FamilyLatency{Present: true, OK: true, Latency: 15 * time.Millisecond},
		FamilyLatency{Present: true, OK: false},
	)
	if ipv4Only != 15*time.Millisecond {
		t.Fatalf("ipv4 only=%s", ipv4Only)
	}
	if bestFamilyLatency(FamilyLatency{}, FamilyLatency{}) != 0 {
		t.Fatal("empty families should have zero latency")
	}
}

func TestTLSVersionLabel(t *testing.T) {
	if got := tlsVersionLabel(0x0304); got != "1.3" {
		t.Fatalf("TLS 1.3 label=%q", got)
	}
	if got := tlsVersionLabel(0xffff); got != "未知" {
		t.Fatalf("unknown TLS label=%q", got)
	}
}

func TestDetectCDNUsesProviderAndHeuristicSignals(t *testing.T) {
	if got := detectCDN("a1.example.akamaiedge.net", "www.example.com", 2); got != "Akamai（CNAME）" {
		t.Fatalf("known CDN=%q", got)
	}
	if got := detectCDN("origin.example.net", "assets.example.com", 3); got != "疑似（CNAME、3 个地址、资源域名）" {
		t.Fatalf("heuristic CDN=%q", got)
	}
	if got := detectCDN("plain.example.com", "plain.example.com", 1); got != "未发现明显特征" {
		t.Fatalf("plain target=%q", got)
	}
}
