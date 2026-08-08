package cli

import (
	"bufio"
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"proxyforge/internal/app"
	"proxyforge/internal/domain"
	"proxyforge/internal/system"
)

func TestStableCommandTree(t *testing.T) {
	root := New("test")
	for _, args := range [][]string{{"install"}, {"upgrade"}, {"config", "generate"}, {"config", "client"}, {"config", "reset"}, {"service"}} {
		cmd, remaining, err := root.Find(args)
		if err != nil {
			t.Fatalf("find %v: %v", args, err)
		}
		if len(remaining) != 0 {
			t.Fatalf("find %v left %v", args, remaining)
		}
		if cmd.Name() != args[len(args)-1] {
			t.Fatalf("find %v got %s", args, cmd.Name())
		}
	}
}

func TestResetCommandRequiresYesWhenNonInteractive(t *testing.T) {
	var out bytes.Buffer
	c := &commandSet{in: strings.NewReader(""), reader: bufio.NewReader(strings.NewReader("")), out: &out}
	cmd := c.resetCommand()
	cmd.SetArgs([]string{"sing-box"})
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "--yes") {
		t.Fatalf("error = %v, want --yes requirement", err)
	}
}

func TestCredentialResetConfirmationWarnsAboutOldClients(t *testing.T) {
	var out bytes.Buffer
	c := &commandSet{reader: bufio.NewReader(strings.NewReader("yes\n")), out: &out}
	confirmed, err := c.confirmCredentialReset("xray", domain.ResetOptions{SNI: "www.example.com", Target: "www.example.com:443"})
	if err != nil {
		t.Fatal(err)
	}
	if !confirmed || !strings.Contains(out.String(), "所有旧客户端配置会立即失效") {
		t.Fatalf("confirmed=%v output=%q", confirmed, out.String())
	}
}

func TestFillResetDefaultsTargetToNewSNI(t *testing.T) {
	store := system.StateStore{Layout: system.Layout{Root: t.TempDir()}}
	if err := store.Save(domain.NodeSpec{ManagedBy: "proxyforge", Core: domain.CoreSingBox, SNI: "old.example.com", Target: "old.example.com:443"}); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	c := &commandSet{
		app:    &app.App{Store: store},
		reader: bufio.NewReader(strings.NewReader("new.example.com\n\n")),
		out:    &out,
	}
	opts := domain.ResetOptions{}
	if err := c.fillReset(context.Background(), domain.CoreSingBox, &opts); err != nil {
		t.Fatal(err)
	}
	if opts.SNI != "new.example.com" || opts.Target != "new.example.com:443" {
		t.Fatalf("reset options = %#v", opts)
	}
}

func TestFillResetUsesSameAutomaticSNICandidatesAsGenerate(t *testing.T) {
	store := system.StateStore{Layout: system.Layout{Root: t.TempDir()}}
	if err := store.Save(domain.NodeSpec{ManagedBy: "proxyforge", Core: domain.CoreSingBox, Server: "server.example.com", SNI: "old.example.com", Target: "old.example.com:443"}); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	c := &commandSet{
		app:    &app.App{Store: store},
		reader: bufio.NewReader(strings.NewReader("\n\n\n")),
		out:    &out,
		probeSNI: func(_ context.Context, candidates []string, server string, limit int) ([]app.SNICandidate, error) {
			if len(candidates) < 10 || server != "server.example.com" || limit != 10 {
				t.Fatalf("probe candidates=%d server=%q limit=%d", len(candidates), server, limit)
			}
			return []app.SNICandidate{{Domain: "new.example.com", Latency: 4 * time.Millisecond, TLSVersion: "1.3", ALPN: "h2", CertificateSANs: []string{"new.example.com"}, CDN: "未发现明显特征"}}, nil
		},
		randomIndex: func(size int) int { return 0 },
	}
	opts := domain.ResetOptions{}
	if err := c.fillReset(context.Background(), domain.CoreSingBox, &opts); err != nil {
		t.Fatal(err)
	}
	if opts.SNI != "new.example.com" || opts.Target != "new.example.com:443" {
		t.Fatalf("reset options = %#v", opts)
	}
	if !strings.Contains(out.String(), "TLS=1.3") || !strings.Contains(out.String(), "证书 SAN=new.example.com") {
		t.Fatalf("candidate output=%q", out.String())
	}
}

func TestConfirmCredentialOnlyResetPreservesTarget(t *testing.T) {
	store := system.StateStore{Layout: system.Layout{Root: t.TempDir()}}
	if err := store.Save(domain.NodeSpec{ManagedBy: "proxyforge", Core: domain.CoreXray, SNI: "keep.example.com", Target: "origin.example.com:443"}); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	c := &commandSet{
		app:    &app.App{Store: store},
		reader: bufio.NewReader(strings.NewReader("yes\n")),
		out:    &out,
	}
	confirmed, err := c.confirmCredentialOnlyReset(domain.CoreXray)
	if err != nil {
		t.Fatal(err)
	}
	if !confirmed || !strings.Contains(out.String(), "SNI 和 target 保持不变：keep.example.com，origin.example.com:443") {
		t.Fatalf("confirmed=%v output=%q", confirmed, out.String())
	}
}

func TestChooseNumberRetriesInvalidInput(t *testing.T) {
	var out bytes.Buffer
	c := &commandSet{reader: bufio.NewReader(strings.NewReader("abc\n9\n2\n")), out: &out}
	choice, err := c.chooseNumber("请选择", 0, 5, 0)
	if err != nil {
		t.Fatal(err)
	}
	if choice != 2 {
		t.Fatalf("choice = %d, want 2", choice)
	}
	if count := strings.Count(out.String(), "无效选择"); count != 2 {
		t.Fatalf("invalid message count = %d, output=%q", count, out.String())
	}
}

func TestSelectCoreUsesNumericChoiceAndDefault(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{name: "default sing-box", input: "\n", want: "sing-box"},
		{name: "xray", input: "2\n", want: "xray"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var out bytes.Buffer
			c := &commandSet{reader: bufio.NewReader(strings.NewReader(tt.input)), out: &out}
			got, selected, err := c.selectCore()
			if err != nil {
				t.Fatal(err)
			}
			if !selected || got != tt.want {
				t.Fatalf("core=%q selected=%v, want %q", got, selected, tt.want)
			}
		})
	}
}

func TestMenuCanReturnFromCoreSelection(t *testing.T) {
	var out, errOut bytes.Buffer
	c := &commandSet{
		reader: bufio.NewReader(strings.NewReader("4\n0\n0\n")),
		out:    &out,
		errOut: &errOut,
	}
	if err := c.menu(context.Background()); err != nil {
		t.Fatal(err)
	}
	if strings.Count(out.String(), "ProxyForge 双内核代理管理器") != 2 {
		t.Fatalf("main menu was not shown twice: %q", out.String())
	}
	if !strings.Contains(out.String(), "已退出 ProxyForge") {
		t.Fatalf("missing exit message: %q", out.String())
	}
	if strings.Contains(out.String(), "\x1b[") {
		t.Fatalf("redirected menu output contains ANSI controls: %q", out.String())
	}
}

func TestScreenControlsAreDisabledForRedirectedIO(t *testing.T) {
	input := strings.NewReader("kept\n")
	var out bytes.Buffer
	c := &commandSet{in: input, reader: bufio.NewReader(input), out: &out}
	c.clearScreen()
	c.pauseForMenu()
	if out.Len() != 0 {
		t.Fatalf("redirected output=%q", out.String())
	}
	line, err := c.reader.ReadString('\n')
	if err != nil || line != "kept\n" {
		t.Fatalf("redirected input was consumed: line=%q err=%v", line, err)
	}
}

func TestFillGenerateSelectsRandomDefaultFromFastCandidates(t *testing.T) {
	var out bytes.Buffer
	c := &commandSet{
		reader: bufio.NewReader(strings.NewReader("\n\nyes\n")),
		out:    &out,
		probeSNI: func(_ context.Context, candidates []string, server string, limit int) ([]app.SNICandidate, error) {
			if len(candidates) < 10 || server != "server.example.com" || limit != 10 {
				t.Fatalf("probe candidates=%d server=%q limit=%d", len(candidates), server, limit)
			}
			return []app.SNICandidate{
				{Domain: "fast.example.com", Latency: 5 * time.Millisecond, TLSVersion: "1.3", ALPN: "h2", CertificateSANs: []string{"fast.example.com", "*.example.com"}, CDN: "Akamai（CNAME）"},
				{Domain: "second.example.com", Latency: 8 * time.Millisecond, TLSVersion: "1.2", ALPN: "http/1.1", CertificateSANs: []string{"second.example.com"}, CDN: "未发现明显特征"},
			}, nil
		},
		randomIndex: func(size int) int {
			if size != 2 {
				t.Fatalf("random size=%d", size)
			}
			return 1
		},
	}
	opts := domain.GenerateOptions{Server: "server.example.com", Port: 443}
	if err := c.fillGenerate(context.Background(), domain.CoreSingBox, &opts); err != nil {
		t.Fatal(err)
	}
	if opts.SNI != "second.example.com" || opts.Target != "second.example.com:443" {
		t.Fatalf("generate options=%#v", opts)
	}
	if !strings.Contains(out.String(), "最快的候选域名") || !strings.Contains(out.String(), "[2]") ||
		!strings.Contains(out.String(), "TLS=1.3") || !strings.Contains(out.String(), "ALPN=h2") ||
		!strings.Contains(out.String(), "证书 SAN=fast.example.com, *.example.com") || !strings.Contains(out.String(), "CDN=Akamai（CNAME）") {
		t.Fatalf("candidate menu output=%q", out.String())
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
