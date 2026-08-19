package cli

import (
	"bufio"
	"bytes"
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"proxyforge/internal/app"
	"proxyforge/internal/domain"
	"proxyforge/internal/system"
)

func TestRetestSNICandidatesCanRunAgainWithoutChangingState(t *testing.T) {
	layout := system.Layout{Root: t.TempDir()}
	store := system.StateStore{Layout: layout}
	wantState := domain.NodeSpec{
		ManagedBy: "proxyforge", Core: domain.CoreXray, Server: "server.example.com",
		SNI: "current.example.com", Target: "current.example.com:443",
	}
	if err := store.Save(wantState); err != nil {
		t.Fatal(err)
	}
	probeCalls := 0
	var out bytes.Buffer
	c := &commandSet{
		app:    &app.App{Store: store},
		reader: bufio.NewReader(strings.NewReader("1\n0\n")),
		out:    &out,
		probeSNI: func(_ context.Context, candidates []string, server string, limit int) ([]app.SNICandidate, error) {
			probeCalls++
			if candidates[0] != wantState.SNI || server != wantState.Server || limit != len(candidates) {
				t.Fatalf("candidates[0]=%q server=%q limit=%d candidates=%d", candidates[0], server, limit, len(candidates))
			}
			return []app.SNICandidate{
				{Domain: "fast.example.com", Latency: 2 * time.Millisecond, TLSVersion: "1.3", ALPN: "h2"},
				{Domain: wantState.SNI, Latency: 5 * time.Millisecond, TLSVersion: "1.3", ALPN: "h2", CertificateSANs: []string{wantState.SNI}},
			}, nil
		},
	}
	if err := c.retestSNICandidates(context.Background(), domain.CoreXray); err != nil {
		t.Fatal(err)
	}
	if probeCalls != 2 {
		t.Fatalf("probe calls=%d, want 2", probeCalls)
	}
	for _, want := range []string{"候选重新检测", "排名第 2", "[当前 SNI]", "重新测试", "不会修改 SNI、target 或重启服务"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("output missing %q: %q", want, out.String())
		}
	}
	gotState, err := store.Load(domain.CoreXray)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(gotState, wantState) {
		t.Fatalf("state changed:\nwant=%#v\ngot=%#v", wantState, gotState)
	}
}

func TestSelectSNICandidateProbesManualOtherDomain(t *testing.T) {
	var out bytes.Buffer
	probeCalls := 0
	c := &commandSet{
		reader: bufio.NewReader(strings.NewReader("0\nother.example.com\nyes\n")),
		out:    &out,
		probeSNI: func(_ context.Context, candidates []string, _ string, limit int) ([]app.SNICandidate, error) {
			probeCalls++
			if probeCalls == 1 {
				return []app.SNICandidate{{Domain: "auto.example.com", Latency: time.Millisecond}}, nil
			}
			if len(candidates) != 1 || candidates[0] != "other.example.com" || limit != 1 {
				t.Fatalf("manual candidates=%v limit=%d", candidates, limit)
			}
			return []app.SNICandidate{{Domain: "other.example.com", Latency: 2 * time.Millisecond, TLSVersion: "1.3"}}, nil
		},
	}
	got, err := c.selectSNICandidate(context.Background(), "server.example.com")
	if err != nil {
		t.Fatal(err)
	}
	if got != "other.example.com" || probeCalls != 2 {
		t.Fatalf("SNI=%q probeCalls=%d", got, probeCalls)
	}
}

func TestFormatCertificateSANsLimitsLongCertificates(t *testing.T) {
	got := formatCertificateSANs([]string{"one.example", "two.example", "three.example", "four.example"}, 3)
	if got != "one.example, two.example, three.example（另有 1 项）" {
		t.Fatalf("SAN summary=%q", got)
	}
}

func TestDefaultSNICandidatesAreUniqueAndValid(t *testing.T) {
	seen := make(map[string]struct{}, len(defaultSNICandidates))
	for _, candidate := range defaultSNICandidates {
		if err := system.ValidateSNI(candidate); err != nil {
			t.Errorf("invalid candidate %q: %v", candidate, err)
		}
		normalized := strings.ToLower(strings.TrimSpace(candidate))
		if _, exists := seen[normalized]; exists {
			t.Errorf("duplicate candidate %q", candidate)
		}
		seen[normalized] = struct{}{}
	}
	if len(seen) < 10 {
		t.Fatalf("candidate pool too small: %d", len(seen))
	}
}
