package cli

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
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

func TestSNIPageBounds(t *testing.T) {
	start, end, pages := sniPageBounds(23, 0, 10)
	if start != 0 || end != 10 || pages != 3 {
		t.Fatalf("page0=%d-%d pages=%d", start, end, pages)
	}
	start, end, pages = sniPageBounds(23, 2, 10)
	if start != 20 || end != 23 || pages != 3 {
		t.Fatalf("page2=%d-%d pages=%d", start, end, pages)
	}
	start, end, pages = sniPageBounds(0, 0, 10)
	if start != 0 || end != 0 || pages != 1 {
		t.Fatalf("empty=%d-%d pages=%d", start, end, pages)
	}
}

func TestParseSNIPageInputJumpsAndSelects(t *testing.T) {
	opts := sniPageChoiceOptions{AllowManual: true}
	action, index, ok := parseSNIPageInput("p3", 25, 0, 3, opts)
	if !ok || action != sniPageActionGoto || index != 2 {
		t.Fatalf("p3 action=%s index=%d ok=%v", action, index, ok)
	}
	action, index, ok = parseSNIPageInput("G", 25, 0, 3, opts)
	if !ok || action != sniPageActionGoto || index != 2 {
		t.Fatalf("G action=%s index=%d ok=%v", action, index, ok)
	}
	if _, _, ok = parseSNIPageInput("21", 25, 0, 2, opts); ok {
		t.Fatal("rank 21 on page 1 should not select")
	}
	action, index, ok = parseSNIPageInput("21", 25, 1, 2, opts)
	if !ok || action != sniPageActionSelect || index != 21 {
		t.Fatalf("21 on page 2 action=%s index=%d ok=%v", action, index, ok)
	}
	action, index, ok = parseSNIPageInput("5", 25, 0, 3, opts)
	if !ok || action != sniPageActionSelect || index != 5 {
		t.Fatalf("5 on page 1 action=%s index=%d ok=%v", action, index, ok)
	}
	if _, _, ok = parseSNIPageInput("p", 25, 0, 3, opts); ok {
		t.Fatal("p on first page should be invalid")
	}
	action, _, ok = parseSNIPageInput("v", 25, 0, 2, opts)
	if !ok || action != sniPageActionSortToggle {
		t.Fatalf("v action=%s ok=%v", action, ok)
	}
	action, _, ok = parseSNIPageInput("v6", 25, 0, 2, opts)
	if !ok || action != sniPageActionSortIPv6 {
		t.Fatalf("v6 action=%s ok=%v", action, ok)
	}
}

func TestSelectSNICandidateCanSwitchIPv6Sort(t *testing.T) {
	var out bytes.Buffer
	c := &commandSet{
		reader: bufio.NewReader(strings.NewReader("v\n1\n")),
		out:    &out,
		probeSNI: func(context.Context, []string, string, int) ([]app.SNICandidate, error) {
			return []app.SNICandidate{
				{
					Domain: "v4-fast.example.com",
					IPv4:   app.FamilyLatency{Present: true, OK: true, Latency: 5 * time.Millisecond},
					IPv6:   app.FamilyLatency{Present: true, OK: true, Latency: 50 * time.Millisecond},
				},
				{
					Domain: "v6-fast.example.com",
					IPv4:   app.FamilyLatency{Present: true, OK: true, Latency: 40 * time.Millisecond},
					IPv6:   app.FamilyLatency{Present: true, OK: true, Latency: 4 * time.Millisecond},
				},
			}, nil
		},
	}
	got, err := c.selectSNICandidate(context.Background(), "server.example.com")
	if err != nil {
		t.Fatal(err)
	}
	if got != "v6-fast.example.com" {
		t.Fatalf("selected=%q", got)
	}
	if !strings.Contains(out.String(), "按 IPv6 延迟排序") {
		t.Fatalf("sort toggle output=%q", out.String())
	}
}

func TestSelectSNICandidatePaginatesAllResults(t *testing.T) {
	results := make([]app.SNICandidate, 25)
	for i := range results {
		results[i] = app.SNICandidate{Domain: fmt.Sprintf("n%d.example.com", i+1), Latency: time.Duration(i+1) * time.Millisecond}
	}
	var out bytes.Buffer
	c := &commandSet{
		reader: bufio.NewReader(strings.NewReader("p2\n21\n")),
		out:    &out,
		probeSNI: func(context.Context, []string, string, int) ([]app.SNICandidate, error) {
			return results, nil
		},
	}
	got, err := c.selectSNICandidate(context.Background(), "server.example.com")
	if err != nil {
		t.Fatal(err)
	}
	if got != "n21.example.com" {
		t.Fatalf("selected=%q", got)
	}
	if !strings.Contains(out.String(), "第 1/2 页，共 25 个") ||
		!strings.Contains(out.String(), "第 2/2 页，共 25 个") ||
		!strings.Contains(out.String(), "n21.example.com") ||
		!strings.Contains(out.String(), "下一页") {
		t.Fatalf("pagination output=%q", out.String())
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

func TestFormatSNILatenciesShowsBothFamilies(t *testing.T) {
	both := formatSNILatencies(app.SNICandidate{
		IPv4: app.FamilyLatency{Present: true, OK: true, Latency: 12 * time.Millisecond},
		IPv6: app.FamilyLatency{Present: true, OK: true, Latency: 28 * time.Millisecond},
	})
	if both != "IPv4 12ms  ·  IPv6 28ms" {
		t.Fatalf("both families=%q", both)
	}
	ipv4Only := formatSNILatencies(app.SNICandidate{
		IPv4: app.FamilyLatency{Present: true, OK: true, Latency: 9 * time.Millisecond},
		IPv6: app.FamilyLatency{Present: false},
	})
	if ipv4Only != "IPv4 9ms  ·  IPv6 无" {
		t.Fatalf("ipv4 only=%q", ipv4Only)
	}
	failed := formatSNILatencies(app.SNICandidate{
		IPv4: app.FamilyLatency{Present: true, OK: false},
		IPv6: app.FamilyLatency{Present: true, OK: true, Latency: 18 * time.Millisecond},
	})
	if failed != "IPv4 失败  ·  IPv6 18ms" {
		t.Fatalf("ipv4 failed=%q", failed)
	}
	legacy := formatSNILatencies(app.SNICandidate{Latency: 5 * time.Millisecond})
	if legacy != "延迟 5ms" {
		t.Fatalf("legacy latency=%q", legacy)
	}
}

func TestPrintSNICandidateSummaryShowsFamilyLatencies(t *testing.T) {
	var out bytes.Buffer
	c := &commandSet{out: &out}
	c.printSNICandidateSummary(1, app.SNICandidate{
		Domain:     "fast.example.com",
		TLSVersion: "1.3",
		ALPN:       "h2",
		CDN:        "未发现明显特征",
		IPv4:       app.FamilyLatency{Present: true, OK: true, Latency: 11 * time.Millisecond},
		IPv6:       app.FamilyLatency{Present: true, OK: true, Latency: 22 * time.Millisecond},
	}, "")
	got := out.String()
	if !strings.Contains(got, "11ms") || !strings.Contains(got, "22ms") || strings.Contains(got, "\n    IPv4") {
		t.Fatalf("summary=%q", got)
	}
}

func TestPrintSNIResultPageAlignsColumns(t *testing.T) {
	var out bytes.Buffer
	c := &commandSet{out: &out}
	c.printSNIResultPage([]app.SNICandidate{
		{Domain: "is1-ssl.mzstatic.com", TLSVersion: "1.3", ALPN: "h2", CDN: "Apple CDN（CNAME）", IPv4: app.FamilyLatency{Present: true, OK: true, Latency: 26 * time.Millisecond}},
		{Domain: "a.b.cdn.console.awsstatic.com", TLSVersion: "1.3", ALPN: "未协商", CDN: "疑似（2 个地址、资源域名）", IPv4: app.FamilyLatency{Present: true, OK: true, Latency: 219 * time.Millisecond}},
	}, 0, "")
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	var rows []string
	for _, line := range lines {
		if strings.Contains(line, "IPv4") {
			rows = append(rows, line)
		}
	}
	if len(rows) != 2 {
		t.Fatalf("rows=%q", out.String())
	}
	left := strings.Index(rows[0], "IPv4")
	if left < 0 || strings.Index(rows[1], "IPv4") != left {
		t.Fatalf("IPv4 columns not aligned:\n%s\n%s", rows[0], rows[1])
	}
	if strings.Index(rows[0], "TLS") != strings.Index(rows[1], "TLS") {
		t.Fatalf("TLS columns not aligned:\n%s\n%s", rows[0], rows[1])
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
