package cli

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"
	"time"

	"proxyforge/internal/app"
	"proxyforge/internal/domain"
	"proxyforge/internal/provider"
	"proxyforge/internal/provider/singbox"
	"proxyforge/internal/provider/xray"
	"proxyforge/internal/selfupdate"
	"proxyforge/internal/system"
)

type liveLogRunner struct {
	name string
	args []string
}

type installedUnitRunner struct{}

func (installedUnitRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	if name == "systemctl" && len(args) > 0 && args[0] == "show" {
		return []byte("loaded\n"), nil
	}
	return nil, nil
}

func markCoreInstalled(a *app.App) {
	a.LookPath = func(name string) (string, error) { return "/usr/bin/" + name, nil }
	a.Services = system.ServiceManager{Runner: installedUnitRunner{}}
}

func (r *liveLogRunner) Run(context.Context, string, ...string) ([]byte, error) {
	return nil, nil
}

func (r *liveLogRunner) RunStreaming(ctx context.Context, stdout, _ io.Writer, name string, args ...string) error {
	r.name = name
	r.args = append([]string(nil), args...)
	_, _ = io.WriteString(stdout, "live entry\n")
	<-ctx.Done()
	return ctx.Err()
}

func TestStableCommandTree(t *testing.T) {
	root := New("test")
	for _, args := range [][]string{{"install"}, {"update"}, {"uninstall"}, {"cleanup"}, {"config", "generate"}, {"config", "client"}, {"config", "reset"}, {"service"}} {
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

func TestUpdateCommandPassesYes(t *testing.T) {
	called := false
	c := &commandSet{
		yes: true,
		selfUpdate: func(_ context.Context, opts selfupdate.Options) error {
			called = true
			if !opts.AssumeYes || opts.Uninstall {
				t.Fatalf("options=%+v", opts)
			}
			return nil
		},
	}
	cmd := c.updateCommand()
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("self updater was not called")
	}
}

func TestUninstallCommandWithoutCoreCallsSelfUninstall(t *testing.T) {
	called := false
	c := &commandSet{selfUpdate: func(_ context.Context, opts selfupdate.Options) error {
		called = true
		if !opts.Uninstall {
			t.Fatalf("options=%+v", opts)
		}
		return nil
	}}
	if err := c.uninstallCommand().Execute(); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("self uninstaller was not called")
	}
}

func TestSelfUninstallRejectsCoreScriptFlags(t *testing.T) {
	c := &commandSet{selfUpdate: func(context.Context, selfupdate.Options) error {
		t.Fatal("self uninstaller should not be called")
		return nil
	}}
	cmd := c.uninstallCommand()
	cmd.SetArgs([]string{"--script-url", "https://example.com/install.sh"})
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "仅用于卸载代理内核") {
		t.Fatalf("error=%v", err)
	}
}

func TestUpdateCommandRejectsArguments(t *testing.T) {
	c := &commandSet{selfUpdate: func(context.Context, selfupdate.Options) error { return nil }}
	cmd := c.updateCommand()
	cmd.SetArgs([]string{"v1.2.3"})
	if err := cmd.Execute(); err == nil {
		t.Fatal("expected argument validation error")
	}
}

func TestVersionOutputIsDetailedAndSkipsRootCheck(t *testing.T) {
	rootChecked := false
	root := newCommand(
		"v1.2.3\ncommit: 0123456789abcdef0123456789abcdef01234567\nbuild date: 2026-08-08T12:00:00Z\ngo: go1.25.0\nplatform: linux/amd64",
		func() error {
			rootChecked = true
			return errors.New("root check should not run")
		},
	)
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetArgs([]string{"-v"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	if rootChecked {
		t.Fatal("root check ran for -v")
	}
	want := "proxyforge v1.2.3\ncommit: 0123456789abcdef0123456789abcdef01234567\nbuild date: 2026-08-08T12:00:00Z\ngo: go1.25.0\nplatform: linux/amd64\n"
	if got := out.String(); got != want {
		t.Fatalf("version output = %q, want %q", got, want)
	}
}

func TestClientCommandOffersClashFormat(t *testing.T) {
	c := &commandSet{}
	flag := c.clientCommand().Flags().Lookup("format")
	if flag == nil || flag.DefValue != app.ClientFormatNative {
		t.Fatalf("format flag=%v, want default %q", flag, app.ClientFormatNative)
	}
}

func TestServiceMenuLiveLogsReturnsAfterInterrupt(t *testing.T) {
	runner := &liveLogRunner{}
	a := &app.App{
		Registry:  provider.NewRegistry(singbox.New(), xray.New()),
		Services:  system.ServiceManager{Runner: runner},
		RootCheck: func() error { return nil },
	}
	var out, errOut bytes.Buffer
	c := &commandSet{
		app: a, reader: bufio.NewReader(strings.NewReader("6\n0\n")), out: &out, errOut: &errOut,
		interruptContext: func(parent context.Context) (context.Context, context.CancelFunc) {
			child, cancel := context.WithCancel(parent)
			cancel()
			return child, func() {}
		},
	}
	if err := c.serviceMenu(context.Background(), domain.CoreXray); err != nil {
		t.Fatal(err)
	}
	wantArgs := []string{"-u", "xray.service", "-n", "100", "-f", "--no-pager"}
	if runner.name != "journalctl" || !reflect.DeepEqual(runner.args, wantArgs) {
		t.Fatalf("command=%s args=%v, want journalctl %v", runner.name, runner.args, wantArgs)
	}
	for _, want := range []string{"实时日志", "live entry", "已停止实时日志", "ProxyForge  ›  xray  ›  服务管理"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("output missing %q: %q", want, out.String())
		}
	}
	if strings.Contains(errOut.String(), "操作失败") {
		t.Fatalf("interrupt was reported as an error: %q", errOut.String())
	}
}

func TestServiceMenuOffersLogLevelSettings(t *testing.T) {
	var out bytes.Buffer
	c := &commandSet{reader: bufio.NewReader(strings.NewReader("0\n")), out: &out}
	if err := c.serviceMenu(context.Background(), domain.CoreSingBox); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "7   日志级别") {
		t.Fatalf("service menu output=%q", out.String())
	}
	if got := logLevelDisplay(domain.CoreSingBox, "info"); !strings.Contains(got, "ProxyForge 默认") {
		t.Fatalf("sing-box info display=%q", got)
	}
	if got := logLevelDisplay(domain.CoreXray, "warning"); !strings.Contains(got, "ProxyForge 默认") {
		t.Fatalf("xray warning display=%q", got)
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
	flag := cmd.Flags().Lookup("simplified-config")
	if flag == nil || flag.DefValue != "false" {
		t.Fatalf("simplified-config flag=%v", flag)
	}
	for _, name := range []string{"standard-config", "sing-box-fallback-guard", "sing-box-fallback-port", "sing-box-fallback-http-domain", "xray-fallback-guard", "xray-fallback-port"} {
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
	for _, text := range []string{"服务端配置", "完整覆盖现有配置，不合并原配置", "查看配置", "DNS 设置", "REALITY 私钥", "server-secret"} {
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

func TestXrayServerConfigMenuOffersDedicatedServiceUser(t *testing.T) {
	var xrayOut, singBoxOut bytes.Buffer
	xrayMenu := &commandSet{reader: bufio.NewReader(strings.NewReader("0\n")), out: &xrayOut}
	if err := xrayMenu.serverConfigMenu(context.Background(), domain.CoreXray); err != nil {
		t.Fatal(err)
	}
	singBoxMenu := &commandSet{reader: bufio.NewReader(strings.NewReader("0\n")), out: &singBoxOut}
	if err := singBoxMenu.serverConfigMenu(context.Background(), domain.CoreSingBox); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(xrayOut.String(), "专用运行用户") || !strings.Contains(xrayOut.String(), "nobody 安全警告") {
		t.Fatalf("xray menu output=%q", xrayOut.String())
	}
	if !strings.Contains(xrayOut.String(), "REALITY SNI 候选检测") || !strings.Contains(singBoxOut.String(), "REALITY SNI 候选检测") {
		t.Fatalf("SNI retest option missing: xray=%q sing-box=%q", xrayOut.String(), singBoxOut.String())
	}
	if strings.Contains(singBoxOut.String(), "专用运行用户") {
		t.Fatalf("sing-box menu unexpectedly contains Xray option: %q", singBoxOut.String())
	}
}

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

func TestDNSProfileDisplayIncludesEncryptedOptions(t *testing.T) {
	for _, profile := range []string{provider.DNSProfileDoHCloudflare, provider.DNSProfileDoHGoogle} {
		got := dnsProfileDisplay(domain.CoreSingBox, profile)
		if !strings.Contains(got, "加密 DNS/DoH") {
			t.Fatalf("profile %q display=%q", profile, got)
		}
	}
}

func TestServerConfigGenerationQReturnsWithoutApplying(t *testing.T) {
	a := &app.App{
		Registry:  provider.NewRegistry(singbox.New(), xray.New()),
		Layout:    system.Layout{Root: t.TempDir()},
		RootCheck: func() error { return nil },
	}
	markCoreInstalled(a)
	var out, errOut bytes.Buffer
	c := &commandSet{
		app:    a,
		reader: bufio.NewReader(strings.NewReader("1\nq\n")),
		out:    &out,
		errOut: &errOut,
	}
	if err := c.serverConfigMenu(context.Background(), domain.CoreSingBox); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "已取消生成服务端配置") || !strings.Contains(out.String(), "输入 q") {
		t.Fatalf("cancel output=%q", out.String())
	}
	if errOut.Len() != 0 {
		t.Fatalf("cancel was reported as an error: %q", errOut.String())
	}
}

func TestServerConfigGenerationRejectsMissingCoreBeforePrompts(t *testing.T) {
	a := &app.App{
		Registry:  provider.NewRegistry(singbox.New(), xray.New()),
		RootCheck: func() error { return nil },
		LookPath:  func(string) (string, error) { return "", errors.New("missing") },
	}
	var out, errOut bytes.Buffer
	c := &commandSet{
		app: a, reader: bufio.NewReader(strings.NewReader("1\n0\n")), out: &out, errOut: &errOut,
	}
	if err := c.serverConfigMenu(context.Background(), domain.CoreSingBox); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(errOut.String(), "尚未安装 sing-box") || !strings.Contains(errOut.String(), "请先执行安装/升级") {
		t.Fatalf("missing-core error=%q", errOut.String())
	}
	if strings.Contains(out.String(), "生成服务端配置：sing-box") || strings.Contains(out.String(), "确认覆盖") {
		t.Fatalf("generation prompts were shown before install check: %q", out.String())
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

func TestUninstallConfirmationDescribesAutomaticCleanup(t *testing.T) {
	var out bytes.Buffer
	c := &commandSet{reader: bufio.NewReader(strings.NewReader("yes\n")), out: &out}
	ok, err := c.confirmUninstall("xray")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"自动清理", "历史备份", "永久删除", "卸载或核验失败时不会"} {
		if !ok || !strings.Contains(out.String(), want) {
			t.Fatalf("confirmed=%v output missing %q: %q", ok, want, out.String())
		}
	}
}

func TestCoreMenuMergesUninstallAndCleanup(t *testing.T) {
	var out bytes.Buffer
	c := &commandSet{out: &out}
	c.printCoreMenu(domain.CoreXray)
	if !strings.Contains(out.String(), "当前内核：xray") ||
		!strings.Contains(out.String(), "5   卸载内核          -- 同时清理配置和运行数据") ||
		strings.Contains(out.String(), "6   ") {
		t.Fatalf("menu did not merge uninstall and cleanup: %q", out.String())
	}
}

func TestCoreMenuAlignsAndDimsDescriptions(t *testing.T) {
	var plain bytes.Buffer
	(&commandSet{out: &plain}).printCoreMenu(domain.CoreSingBox)
	wantColumn := -1
	for _, line := range strings.Split(plain.String(), "\n") {
		separator := strings.Index(line, "-- ")
		if separator < 0 {
			continue
		}
		column := menuDisplayWidth(line[:separator])
		if wantColumn < 0 {
			wantColumn = column
		} else if column != wantColumn {
			t.Fatalf("description column=%d, want %d: %q", column, wantColumn, line)
		}
	}
	if wantColumn < 0 {
		t.Fatalf("menu has no descriptions: %q", plain.String())
	}

	var colored bytes.Buffer
	(&commandSet{out: system.NewColorWriter(&colored, true)}).printCoreMenu(domain.CoreSingBox)
	if !strings.Contains(colored.String(), "\x1b[90m-- 安装内核或升级版本\x1b[0m") {
		t.Fatalf("menu description is not gray: %q", colored.String())
	}
}

func TestMenuChoicesAlignAndSeparateDescriptions(t *testing.T) {
	var out bytes.Buffer
	c := &commandSet{out: &out}
	c.printMenuChoice("1", "生成/更新配置（完整覆盖现有配置，不合并原配置）")
	c.printMenuChoice("2", "查看配置")
	c.printMenuChoice("0/q", "返回")
	want := "  1   生成/更新配置     -- 完整覆盖现有配置，不合并原配置\n" +
		"  2   查看配置\n" +
		"  0/q 返回\n"
	if got := out.String(); got != want {
		t.Fatalf("menu output=%q, want %q", got, want)
	}
}

func TestCoreMenusUseGlobalPageHeader(t *testing.T) {
	for _, core := range []string{domain.CoreXray, domain.CoreSingBox} {
		var out bytes.Buffer
		c := &commandSet{out: &out, currentVersion: "v1.2.3"}
		c.printCoreMenu(core)
		for _, want := range []string{
			"╭─ ProxyForge  ›  " + core,
			proxyForgeHeaderRule,
		} {
			if !strings.Contains(out.String(), want) {
				t.Fatalf("core=%s menu output=%q, missing %q", core, out.String(), want)
			}
		}
	}
}

func TestInstallConfirmationDescribesSystemChanges(t *testing.T) {
	var out bytes.Buffer
	c := &commandSet{reader: bufio.NewReader(strings.NewReader("yes\n")), out: &out}
	ok, err := c.confirmInstall(domain.CoreSingBox)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"操作确认：安装/升级内核", "目标内核：sing-box", "将执行：", "配置保护：", "安全确认：",
		"安装或升级 sing-box", "内核二进制", "systemd unit", "现有配置会先备份",
		"确认操作？[Y/1=确认，Q/0=返回]",
	} {
		if !ok || !strings.Contains(out.String(), want) {
			t.Fatalf("confirmed=%v output missing %q: %q", ok, want, out.String())
		}
	}
}

func TestCoreMenuCanCancelInstallBeforeCallingApp(t *testing.T) {
	var out bytes.Buffer
	c := &commandSet{
		reader: bufio.NewReader(strings.NewReader("1\nq\n\n0\n")),
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
	t.Run("invalid input retries", func(t *testing.T) {
		var out bytes.Buffer
		c := &commandSet{reader: bufio.NewReader(strings.NewReader("n\ny\n")), out: &out}
		ok, err := c.confirm("confirm")
		if err != nil || !ok || !strings.Contains(out.String(), "输入无效，请输入 yes/y") {
			t.Fatalf("confirmed=%v error=%v output=%q", ok, err, out.String())
		}
	})
	t.Run("q returns menu", func(t *testing.T) {
		c := &commandSet{reader: bufio.NewReader(strings.NewReader("q\n")), out: io.Discard}
		ok, err := c.confirm("confirm")
		if !errors.Is(err, errReturnToMenu) || ok {
			t.Fatalf("confirmed=%v error=%v", ok, err)
		}
	})
}

func TestCancelableGenerateInputsRecognizeQ(t *testing.T) {
	t.Run("text", func(t *testing.T) {
		c := &commandSet{reader: bufio.NewReader(strings.NewReader("Q\n")), out: io.Discard}
		if _, err := c.askDefaultCancelable("value", "default"); !errors.Is(err, errReturnToMenu) {
			t.Fatalf("error=%v", err)
		}
	})
	t.Run("number", func(t *testing.T) {
		c := &commandSet{reader: bufio.NewReader(strings.NewReader("q\n")), out: io.Discard}
		if _, err := c.chooseNumberCancelable("choice", 1, 3, 1); !errors.Is(err, errReturnToMenu) {
			t.Fatalf("error=%v", err)
		}
	})
	t.Run("confirmation", func(t *testing.T) {
		c := &commandSet{reader: bufio.NewReader(strings.NewReader("q\n")), out: io.Discard}
		if _, err := c.confirmCancelable("confirm"); !errors.Is(err, errReturnToMenu) {
			t.Fatalf("error=%v", err)
		}
	})
}

func TestSNIConfirmationDefaultsToYes(t *testing.T) {
	var out bytes.Buffer
	c := &commandSet{reader: bufio.NewReader(strings.NewReader("\n")), out: &out}
	ok, err := c.confirmCancelableDefaultYes("确认 SNI 和 REALITY target？")
	if err != nil || !ok {
		t.Fatalf("confirmed=%v error=%v", ok, err)
	}
	if !strings.Contains(out.String(), "Y/1=确认") || !strings.Contains(out.String(), "Q/0=返回") || !strings.Contains(out.String(), "回车=确认") {
		t.Fatalf("default confirmation prompt=%q", out.String())
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
	if !confirmed || !strings.Contains(out.String(), "所有旧客户端配置中的 SNI/target 会立即失效") || !strings.Contains(out.String(), "手动配置会保留") {
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
			if len(candidates) < 10 || server != "server.example.com" || limit != 10 {
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

func TestSelectCoreUsesNumericChoice(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{name: "xray", input: "1\n", want: "xray"},
		{name: "xray default", input: "\n", want: "xray"},
		{name: "sing-box", input: "2\n", want: "sing-box"},
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

func TestSelectCoreAcceptsQToExit(t *testing.T) {
	var out bytes.Buffer
	c := &commandSet{
		reader:         bufio.NewReader(strings.NewReader("q\n")),
		out:            &out,
		currentVersion: "v1.2.6",
	}
	core, selected, err := c.selectCore()
	if err != nil {
		t.Fatal(err)
	}
	if selected || core != "" {
		t.Fatalf("core=%q selected=%v, want exit", core, selected)
	}
	if !strings.Contains(out.String(), "  1   Xray-core         -- 管理 Xray-core 内核与节点配置，默认") ||
		!strings.Contains(out.String(), "  2   sing-box          -- 管理 sing-box 内核与节点配置") ||
		!strings.Contains(out.String(), "  0/q 退出") ||
		!strings.Contains(out.String(), "❯ 请选择 [1]：") {
		t.Fatalf("menu style output=%q", out.String())
	}
}

func TestMenuCanReturnFromCoreSelection(t *testing.T) {
	var out, errOut bytes.Buffer
	c := &commandSet{
		reader:         bufio.NewReader(strings.NewReader("1\n0\n0\n")),
		out:            &out,
		errOut:         &errOut,
		currentVersion: "v1.2.3",
	}
	if err := c.menu(context.Background()); err != nil {
		t.Fatal(err)
	}
	if strings.Count(out.String(), "│ 双内核代理管理器  [版本 v1.2.3]") != 2 {
		t.Fatalf("core selector was not shown twice: %q", out.String())
	}
	if strings.Count(out.String(), proxyForgeHeaderRule) != 3 {
		t.Fatalf("global page header rule count is incorrect: %q", out.String())
	}
	if !strings.Contains(out.String(), "ProxyForge  ›  xray") || !strings.Contains(out.String(), "安装/升级") || !strings.Contains(out.String(), "服务端配置") {
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

func TestFillGenerateRequiresExplicitFastCandidateSelection(t *testing.T) {
	var out bytes.Buffer
	c := &commandSet{
		reader: bufio.NewReader(strings.NewReader("\n\n\n\n2\nyes\n")),
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
	}
	opts := domain.GenerateOptions{Server: "server.example.com", Port: 443, StandardConfig: true}
	if err := c.fillGenerate(context.Background(), domain.CoreSingBox, &opts); err != nil {
		t.Fatal(err)
	}
	if opts.SNI != "second.example.com" || opts.Target != "second.example.com:443" || opts.UserName != domain.DefaultUserName || opts.InboundTag != domain.DefaultInboundTag(domain.CoreSingBox) {
		t.Fatalf("generate options=%#v", opts)
	}
	if !strings.Contains(out.String(), "最快的候选域名（按延迟排序）") || !strings.Contains(out.String(), "2 second.example.com") ||
		!strings.Contains(out.String(), "必须输入编号") || !strings.Contains(out.String(), "TLS 1.3 / h2") ||
		!strings.Contains(out.String(), "Akamai（CNAME）") || !strings.Contains(out.String(), "均已通过 DNS、TLS 和证书名称校验") ||
		strings.Contains(out.String(), "[默认]") || strings.Contains(out.String(), "证书 SAN=fast.example.com") {
		t.Fatalf("candidate menu output=%q", out.String())
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
		reader: bufio.NewReader(strings.NewReader("\n\n\n\n\nyes\nyes\n")),
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
	if !opts.XrayFallbackGuard || opts.XrayFallbackPort != domain.DefaultXrayFallbackPort {
		t.Fatalf("generate options=%#v", opts)
	}
	for _, want := range []string{"回落防护", "dokodemo-door", "本机 dokodemo-door 回落端口"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("output missing %q: %q", want, out.String())
		}
	}
}

func TestFillGenerateSelectsSingBoxFallbackGuardConfig(t *testing.T) {
	var out bytes.Buffer
	c := &commandSet{
		reader: bufio.NewReader(strings.NewReader("\n\n\n\n\nyes\nyes\n")),
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
	if !opts.SingBoxFallbackGuard || opts.SingBoxFallbackPort != domain.DefaultSingBoxFallbackPort || opts.SingBoxFallbackHTTPDomain || opts.SimplifiedConfig {
		t.Fatalf("generate options=%#v", opts)
	}
	for _, want := range []string{"回落防护", "direct 入站", "本机 direct 回落端口", "不限制 Host       -- 默认"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("output missing %q: %q", want, out.String())
		}
	}
}

func TestFillGenerateEnablesSingBoxFallbackHTTPDomain(t *testing.T) {
	var out bytes.Buffer
	c := &commandSet{
		reader: bufio.NewReader(strings.NewReader("\n2\n\n\n\nyes\nyes\n")),
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
	if !opts.SingBoxFallbackGuard || !opts.SingBoxFallbackHTTPDomain {
		t.Fatalf("generate options=%#v", opts)
	}
	if !strings.Contains(out.String(), "Host 匹配 SNI") {
		t.Fatalf("HTTP domain option missing: %q", out.String())
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
