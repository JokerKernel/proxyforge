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
	probe := func(_ context.Context, domain, server string) (time.Duration, error) {
		if server != "server.example.com" {
			t.Fatalf("server=%q", server)
		}
		latency, ok := latencies[domain]
		if !ok {
			return 0, errors.New("TLS validation failed")
		}
		return latency, nil
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
}

func TestProbeSNICandidatesRejectsNoValidTargets(t *testing.T) {
	_, err := probeSNICandidates(
		context.Background(),
		[]string{"bad.example.com"},
		"server.example.com",
		10,
		1,
		func(context.Context, string, string) (time.Duration, error) {
			return 0, errors.New("failed")
		},
	)
	if err == nil {
		t.Fatal("expected no-valid-target error")
	}
}
