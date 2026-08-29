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
	"proxyforge/internal/provider"
	"proxyforge/internal/provider/singbox"
	"proxyforge/internal/provider/xray"
	"proxyforge/internal/system"
)

func TestGenerateCommandOffersUserNameAndInboundTag(t *testing.T) {
	c := &commandSet{}
	cmd := c.generateCommand()
	for _, name := range []string{"user-name", "inbound-tag"} {
		flag := cmd.Flags().Lookup(name)
		if flag == nil || flag.DefValue != "" {
			t.Fatalf("%s flag=%v", name, flag)
		}
	}
	flag := cmd.Flags().Lookup("simplified-config")
	if flag == nil || flag.DefValue != "false" {
		t.Fatalf("simplified-config flag=%v", flag)
	}
	for _, name := range []string{"standard-config", "sing-box-fallback-guard", "sing-box-fallback-port", "sing-box-fallback-http-domain", "sing-box-fallback-exact-domain", "xray-fallback-guard", "xray-fallback-port", "xray-fallback-http-domain", "xray-fallback-exact-domain"} {
		if flag := cmd.Flags().Lookup(name); flag == nil {
			t.Fatalf("missing %s flag", name)
		}
	}
}

func TestPrintGenerateSuccessDisplaysNodeInformation(t *testing.T) {
	var out bytes.Buffer
	printGenerateSuccess(&out, domain.NodeSpec{
		Core: domain.CoreSingBox, Server: "2001:db8::10", Port: 443,
		SNI: "www.example.com", Target: "www.example.com:443",
		UserName: "phone", InboundTag: "phone-in", CoreVersion: "sing-box version 1.13.16",
	})

	for _, want := range []string{
		"sing-box 服务端配置生成成功",
		"服务状态：active（运行中）",
		"开机启动：enabled（已启用）",
		"连接地址：[2001:db8::10]:443",
		"REALITY SNI：www.example.com",
		"REALITY target：www.example.com:443",
		"用户名称：phone",
		"入站标签：phone-in",
		"配置模式：标准安全配置",
		"内核版本：sing-box version 1.13.16",
		"客户端配置",
	} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("success output missing %q: %q", want, out.String())
		}
	}
}

func TestExistingServerConfigRequiresOverwriteConfirmation(t *testing.T) {
	layout := system.Layout{Root: t.TempDir()}
	if err := system.AtomicWrite(layout.Resolve(singbox.New().ConfigPath()), []byte("existing"), 0600); err != nil {
		t.Fatal(err)
	}
	a := &app.App{
		Registry:  provider.NewRegistry(singbox.New(), xray.New()),
		Layout:    layout,
		RootCheck: func() error { return nil },
	}
	markCoreInstalled(a)
	t.Run("interactive cancel", func(t *testing.T) {
		var out bytes.Buffer
		c := &commandSet{app: a, reader: bufio.NewReader(strings.NewReader("q\n")), out: &out}
		err := c.confirmServerConfigOverwrite(domain.CoreSingBox, true)
		if !errors.Is(err, errReturnToMenu) || !strings.Contains(out.String(), "完整覆盖") {
			t.Fatalf("error=%v output=%q", err, out.String())
		}
	})
	t.Run("interactive confirm", func(t *testing.T) {
		c := &commandSet{app: a, reader: bufio.NewReader(strings.NewReader("yes\n")), out: io.Discard}
		if err := c.confirmServerConfigOverwrite(domain.CoreSingBox, true); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("menu cancel returns to server config menu", func(t *testing.T) {
		var out, errOut bytes.Buffer
		c := &commandSet{
			app:    a,
			reader: bufio.NewReader(strings.NewReader("1\nq\n0\n")),
			out:    &out,
			errOut: &errOut,
		}
		if err := c.serverConfigMenu(context.Background(), domain.CoreSingBox); err != nil {
			t.Fatal(err)
		}
		if count := strings.Count(out.String(), "ProxyForge  ›  sing-box  ›  服务端配置"); count != 2 {
			t.Fatalf("server config menu count=%d, want 2; output=%q", count, out.String())
		}
		for _, text := range []string{"Q/0=返回", "已取消生成服务端配置，返回服务端配置菜单"} {
			if !strings.Contains(out.String(), text) {
				t.Fatalf("cancel output missing %q: %q", text, out.String())
			}
		}
		if errOut.Len() != 0 {
			t.Fatalf("cancel was reported as an error: %q", errOut.String())
		}
	})
	t.Run("non-interactive requires yes", func(t *testing.T) {
		c := &commandSet{app: a, out: io.Discard}
		if err := c.confirmServerConfigOverwrite(domain.CoreSingBox, false); err == nil || !strings.Contains(err.Error(), "--yes") {
			t.Fatalf("error=%v", err)
		}
		c.yes = true
		if err := c.confirmServerConfigOverwrite(domain.CoreSingBox, false); err != nil {
			t.Fatal(err)
		}
	})
}

func TestFillGenerateRequiresExplicitFastCandidateSelection(t *testing.T) {
	var out bytes.Buffer
	c := &commandSet{
		reader: bufio.NewReader(strings.NewReader("\n\n\n\n2\nyes\n")),
		out:    &out,
		probeSNI: func(_ context.Context, candidates []string, server string, limit int) ([]app.SNICandidate, error) {
			if len(candidates) < 10 || server != "server.example.com" || limit != len(defaultSNICandidates) {
				t.Fatalf("probe candidates=%d server=%q limit=%d", len(candidates), server, limit)
			}
			return []app.SNICandidate{
				{Domain: "fast.example.com", Latency: 5 * time.Millisecond, TLSVersion: "1.3", ALPN: "h2", CertificateSANs: []string{"fast.example.com", "*.example.com"}, CDN: "Akamai（CNAME）"},
				{Domain: "second.example.com", Latency: 8 * time.Millisecond, TLSVersion: "1.2", ALPN: "http/1.1", CertificateSANs: []string{"second.example.com"}, CDN: "未发现明显特征"},
			}, nil
		},
	}
	opts := domain.GenerateOptions{Server: "server.example.com", Port: 443, StandardConfig: true}
	if err := c.fillGenerate(context.Background(), domain.CoreSingBox, &opts); err != nil {
		t.Fatal(err)
	}
	if opts.SNI != "second.example.com" || opts.Target != "second.example.com:443" || opts.UserName != domain.DefaultUserName || opts.InboundTag != domain.DefaultInboundTag(domain.CoreSingBox) {
		t.Fatalf("generate options=%#v", opts)
	}
	if !strings.Contains(out.String(), "全部有效候选（按 IPv4 延迟排序）") || !strings.Contains(out.String(), "second.example.com") ||
		!strings.Contains(out.String(), "第 1/1 页") || !strings.Contains(out.String(), "TLS 1.3 / h2") ||
		!strings.Contains(out.String(), "Akamai（CNAME）") || !strings.Contains(out.String(), "均已通过 DNS、TLS 和证书名称校验") ||
		strings.Contains(out.String(), "[默认]") || strings.Contains(out.String(), "证书 SAN=fast.example.com") {
		t.Fatalf("candidate menu output=%q", out.String())
	}
}

func TestFillGenerateOffersExistingSNI(t *testing.T) {
	store := system.StateStore{Layout: system.Layout{Root: t.TempDir()}}
	if err := store.Save(domain.NodeSpec{
		ManagedBy: "proxyforge", Core: domain.CoreSingBox,
		SNI: "old.example.com", Target: "origin.example.com:8443",
	}); err != nil {
		t.Fatal(err)
	}
	probeCalls := 0
	var out bytes.Buffer
	c := &commandSet{
		app:    &app.App{Store: store},
		reader: bufio.NewReader(strings.NewReader("\n\n\n\nyes\n")),
		out:    &out,
		probeSNI: func(context.Context, []string, string, int) ([]app.SNICandidate, error) {
			probeCalls++
			return nil, errors.New("should not probe when reusing SNI")
		},
	}
	opts := domain.GenerateOptions{Server: "server.example.com", Port: 443, StandardConfig: true}
	if err := c.fillGenerate(context.Background(), domain.CoreSingBox, &opts); err != nil {
		t.Fatal(err)
	}
	if probeCalls != 0 || opts.SNI != "old.example.com" || opts.Target != "origin.example.com:8443" {
		t.Fatalf("probeCalls=%d options=%#v", probeCalls, opts)
	}
	if !strings.Contains(out.String(), "当前已配置 REALITY SNI：old.example.com") ||
		!strings.Contains(out.String(), "使用原有 SNI") ||
		!strings.Contains(out.String(), "重新输入或自动测速") {
		t.Fatalf("existing SNI prompt missing: %q", out.String())
	}
}

func TestFillGenerateCanSkipExistingSNIAndSpeedTest(t *testing.T) {
	store := system.StateStore{Layout: system.Layout{Root: t.TempDir()}}
	if err := store.Save(domain.NodeSpec{
		ManagedBy: "proxyforge", Core: domain.CoreSingBox, SNI: "old.example.com", Target: "old.example.com:443",
	}); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	c := &commandSet{
		app:    &app.App{Store: store},
		reader: bufio.NewReader(strings.NewReader("\n\n\n2\n\n1\nyes\n")),
		out:    &out,
		probeSNI: func(_ context.Context, candidates []string, server string, limit int) ([]app.SNICandidate, error) {
			return []app.SNICandidate{{Domain: "fast.example.com", Latency: time.Millisecond, TLSVersion: "1.3"}}, nil
		},
	}
	opts := domain.GenerateOptions{Server: "server.example.com", Port: 443, StandardConfig: true}
	if err := c.fillGenerate(context.Background(), domain.CoreSingBox, &opts); err != nil {
		t.Fatal(err)
	}
	if opts.SNI != "fast.example.com" || opts.Target != "fast.example.com:443" {
		t.Fatalf("generate options=%#v", opts)
	}
}

func TestFillGenerateSelectsSimplifiedSingBoxConfig(t *testing.T) {
	var out bytes.Buffer
	c := &commandSet{
		reader: bufio.NewReader(strings.NewReader("2\n\n\nyes\nyes\n")),
		out:    &out,
		probeSNI: func(_ context.Context, candidates []string, server string, limit int) ([]app.SNICandidate, error) {
			return []app.SNICandidate{{Domain: candidates[0], Latency: 5 * time.Millisecond, TLSVersion: "1.3", ALPN: "h2", CertificateSANs: []string{candidates[0]}, CDN: "未发现明显特征"}}, nil
		},
	}
	opts := domain.GenerateOptions{
		Server: "server.example.com", Port: 443,
		SNI: "www.example.com", Target: "www.example.com:443",
	}
	if err := c.fillGenerate(context.Background(), domain.CoreSingBox, &opts); err != nil {
		t.Fatal(err)
	}
	if !opts.SimplifiedConfig {
		t.Fatalf("generate options=%#v", opts)
	}
	if !strings.Contains(out.String(), "DNS 日志较少") || !strings.Contains(out.String(), "可能绕过拦截") {
		t.Fatalf("simplified warning missing: %q", out.String())
	}
}

func TestFillGenerateSelectsXrayFallbackGuardConfig(t *testing.T) {
	var out bytes.Buffer
	c := &commandSet{
		reader: bufio.NewReader(strings.NewReader("\n\n\n\n\n\nyes\nyes\n")),
		out:    &out,
		probeSNI: func(_ context.Context, candidates []string, server string, limit int) ([]app.SNICandidate, error) {
			return []app.SNICandidate{{Domain: candidates[0], Latency: 5 * time.Millisecond, TLSVersion: "1.3", CertificateSANs: []string{candidates[0]}}}, nil
		},
	}
	opts := domain.GenerateOptions{
		Server: "server.example.com", Port: 443, SNI: "speed.cloudflare.com", Target: "speed.cloudflare.com:443",
	}
	if err := c.fillGenerate(context.Background(), domain.CoreXray, &opts); err != nil {
		t.Fatal(err)
	}
	if !opts.XrayFallbackGuard || opts.XrayFallbackPort < domain.FallbackPortMin || opts.XrayFallbackPort > domain.FallbackPortMax || opts.XrayFallbackPort == opts.Port || opts.XrayFallbackHTTPDomain || opts.XrayFallbackExactDomain {
		t.Fatalf("generate options=%#v", opts)
	}
	for _, want := range []string{"回落防护", "dokodemo-door", "本机 dokodemo-door 回落端口", "不限制 Host       -- 默认", "严格匹配回落域名", "允许子域名        -- 默认", "  2   严格匹配"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("output missing %q: %q", want, out.String())
		}
	}
}

func TestFillGenerateEnablesXrayFallbackHTTPDomain(t *testing.T) {
	var out bytes.Buffer
	c := &commandSet{
		reader: bufio.NewReader(strings.NewReader("\n2\n\n\n\n\nyes\nyes\n")),
		out:    &out,
		probeSNI: func(_ context.Context, candidates []string, server string, limit int) ([]app.SNICandidate, error) {
			return []app.SNICandidate{{Domain: candidates[0], Latency: 5 * time.Millisecond, TLSVersion: "1.3", CertificateSANs: []string{candidates[0]}}}, nil
		},
	}
	opts := domain.GenerateOptions{
		Server: "server.example.com", Port: 443, SNI: "speed.cloudflare.com", Target: "speed.cloudflare.com:443",
	}
	if err := c.fillGenerate(context.Background(), domain.CoreXray, &opts); err != nil {
		t.Fatal(err)
	}
	if !opts.XrayFallbackGuard || !opts.XrayFallbackHTTPDomain || opts.XrayFallbackExactDomain {
		t.Fatalf("generate options=%#v", opts)
	}
	if !strings.Contains(out.String(), "Host 匹配 SNI") {
		t.Fatalf("HTTP domain option missing: %q", out.String())
	}
}

func TestFillGenerateEnablesXrayFallbackExactDomainIndependently(t *testing.T) {
	var out bytes.Buffer
	c := &commandSet{
		reader: bufio.NewReader(strings.NewReader("\n1\n2\n\n\n\n\nyes\nyes\n")),
		out:    &out,
		probeSNI: func(_ context.Context, candidates []string, server string, limit int) ([]app.SNICandidate, error) {
			return []app.SNICandidate{{Domain: candidates[0], Latency: 5 * time.Millisecond, TLSVersion: "1.3", CertificateSANs: []string{candidates[0]}}}, nil
		},
	}
	opts := domain.GenerateOptions{
		Server: "server.example.com", Port: 443, SNI: "speed.cloudflare.com", Target: "speed.cloudflare.com:443",
	}
	if err := c.fillGenerate(context.Background(), domain.CoreXray, &opts); err != nil {
		t.Fatal(err)
	}
	if !opts.XrayFallbackGuard || opts.XrayFallbackHTTPDomain || !opts.XrayFallbackExactDomain {
		t.Fatalf("generate options=%#v", opts)
	}
	if !strings.Contains(out.String(), "严格匹配回落域名") {
		t.Fatalf("exact domain option missing: %q", out.String())
	}
}

func TestFillGenerateSelectsSingBoxFallbackGuardConfig(t *testing.T) {
	var out bytes.Buffer
	c := &commandSet{
		reader: bufio.NewReader(strings.NewReader("\n\n\n\n\n\nyes\nyes\n")),
		out:    &out,
		probeSNI: func(_ context.Context, candidates []string, server string, limit int) ([]app.SNICandidate, error) {
			return []app.SNICandidate{{Domain: candidates[0], Latency: 5 * time.Millisecond, TLSVersion: "1.3", CertificateSANs: []string{candidates[0]}}}, nil
		},
	}
	opts := domain.GenerateOptions{
		Server: "server.example.com", Port: 443, SNI: "speed.cloudflare.com", Target: "speed.cloudflare.com:443",
	}
	if err := c.fillGenerate(context.Background(), domain.CoreSingBox, &opts); err != nil {
		t.Fatal(err)
	}
	if !opts.SingBoxFallbackGuard || opts.SingBoxFallbackPort < domain.FallbackPortMin || opts.SingBoxFallbackPort > domain.FallbackPortMax || opts.SingBoxFallbackPort == opts.Port || opts.SingBoxFallbackHTTPDomain || opts.SingBoxFallbackExactDomain || opts.SimplifiedConfig {
		t.Fatalf("generate options=%#v", opts)
	}
	for _, want := range []string{"回落防护", "direct 入站", "本机 direct 回落端口", "不限制 Host       -- 默认", "严格匹配回落域名", "允许子域名        -- 默认", "  2   严格匹配"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("output missing %q: %q", want, out.String())
		}
	}
}

func TestFillGenerateEnablesSingBoxFallbackHTTPDomain(t *testing.T) {
	var out bytes.Buffer
	c := &commandSet{
		reader: bufio.NewReader(strings.NewReader("\n2\n\n\n\n\nyes\nyes\n")),
		out:    &out,
		probeSNI: func(_ context.Context, candidates []string, server string, limit int) ([]app.SNICandidate, error) {
			return []app.SNICandidate{{Domain: candidates[0], Latency: 5 * time.Millisecond, TLSVersion: "1.3", CertificateSANs: []string{candidates[0]}}}, nil
		},
	}
	opts := domain.GenerateOptions{
		Server: "server.example.com", Port: 443, SNI: "speed.cloudflare.com", Target: "speed.cloudflare.com:443",
	}
	if err := c.fillGenerate(context.Background(), domain.CoreSingBox, &opts); err != nil {
		t.Fatal(err)
	}
	if !opts.SingBoxFallbackGuard || !opts.SingBoxFallbackHTTPDomain || opts.SingBoxFallbackExactDomain {
		t.Fatalf("generate options=%#v", opts)
	}
	if !strings.Contains(out.String(), "Host 匹配 SNI") {
		t.Fatalf("HTTP domain option missing: %q", out.String())
	}
}

func TestFillGenerateEnablesSingBoxFallbackExactDomainIndependently(t *testing.T) {
	var out bytes.Buffer
	c := &commandSet{
		reader: bufio.NewReader(strings.NewReader("\n1\n2\n\n\n\n\nyes\nyes\n")),
		out:    &out,
		probeSNI: func(_ context.Context, candidates []string, server string, limit int) ([]app.SNICandidate, error) {
			return []app.SNICandidate{{Domain: candidates[0], Latency: 5 * time.Millisecond, TLSVersion: "1.3", CertificateSANs: []string{candidates[0]}}}, nil
		},
	}
	opts := domain.GenerateOptions{
		Server: "server.example.com", Port: 443, SNI: "speed.cloudflare.com", Target: "speed.cloudflare.com:443",
	}
	if err := c.fillGenerate(context.Background(), domain.CoreSingBox, &opts); err != nil {
		t.Fatal(err)
	}
	if !opts.SingBoxFallbackGuard || opts.SingBoxFallbackHTTPDomain || !opts.SingBoxFallbackExactDomain {
		t.Fatalf("generate options=%#v", opts)
	}
	if !strings.Contains(out.String(), "严格匹配回落域名") {
		t.Fatalf("exact domain option missing: %q", out.String())
	}
}

func TestFillGenerateProbesManualSNIAndConfirmsTwice(t *testing.T) {
	var out bytes.Buffer
	probeCalls := 0
	c := &commandSet{
		reader: bufio.NewReader(strings.NewReader("\n\n\nmanual.example.com\nyes\nyes\n")),
		out:    &out,
		probeSNI: func(_ context.Context, candidates []string, server string, limit int) ([]app.SNICandidate, error) {
			probeCalls++
			if len(candidates) != 1 || candidates[0] != "manual.example.com" || server != "server.example.com" || limit != 1 {
				t.Fatalf("candidates=%v server=%q limit=%d", candidates, server, limit)
			}
			return []app.SNICandidate{{
				Domain: "manual.example.com", Latency: 7 * time.Millisecond, TLSVersion: "1.3", ALPN: "h2",
				CertificateSANs: []string{"manual.example.com", "*.example.com"}, CDN: "测试 CDN",
			}}, nil
		},
	}
	opts := domain.GenerateOptions{Server: "server.example.com", Port: 443, StandardConfig: true}
	if err := c.fillGenerate(context.Background(), domain.CoreSingBox, &opts); err != nil {
		t.Fatal(err)
	}
	if probeCalls != 1 || opts.SNI != "manual.example.com" || opts.Target != "manual.example.com:443" {
		t.Fatalf("probeCalls=%d options=%#v", probeCalls, opts)
	}
	for _, want := range []string{
		"正在检测手动 SNI", "TLS=1.3", "ALPN=h2", "证书 SAN=manual.example.com, *.example.com", "CDN=测试 CDN",
		"确认采用这个手动 SNI", "确认 SNI 和 REALITY target",
	} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("manual SNI output missing %q: %q", want, out.String())
		}
	}
}

func TestSelectPublicAddressDefaultsToPhysicalInterface(t *testing.T) {
	var out bytes.Buffer
	externalCalled := false
	c := &commandSet{
		reader: bufio.NewReader(strings.NewReader("\n")), out: &out,
		physicalIPs: func() ([]app.PublicInterfaceAddress, error) {
			return []app.PublicInterfaceAddress{{Interface: "eth0", Address: "198.51.100.10"}}, nil
		},
		externalIP: func(context.Context) (string, error) {
			externalCalled = true
			return "203.0.113.10", nil
		},
	}
	got, err := c.selectPublicAddress(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got != "198.51.100.10" || externalCalled {
		t.Fatalf("address=%q externalCalled=%v", got, externalCalled)
	}
	if !strings.Contains(out.String(), "物理网卡          -- 默认") {
		t.Fatalf("method menu=%q", out.String())
	}
}

func TestSelectPublicAddressCanUseExternalService(t *testing.T) {
	var out bytes.Buffer
	c := &commandSet{
		reader: bufio.NewReader(strings.NewReader("2\n")), out: &out,
		physicalIPs: func() ([]app.PublicInterfaceAddress, error) {
			return []app.PublicInterfaceAddress{{Interface: "eth0", Address: "198.51.100.10"}}, nil
		},
		externalIP: func(context.Context) (string, error) { return "203.0.113.10", nil },
	}
	got, err := c.selectPublicAddress(context.Background())
	if err != nil || got != "203.0.113.10" {
		t.Fatalf("address=%q error=%v", got, err)
	}
}

func TestSelectPublicAddressCanUseManualInput(t *testing.T) {
	var out bytes.Buffer
	detectorCalled := false
	c := &commandSet{
		reader: bufio.NewReader(strings.NewReader("3\n")), out: &out,
		physicalIPs: func() ([]app.PublicInterfaceAddress, error) {
			detectorCalled = true
			return nil, nil
		},
		externalIP: func(context.Context) (string, error) {
			detectorCalled = true
			return "", nil
		},
	}
	got, err := c.selectPublicAddress(context.Background())
	if err != nil || got != "" || detectorCalled {
		t.Fatalf("address=%q detectorCalled=%v error=%v", got, detectorCalled, err)
	}
}

func TestSelectPublicAddressLetsUserChooseFromMultiplePhysicalAddresses(t *testing.T) {
	var out bytes.Buffer
	c := &commandSet{
		reader: bufio.NewReader(strings.NewReader("\n2\n")), out: &out,
		physicalIPs: func() ([]app.PublicInterfaceAddress, error) {
			return []app.PublicInterfaceAddress{
				{Interface: "eth0", Address: "8.8.8.8"},
				{Interface: "eth1", Address: "2606:4700:4700::1111"},
			}, nil
		},
	}
	got, err := c.selectPublicAddress(context.Background())
	if err != nil || got != "2606:4700:4700::1111" {
		t.Fatalf("address=%q error=%v", got, err)
	}
	for _, want := range []string{"检测到多个", "eth0  8.8.8.8（IPv4）", "eth1  2606:4700:4700::1111（IPv6）"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("address menu missing %q: %q", want, out.String())
		}
	}
}
