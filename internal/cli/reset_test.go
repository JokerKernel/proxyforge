package cli

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"proxyforge/internal/app"
	"proxyforge/internal/domain"
	"proxyforge/internal/system"
)

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
	if !confirmed || !strings.Contains(out.String(), "所有旧客户端配置中的 SNI/target 会立即失效") || !strings.Contains(out.String(), "手动配置会保留") {
		t.Fatalf("confirmed=%v output=%q", confirmed, out.String())
	}
}

func TestModifyConfigResetSNIQReturnsWithoutError(t *testing.T) {
	store := system.StateStore{Layout: system.Layout{Root: t.TempDir()}}
	if err := store.Save(domain.NodeSpec{
		ManagedBy: "proxyforge", Core: domain.CoreSingBox, Server: "server.example.com",
		SNI: "old.example.com", Target: "old.example.com:443",
	}); err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	c := &commandSet{
		app:    &app.App{Store: store},
		reader: bufio.NewReader(strings.NewReader("3\n\nq\n0\n0\n")),
		out:    &out,
		errOut: &errOut,
		probeSNI: func(context.Context, []string, string, int) ([]app.SNICandidate, error) {
			return []app.SNICandidate{{Domain: "fast.example.com", Latency: time.Millisecond}}, nil
		},
	}
	if err := c.modifyConfigMenu(context.Background(), domain.CoreSingBox); err != nil {
		t.Fatal(err)
	}
	if errOut.Len() != 0 {
		t.Fatalf("q reported as error: %q", errOut.String())
	}
	if strings.Contains(out.String(), "操作失败") {
		t.Fatalf("q treated as failure: %q", out.String())
	}
	if strings.Count(out.String(), "新的 REALITY SNI（输入域名；直接回车自动测速候选）") != 2 {
		t.Fatalf("candidate cancellation did not return to SNI input: %q", out.String())
	}
	current, err := store.Load(domain.CoreSingBox)
	if err != nil {
		t.Fatal(err)
	}
	if current.SNI != "old.example.com" || current.Target != "old.example.com:443" {
		t.Fatalf("canceled reset changed node: %#v", current)
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
		reader: bufio.NewReader(strings.NewReader("new.example.com\nyes\n\n")),
		out:    &out,
		probeSNI: func(_ context.Context, candidates []string, server string, limit int) ([]app.SNICandidate, error) {
			if len(candidates) != 1 || candidates[0] != "new.example.com" || limit != 1 {
				t.Fatalf("candidates=%v server=%q limit=%d", candidates, server, limit)
			}
			return []app.SNICandidate{{Domain: "new.example.com", Latency: 3 * time.Millisecond, TLSVersion: "1.3", CertificateSANs: []string{"new.example.com"}}}, nil
		},
	}
	opts := domain.ResetOptions{}
	if err := c.fillReset(context.Background(), domain.CoreSingBox, &opts); err != nil {
		t.Fatal(err)
	}
	if opts.SNI != "new.example.com" || opts.Target != "new.example.com:443" {
		t.Fatalf("reset options = %#v", opts)
	}
	if !strings.Contains(out.String(), "手动 SNI 检测结果") || !strings.Contains(out.String(), "确认采用这个手动 SNI") {
		t.Fatalf("manual reset SNI was not checked: %q", out.String())
	}
}

func TestFillResetStopsOnCanceledOrEndedInput(t *testing.T) {
	for _, tt := range []struct {
		name, input, sni string
		wantErr          error
	}{
		{"SNI EOF", "", "", io.EOF},
		{"SNI cancel", "q\n", "", errReturnToMenu},
		{"target EOF", "yes\n", "new.example.com", io.EOF},
		{"target cancel", "yes\nq\n", "new.example.com", errReturnToMenu},
	} {
		t.Run(tt.name, func(t *testing.T) {
			store := system.StateStore{Layout: system.Layout{Root: t.TempDir()}}
			if err := store.Save(domain.NodeSpec{ManagedBy: "proxyforge", Core: domain.CoreXray, SNI: "old.example.com", Target: "old.example.com:443"}); err != nil {
				t.Fatal(err)
			}
			probed := false
			c := &commandSet{
				app: &app.App{Store: store}, reader: bufio.NewReader(strings.NewReader(tt.input)), out: io.Discard,
				probeSNI: func(_ context.Context, candidates []string, _ string, _ int) ([]app.SNICandidate, error) {
					probed = true
					if len(candidates) != 1 || candidates[0] != tt.sni {
						t.Fatalf("unexpected SNI probe: %v", candidates)
					}
					return []app.SNICandidate{{Domain: tt.sni}}, nil
				},
			}
			opts := domain.ResetOptions{SNI: tt.sni}
			if err := c.fillReset(context.Background(), domain.CoreXray, &opts); !errors.Is(err, tt.wantErr) {
				t.Fatalf("error = %v; want %v", err, tt.wantErr)
			}
			if tt.sni == "" && probed {
				t.Fatal("SNI probing continued after input ended or was canceled")
			}
			if opts.Target != "" {
				t.Fatalf("reset target filled after input ended or was canceled: %s", opts.Target)
			}
		})
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
		reader: bufio.NewReader(strings.NewReader("\n1\n\n")),
		out:    &out,
		probeSNI: func(_ context.Context, candidates []string, server string, limit int) ([]app.SNICandidate, error) {
			if len(candidates) < 10 || server != "server.example.com" || limit != len(defaultSNICandidates) {
				t.Fatalf("probe candidates=%d server=%q limit=%d", len(candidates), server, limit)
			}
			return []app.SNICandidate{{Domain: "new.example.com", Latency: 4 * time.Millisecond, TLSVersion: "1.3", ALPN: "h2", CertificateSANs: []string{"new.example.com"}, CDN: "未发现明显特征"}}, nil
		},
	}
	opts := domain.ResetOptions{}
	if err := c.fillReset(context.Background(), domain.CoreSingBox, &opts); err != nil {
		t.Fatal(err)
	}
	if opts.SNI != "new.example.com" || opts.Target != "new.example.com:443" {
		t.Fatalf("reset options = %#v", opts)
	}
	if !strings.Contains(out.String(), "TLS 1.3 / h2") || !strings.Contains(out.String(), "均已通过 DNS、TLS 和证书名称校验") {
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
	if !confirmed || !strings.Contains(out.String(), "SNI 和 target 保持不变：keep.example.com，origin.example.com:443") || !strings.Contains(out.String(), "手动配置会保留") {
		t.Fatalf("confirmed=%v output=%q", confirmed, out.String())
	}
}

func TestTLSResetConfirmationExplainsCertificateChange(t *testing.T) {
	var out bytes.Buffer
	c := &commandSet{reader: bufio.NewReader(strings.NewReader("yes\n")), out: &out}
	ok, err := c.confirmTLSReset(domain.CoreXray, domain.ResetOptions{RotateCertificate: true})
	if err != nil || !ok || !strings.Contains(out.String(), "保留 UUID") || !strings.Contains(out.String(), "中转连接需要更新") {
		t.Fatalf("ok=%v err=%v output=%s", ok, err, out.String())
	}
}
