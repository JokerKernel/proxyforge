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

func TestStableCommandTree(t *testing.T) {
	root := New("test")
	for _, args := range [][]string{{"install"}, {"uninstall"}, {"cleanup"}, {"config", "generate"}, {"config", "client"}, {"config", "reset"}, {"service"}} {
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
	cmd, _, err := root.Find([]string{"upgrade"})
	if err != nil || cmd.Name() != "install" {
		t.Fatalf("upgrade alias resolved to %v, error=%v", cmd, err)
	}
}

func TestClientCommandOffersClashFormat(t *testing.T) {
	c := &commandSet{}
	flag := c.clientCommand().Flags().Lookup("format")
	if flag == nil || flag.DefValue != app.ClientFormatNative {
		t.Fatalf("format flag=%v, want default %q", flag, app.ClientFormatNative)
	}
}

func TestGenerateCommandOffersUserNameAndInboundTag(t *testing.T) {
	c := &commandSet{}
	cmd := c.generateCommand()
	for _, name := range []string{"user-name", "inbound-tag"} {
		flag := cmd.Flags().Lookup(name)
		if flag == nil || flag.DefValue != "" {
			t.Fatalf("%s flag=%v", name, flag)
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
		"连接地址：[2001:db8::10]:443",
		"REALITY SNI：www.example.com",
		"REALITY target：www.example.com:443",
		"用户名称：phone",
		"入站标签：phone-in",
		"内核版本：sing-box version 1.13.16",
		"查看客户端配置",
	} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("success output missing %q: %q", want, out.String())
		}
	}
}

func TestClientMenuOutputsClashYAML(t *testing.T) {
	layout := system.Layout{Root: t.TempDir()}
	store := system.StateStore{Layout: layout}
	if err := store.Save(domain.NodeSpec{
		ManagedBy: "proxyforge", Core: domain.CoreSingBox, Server: "server.example.com", Port: 443,
		SNI: "www.example.com", UUID: "123e4567-e89b-42d3-a456-426614174000",
		PublicKey: "public", ShortID: "0123456789abcdef",
	}); err != nil {
		t.Fatal(err)
	}
	a := &app.App{
		Registry: provider.NewRegistry(singbox.New(), xray.New()), Store: store,
		RootCheck: func() error { return nil },
	}
	var out bytes.Buffer
	c := &commandSet{app: a, reader: bufio.NewReader(strings.NewReader("2\n")), out: &out}
	pause, err := c.clientMenu(context.Background(), domain.CoreSingBox)
	if err != nil {
		t.Fatal(err)
	}
	if !pause || !strings.Contains(out.String(), "Clash YAML") || !strings.Contains(out.String(), "type: vless") {
		t.Fatalf("pause=%v output=%q", pause, out.String())
	}
}

func TestServerConfigMenuShowsCurrentConfig(t *testing.T) {
	layout := system.Layout{Root: t.TempDir()}
	want := []byte("{\"private_key\":\"server-secret\"}\n")
	if err := system.AtomicWrite(layout.Resolve(singbox.New().ConfigPath()), want, 0600); err != nil {
		t.Fatal(err)
	}
	a := &app.App{
		Registry: provider.NewRegistry(singbox.New(), xray.New()), Layout: layout,
		RootCheck: func() error { return nil },
	}
	var out, errOut bytes.Buffer
	c := &commandSet{
		app: a, reader: bufio.NewReader(strings.NewReader("2\n0\n")), out: &out, errOut: &errOut,
	}
	if err := c.serverConfigMenu(context.Background(), domain.CoreSingBox); err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{"服务端配置管理", "查看当前配置", "REALITY 私钥", "server-secret"} {
		if !strings.Contains(out.String(), text) {
			t.Fatalf("server config menu missing %q: %q", text, out.String())
		}
	}
	if count := strings.Count(out.String(), "警告：当前服务端配置可能包含"); count != 1 {
		t.Fatalf("sensitive warning count=%d output=%q", count, out.String())
	}
	if errOut.Len() != 0 {
		t.Fatalf("server config menu error output=%q", errOut.String())
	}
}

func TestCleanupCommandRequiresYesWhenNonInteractive(t *testing.T) {
	input := strings.NewReader("")
	c := &commandSet{in: input, reader: bufio.NewReader(input), out: io.Discard}
	cmd := c.cleanupCommand()
	cmd.SetArgs([]string{"all"})
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "--yes") {
		t.Fatalf("error = %v, want --yes requirement", err)
	}
}

func TestUninstallCommandRequiresYesWhenNonInteractive(t *testing.T) {
	input := strings.NewReader("")
	c := &commandSet{in: input, reader: bufio.NewReader(input), out: io.Discard}
	cmd := c.uninstallCommand()
	cmd.SetArgs([]string{"sing-box"})
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "--yes") {
		t.Fatalf("error = %v, want --yes requirement", err)
	}
}

func TestUninstallConfirmationDescribesRetainedRecoveryData(t *testing.T) {
	var out bytes.Buffer
	c := &commandSet{reader: bufio.NewReader(strings.NewReader("yes\n")), out: &out}
	ok, err := c.confirmUninstall("xray")
	if err != nil {
		t.Fatal(err)
	}
	if !ok || !strings.Contains(out.String(), "历史备份") || !strings.Contains(out.String(), "客户端将立即失效") {
		t.Fatalf("confirmed=%v output=%q", ok, out.String())
	}
}

func TestInstallConfirmationDescribesSystemChanges(t *testing.T) {
	var out bytes.Buffer
	c := &commandSet{reader: bufio.NewReader(strings.NewReader("yes\n")), out: &out}
	ok, err := c.confirmInstall(domain.CoreSingBox)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"安装或升级 sing-box", "内核二进制", "systemd unit", "现有配置会先备份"} {
		if !ok || !strings.Contains(out.String(), want) {
			t.Fatalf("confirmed=%v output missing %q: %q", ok, want, out.String())
		}
	}
}

func TestCoreMenuCanCancelInstallBeforeCallingApp(t *testing.T) {
	var out bytes.Buffer
	c := &commandSet{
		reader: bufio.NewReader(strings.NewReader("1\nn\n\n")),
		out:    &out,
	}
	if err := c.coreMenu(context.Background(), domain.CoreSingBox); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "已取消安装/升级") {
		t.Fatalf("cancel output=%q", out.String())
	}
}

func TestConfirmAcceptsGlobalAffirmativeForms(t *testing.T) {
	for _, input := range []string{"yes\n", "y\n", "Y\n", "YES\n"} {
		t.Run(strings.TrimSpace(input), func(t *testing.T) {
			var out bytes.Buffer
			c := &commandSet{reader: bufio.NewReader(strings.NewReader(input)), out: &out}
			ok, err := c.confirm("confirm")
			if err != nil || !ok {
				t.Fatalf("input=%q confirmed=%v error=%v", input, ok, err)
			}
		})
	}
	for _, input := range []string{"n\n", "no\n", "\n"} {
		t.Run("reject_"+strings.TrimSpace(input), func(t *testing.T) {
			c := &commandSet{reader: bufio.NewReader(strings.NewReader(input)), out: io.Discard}
			ok, err := c.confirm("confirm")
			if err != nil || ok {
				t.Fatalf("input=%q confirmed=%v error=%v", input, ok, err)
			}
		})
	}
}

func TestCleanupConfirmationWarnsNoBackup(t *testing.T) {
	var out bytes.Buffer
	c := &commandSet{reader: bufio.NewReader(strings.NewReader("Y\n")), out: &out}
	ok, err := c.confirmCleanup("all")
	if err != nil {
		t.Fatal(err)
	}
	if !ok || !strings.Contains(out.String(), "不会创建新备份") || !strings.Contains(out.String(), "永久删除") {
		t.Fatalf("confirmed=%v output=%q", ok, out.String())
	}
}

func TestCommandsRequireRootBeforeRunning(t *testing.T) {
	want := errors.New("root required")
	checks := 0
	root := newCommand("test", func() error {
		checks++
		return want
	})
	root.SetArgs([]string{"config", "client", "sing-box"})
	err := root.Execute()
	if !errors.Is(err, want) {
		t.Fatalf("error = %v, want %v", err, want)
	}
	if checks != 1 {
		t.Fatalf("root checks = %d, want 1", checks)
	}
}

func TestMenuRequiresRootBeforeRunning(t *testing.T) {
	want := errors.New("root required")
	root := newCommand("test", func() error { return want })
	root.SetArgs(nil)
	if err := root.Execute(); !errors.Is(err, want) {
		t.Fatalf("error = %v, want %v", err, want)
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
	if strings.Contains(out.String(), "\x1b[") {
		t.Fatalf("redirected choice output contains ANSI controls: %q", out.String())
	}
}

func TestEraseChoiceRetryReplacesPromptAndPreviousError(t *testing.T) {
	const eraseLine = "\x1b[1A\r\x1b[2K"
	var first, retry bytes.Buffer
	eraseChoiceRetry(&first, false)
	eraseChoiceRetry(&retry, true)
	if first.String() != eraseLine {
		t.Fatalf("first invalid clear=%q", first.String())
	}
	if retry.String() != eraseLine+eraseLine {
		t.Fatalf("repeated invalid clear=%q", retry.String())
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
		reader: bufio.NewReader(strings.NewReader("1\n0\n0\n")),
		out:    &out,
		errOut: &errOut,
	}
	if err := c.menu(context.Background()); err != nil {
		t.Fatal(err)
	}
	if strings.Count(out.String(), "ProxyForge 双内核代理管理器") != 2 {
		t.Fatalf("core selector was not shown twice: %q", out.String())
	}
	if !strings.Contains(out.String(), "sing-box 管理菜单") || !strings.Contains(out.String(), "安装/升级内核") || !strings.Contains(out.String(), "服务端配置管理") {
		t.Fatalf("core menu missing: %q", out.String())
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

func TestClearTerminalClearsVisibleScreenAndScrollback(t *testing.T) {
	var out bytes.Buffer
	clearTerminal(&out)
	if out.String() != "\x1b[H\x1b[2J\x1b[3J" {
		t.Fatalf("clear sequence=%q", out.String())
	}
}

func TestFillGenerateSelectsRandomDefaultFromFastCandidates(t *testing.T) {
	var out bytes.Buffer
	c := &commandSet{
		reader: bufio.NewReader(strings.NewReader("\n\n\n\nyes\n")),
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
	if opts.SNI != "second.example.com" || opts.Target != "second.example.com:443" || opts.UserName != domain.DefaultUserName || opts.InboundTag != domain.DefaultInboundTag(domain.CoreSingBox) {
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
