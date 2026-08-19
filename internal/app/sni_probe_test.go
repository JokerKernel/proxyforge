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

func TestSNISortPrefersIPv4Latency(t *testing.T) {
	got, err := probeSNICandidates(
		context.Background(),
		[]string{"v6-fast.example.com", "v4-mid.example.com", "v4-fast.example.com"},
		"server.example.com",
		10,
		3,
		func(_ context.Context, domain, _ string) (SNICandidate, error) {
			switch domain {
			case "v6-fast.example.com":
				return SNICandidate{
					IPv6: FamilyLatency{Present: true, OK: true, Latency: 3 * time.Millisecond},
				}, nil
			case "v4-mid.example.com":
				return SNICandidate{
					IPv4: FamilyLatency{Present: true, OK: true, Latency: 20 * time.Millisecond},
					IPv6: FamilyLatency{Present: true, OK: true, Latency: 1 * time.Millisecond},
				}, nil
			case "v4-fast.example.com":
				return SNICandidate{
					IPv4: FamilyLatency{Present: true, OK: true, Latency: 8 * time.Millisecond},
					IPv6: FamilyLatency{Present: true, OK: true, Latency: 50 * time.Millisecond},
				}, nil
			default:
				return SNICandidate{}, errors.New("unexpected")
			}
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[0].Domain != "v4-fast.example.com" || got[1].Domain != "v4-mid.example.com" || got[2].Domain != "v6-fast.example.com" {
		t.Fatalf("order=%#v", got)
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
