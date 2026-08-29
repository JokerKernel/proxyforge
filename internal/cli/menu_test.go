package cli

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"os/exec"
	"strings"
	"testing"

	"proxyforge/internal/app"
	"proxyforge/internal/domain"
	"proxyforge/internal/provider"
	"proxyforge/internal/provider/singbox"
	"proxyforge/internal/provider/xray"
	"proxyforge/internal/system"
)

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
		app: a, reader: bufio.NewReader(strings.NewReader("3\n0\n")), out: &out, errOut: &errOut,
	}
	if err := c.serverConfigMenu(context.Background(), domain.CoreSingBox); err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{"服务端配置", "完整覆盖现有配置，不合并原配置", "查看配置", "编辑配置", "修改配置", "REALITY 私钥", "server-secret"} {
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

func TestServerConfigMenuShowsStatusCard(t *testing.T) {
	layout := system.Layout{Root: t.TempDir()}
	store := system.StateStore{Layout: layout}
	p := xray.New()
	node := domain.NodeSpec{
		ManagedBy: "proxyforge", Core: domain.CoreXray, InboundTag: "xray-one", UserName: "one",
		Server: "203.0.113.10", Port: 443, SNI: "www.example.com", Target: "www.example.com:443",
		UUID: "123e4567-e89b-42d3-a456-426614174000", PrivateKey: "private", PublicKey: "public", ShortID: "abcd",
		XrayFallbackGuard: true, XrayFallbackPort: 61431,
	}
	config, err := p.RenderServer(node)
	if err != nil {
		t.Fatal(err)
	}
	if err := system.AtomicWrite(layout.Resolve(p.ConfigPath()), config, 0600); err != nil {
		t.Fatal(err)
	}
	if err := store.Save(node); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	c := &commandSet{
		app: &app.App{
			Registry: provider.NewRegistry(p), Layout: layout, Store: store,
			Services: system.ServiceManager{Runner: installedUnitRunner{}},
		},
		reader: bufio.NewReader(strings.NewReader("0\n")),
		out:    &out,
	}
	if err := c.serverConfigMenu(context.Background(), domain.CoreXray); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	if userAt, dnsAt := strings.Index(got, "运行用户"), strings.Index(got, "DNS 设置"); userAt < 0 || dnsAt < 0 || userAt > dnsAt {
		t.Fatalf("running user should be the first config row: %q", got)
	}
	for _, text := range []string{
		"服务端配置", "╭─ 当前配置",
		"DNS 设置", "系统 DNS",
		"出站 IP", "默认（先 IPv4，300ms 后竞速 IPv6）",
		"回落 IP", "运行用户", "xray", "SNI", "www.example.com",
		"日志级别", "warning", "SNI 防护", "已开启", "严格模式", "未开启",
	} {
		if !strings.Contains(got, text) {
			t.Fatalf("server config card missing %q: %q", text, got)
		}
	}
}

func TestXrayServerConfigMenuOffersDedicatedServiceUser(t *testing.T) {
	var xrayOut, singBoxOut bytes.Buffer
	xrayMenu := &commandSet{reader: bufio.NewReader(strings.NewReader("2\n0\n0\n")), out: &xrayOut}
	if err := xrayMenu.serverConfigMenu(context.Background(), domain.CoreXray); err != nil {
		t.Fatal(err)
	}
	singBoxMenu := &commandSet{reader: bufio.NewReader(strings.NewReader("2\n0\n0\n")), out: &singBoxOut}
	if err := singBoxMenu.serverConfigMenu(context.Background(), domain.CoreSingBox); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(xrayOut.String(), "专用运行用户") || !strings.Contains(xrayOut.String(), "nobody 安全警告") {
		t.Fatalf("xray menu output=%q", xrayOut.String())
	}
	for _, text := range []string{"╭─ 当前配置", "运行用户", "DNS 设置", "出站 IP", "SNI"} {
		if !strings.Contains(xrayOut.String(), text) || !strings.Contains(singBoxOut.String(), text) {
			t.Fatalf("server config card missing %q: xray=%q sing-box=%q", text, xrayOut.String(), singBoxOut.String())
		}
	}
	if !strings.Contains(xrayOut.String(), "REALITY SNI 候选检测") || !strings.Contains(singBoxOut.String(), "REALITY SNI 候选检测") {
		t.Fatalf("SNI retest option missing: xray=%q sing-box=%q", xrayOut.String(), singBoxOut.String())
	}
	if strings.Contains(singBoxOut.String(), "专用运行用户") {
		t.Fatalf("sing-box menu unexpectedly contains Xray option: %q", singBoxOut.String())
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
		reader: bufio.NewReader(strings.NewReader("1\nq\n0\n")),
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

func TestCoreMenuMergesUninstallAndCleanup(t *testing.T) {
	var out bytes.Buffer
	c := &commandSet{out: &out}
	c.printCoreMenu(context.Background(), domain.CoreXray)
	if !strings.Contains(out.String(), "╭─ 当前内核") ||
		!strings.Contains(out.String(), "Xray-core") ||
		!strings.Contains(out.String(), "[未安装]") ||
		!strings.Contains(out.String(), "4   卸载内核          -- 同时清理配置和运行数据") ||
		strings.Contains(out.String(), "5   ") {
		t.Fatalf("menu did not merge uninstall and cleanup: %q", out.String())
	}
}

func TestServiceManagementIsNestedUnderServerConfig(t *testing.T) {
	var coreOut bytes.Buffer
	(&commandSet{out: &coreOut}).printCoreMenu(context.Background(), domain.CoreXray)
	if strings.Contains(coreOut.String(), "4   服务管理") ||
		!strings.Contains(coreOut.String(), "服务端配置") ||
		!strings.Contains(coreOut.String(), "生成、修改、查看与服务管理") {
		t.Fatalf("core menu output=%q", coreOut.String())
	}

	var serverOut bytes.Buffer
	c := &commandSet{
		reader: bufio.NewReader(strings.NewReader("5\n0\n0\n")),
		out:    &serverOut,
	}
	if err := c.serverConfigMenu(context.Background(), domain.CoreXray); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"5   服务管理", "6   专用运行用户",
		"ProxyForge  ›  xray  ›  服务端配置  ›  服务管理",
	} {
		if !strings.Contains(serverOut.String(), want) {
			t.Fatalf("nested service menu missing %q: %q", want, serverOut.String())
		}
	}
}

func TestCoreMenuShowsInstalledCoreVersion(t *testing.T) {
	a := &app.App{
		Registry: provider.NewRegistry(singbox.New(), xray.New()),
		Runner:   installedVersionRunner{},
	}
	var out bytes.Buffer
	c := &commandSet{app: a, out: &out}
	c.printCoreMenu(context.Background(), domain.CoreXray)
	if !strings.Contains(out.String(), "╭─ 当前内核") ||
		!strings.Contains(out.String(), "Xray-core") ||
		!strings.Contains(out.String(), "[已安装]") ||
		!strings.Contains(out.String(), "Xray 25.1.1") {
		t.Fatalf("core menu missing version card: %q", out.String())
	}
}

func TestCoreMenuAlignsAndDimsDescriptions(t *testing.T) {
	var plain bytes.Buffer
	(&commandSet{out: &plain}).printCoreMenu(context.Background(), domain.CoreSingBox)
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
	(&commandSet{out: system.NewColorWriter(&colored, true)}).printCoreMenu(context.Background(), domain.CoreSingBox)
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
		c.printCoreMenu(context.Background(), core)
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
			got, selected, err := c.selectCore(context.Background())
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
	core, selected, err := c.selectCore(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if selected || core != "" {
		t.Fatalf("core=%q selected=%v, want exit", core, selected)
	}
	if !strings.Contains(out.String(), "  1   Xray-core           [未安装]") ||
		!strings.Contains(out.String(), "  2   sing-box            [未安装]") ||
		!strings.Contains(out.String(), "  0/q 退出") ||
		!strings.Contains(out.String(), "❯ 请选择 [1]：") {
		t.Fatalf("menu style output=%q", out.String())
	}
}

func TestSelectCoreShowsInstallStatus(t *testing.T) {
	a := &app.App{
		Registry:  provider.NewRegistry(singbox.New(), xray.New()),
		RootCheck: func() error { return nil },
		LookPath: func(name string) (string, error) {
			if name == "xray" {
				return "/usr/bin/xray", nil
			}
			return "", exec.ErrNotFound
		},
		Services: system.ServiceManager{Runner: installedUnitRunner{}},
	}
	var out bytes.Buffer
	c := &commandSet{
		app:    a,
		reader: bufio.NewReader(strings.NewReader("q\n")),
		out:    &out,
	}
	if _, _, err := c.selectCore(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "  1   Xray-core           [已安装]") {
		t.Fatalf("installed xray status missing: %q", out.String())
	}
	if !strings.Contains(out.String(), "  2   sing-box            [未安装]") {
		t.Fatalf("missing sing-box status: %q", out.String())
	}
}

func TestSelectCoreColorsInstallStatus(t *testing.T) {
	a := &app.App{
		Registry:  provider.NewRegistry(singbox.New(), xray.New()),
		RootCheck: func() error { return nil },
		LookPath: func(name string) (string, error) {
			if name == "xray" {
				return "/usr/bin/xray", nil
			}
			return "", exec.ErrNotFound
		},
		Services: system.ServiceManager{Runner: installedUnitRunner{}},
	}
	var out bytes.Buffer
	c := &commandSet{
		app:    a,
		reader: bufio.NewReader(strings.NewReader("q\n")),
		out:    system.NewColorWriter(&out, true),
	}
	if _, _, err := c.selectCore(context.Background()); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	if !strings.Contains(got, "\x1b[38;5;208m[已安装]\x1b[0m") {
		t.Fatalf("installed status should use theme orange: %q", got)
	}
	if !strings.Contains(got, "\x1b[90m[未安装]\x1b[0m") {
		t.Fatalf("missing status should be gray: %q", got)
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
	if strings.Count(out.String(), proxyForgeHeaderRule) != 4 {
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
