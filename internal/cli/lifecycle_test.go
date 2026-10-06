package cli

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"testing"

	"proxyforge/internal/app"
	"proxyforge/internal/domain"
	"proxyforge/internal/install"
	"proxyforge/internal/selfupdate"
	"proxyforge/internal/system"
)

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
	for _, want := range []string{"自动清理", "历史备份", "永久删除", "卸载或核验失败时不会", "xray 专用系统用户和组"} {
		if !ok || !strings.Contains(out.String(), want) {
			t.Fatalf("confirmed=%v output missing %q: %q", ok, want, out.String())
		}
	}
}

func TestInstallConfirmationDescribesSystemChanges(t *testing.T) {
	var out bytes.Buffer
	c := &commandSet{reader: bufio.NewReader(strings.NewReader("yes\n")), out: &out}
	ok, err := c.confirmInstall(domain.CoreSingBox, install.Options{})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"操作确认：安装/升级内核", "目标内核：sing-box", "安装版本：最新稳定版", "将执行：", "配置保护：", "安全确认：",
		"安装或升级 sing-box", "内核二进制", "systemd unit", "现有配置会先备份",
		"确认操作？[Y/1=确认，Q/0=返回]",
	} {
		if !ok || !strings.Contains(out.String(), want) {
			t.Fatalf("confirmed=%v output missing %q: %q", ok, want, out.String())
		}
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

func TestInstallCommandRejectsBetaWithVersion(t *testing.T) {
	c := &commandSet{out: io.Discard}
	cmd := c.installCommand()
	cmd.SetArgs([]string{"xray", "--beta", "--version", "v26.9.9"})
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "--beta 与 --version 不能同时使用") {
		t.Fatalf("error=%v", err)
	}
}

func TestInstallPageSelectsTargetForBothCores(t *testing.T) {
	for _, core := range []string{domain.CoreSingBox, domain.CoreXray} {
		for _, tt := range []struct {
			name, input, version, target string
			beta                         bool
		}{
			{name: "default stable", input: "1\nyes\n", target: "稳定版（最新正式版）"},
			{name: "development", input: "2\n2\n1\nyes\n", beta: true, target: "开发版（最新预发布）"},
			{name: "specified after development", input: "2\n2\n3\nv26.9.9\n1\nyes\n", version: "v26.9.9", target: "指定版本 v26.9.9"},
			{name: "stable clears specified", input: "3\nv26.9.9\n2\n1\n1\nyes\n", target: "稳定版（最新正式版）"},
			{name: "development clears specified", input: "3\nv26.9.9\n2\n2\n1\nyes\n", beta: true, target: "开发版（最新预发布）"},
			{name: "cancel confirmation retains target", input: "2\n2\n1\nq\n1\nyes\n", beta: true, target: "开发版（最新预发布）"},
			{name: "cancel selection retains target", input: "2\n2\n2\nq\n3\nq\n1\nyes\n", beta: true, target: "开发版（最新预发布）"},
			{name: "invalid specified version retries", input: "3\n--beta\nv26.9.9\n1\nyes\n", version: "v26.9.9", target: "指定版本 v26.9.9"},
		} {
			t.Run(core+"/"+tt.name, func(t *testing.T) {
				var out bytes.Buffer
				c := &commandSet{reader: bufio.NewReader(strings.NewReader(tt.input)), out: &out}
				base := install.Options{URL: "https://example.com/install.sh", TrustScriptSHA256: "fixed-hash"}
				opts, err := c.chooseInstallOptions(context.Background(), core, base)
				if err != nil {
					t.Fatal(err)
				}
				if opts.Beta != tt.beta || opts.Version != tt.version || opts.URL != base.URL || opts.TrustScriptSHA256 != base.TrustScriptSHA256 {
					t.Fatalf("opts=%+v", opts)
				}
				// Inspect the last page before confirmation, so stale targets
				// displayed on an earlier page cannot satisfy the assertion.
				page := out.String()[strings.LastIndex(out.String(), "╭─ 当前内核"):]
				for _, want := range []string{"版本号", "尚未安装", "所选版本", tt.target, "1   安装/更新", "2   选择版本\n", "3   指定版本"} {
					if !strings.Contains(page, want) {
						t.Fatalf("page missing %q: %q", want, page)
					}
				}
			})
		}
	}
}

func TestInstallPageReturnsWithoutInstallingOnCancelOrEOF(t *testing.T) {
	for _, input := range []string{"0\n", "q\n", "2\n2\nq\n", "3\n"} {
		c := &commandSet{reader: bufio.NewReader(strings.NewReader(input)), out: io.Discard}
		_, err := c.chooseInstallOptions(context.Background(), domain.CoreSingBox, install.Options{})
		if err == nil {
			t.Fatalf("input=%q returned an installation target without confirmation", input)
		}
	}
}

func TestInstallPageRestoresSavedChannelWithoutSavingUninstalledChoices(t *testing.T) {
	for _, core := range []string{domain.CoreSingBox, domain.CoreXray} {
		for _, tt := range []struct {
			name, saved, input string
			beta, canceled     bool
		}{
			{name: "remember development", saved: system.InstallChannelBeta, input: "1\nyes\n", beta: true},
			{name: "remember stable", saved: system.InstallChannelStable, input: "1\nyes\n"},
			{name: "switch to stable without installing", saved: system.InstallChannelBeta, input: "2\n1\n1\nyes\n"},
			{name: "switch to development then cancel", saved: system.InstallChannelStable, input: "2\n2\n0\n", canceled: true},
		} {
			t.Run(core+"/"+tt.name, func(t *testing.T) {
				layout := system.Layout{Root: t.TempDir()}
				store := system.InstallPreferencesStore{Layout: layout}
				if err := store.SaveChannel(core, tt.saved); err != nil {
					t.Fatal(err)
				}
				var out bytes.Buffer
				c := &commandSet{app: &app.App{Layout: layout}, reader: bufio.NewReader(strings.NewReader(tt.input)), out: &out}
				opts, err := c.chooseInstallOptions(context.Background(), core, install.Options{})
				if tt.canceled {
					if err != errReturnToMenu {
						t.Fatalf("err=%v", err)
					}
				} else if err != nil || opts.Beta != tt.beta {
					t.Fatalf("opts=%+v err=%v", opts, err)
				}
				if got, err := store.LoadChannel(core); err != nil || got != tt.saved {
					t.Fatalf("selection was saved before installation: %q %v", got, err)
				}
				other := domain.CoreXray
				if core == other {
					other = domain.CoreSingBox
				}
				if _, err := os.Stat(layout.InstallPreferencesPath(other)); !os.IsNotExist(err) {
					t.Fatalf("other core preference unexpectedly created: %v", err)
				}
			})
		}
	}
}

func TestInteractiveInstallCommandUsesPreferencesOnlyWithoutExplicitFlags(t *testing.T) {
	input, err := os.Open("/dev/null")
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	for _, core := range []string{domain.CoreSingBox, domain.CoreXray} {
		for _, flag := range []string{"", "--beta=false", "--beta", "--version=1.2.3"} {
			t.Run(core+"/"+flag, func(t *testing.T) {
				layout := system.Layout{Root: t.TempDir()}
				store := system.InstallPreferencesStore{Layout: layout}
				if err := store.SaveChannel(core, system.InstallChannelBeta); err != nil {
					t.Fatal(err)
				}
				stop := errors.New("stop before installation")
				var out bytes.Buffer
				c := &commandSet{
					app: &app.App{Layout: layout, RootCheck: func() error { return stop }},
					in:  input, reader: bufio.NewReader(strings.NewReader("1\nyes\n")), out: &out,
				}
				cmd := c.installCommand()
				args := []string{core}
				if flag != "" {
					args = append(args, flag)
				}
				cmd.SetArgs(args)
				if err := cmd.Execute(); !errors.Is(err, stop) {
					t.Fatalf("err=%v", err)
				}
				if flag == "" {
					if !strings.Contains(out.String(), "所选版本  开发版（最新预发布）") {
						t.Fatalf("saved selection not restored: %q", out.String())
					}
				} else if out.Len() != 0 {
					t.Fatalf("explicit flag unexpectedly opened the preference menu: %q", out.String())
				}
			})
		}
	}
}

func TestInstallCardShowsVersionAndTargetForBothCores(t *testing.T) {
	for _, core := range []string{domain.CoreSingBox, domain.CoreXray} {
		for _, tt := range []struct {
			opts  install.Options
			label string
		}{
			{install.Options{}, "稳定版（最新正式版）"},
			{install.Options{Beta: true}, "开发版（最新预发布）"},
			{install.Options{Version: "v26.9.9"}, "指定版本 v26.9.9"},
		} {
			var out bytes.Buffer
			c := &commandSet{out: &out}
			c.printInstallStatusCard(core, app.CoreInstallStatus{Installed: true, Version: "current-version"}, tt.opts)
			for _, want := range []string{"版本号    current-version", "所选版本  " + tt.label} {
				if !strings.Contains(out.String(), want) {
					t.Fatalf("core=%s card missing %q: %q", core, want, out.String())
				}
			}
			if strings.Contains(out.String(), "当前版本") || strings.Contains(out.String(), "当前渠道") {
				t.Fatalf("card still displays the installed channel: %q", out.String())
			}
		}
	}
}

func TestParseSpecifiedCoreVersion(t *testing.T) {
	tests := []struct {
		in      string
		want    string
		wantErr string
	}{
		{in: "v26.9.9", want: "v26.9.9"},
		{in: " 1.12.0 ", want: "1.12.0"},
		{in: "", wantErr: "不能为空"},
		{in: "--beta", wantErr: "无效"},
		{in: "v26.9.9 latest", wantErr: "无效"},
	}
	for _, tt := range tests {
		got, err := parseSpecifiedCoreVersion(tt.in)
		if tt.wantErr != "" {
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("parseSpecifiedCoreVersion(%q) error=%v, want %q", tt.in, err, tt.wantErr)
			}
			continue
		}
		if err != nil || got != tt.want {
			t.Errorf("parseSpecifiedCoreVersion(%q)=%q, %v, want %q", tt.in, got, err, tt.want)
		}
	}
}

func TestConfirmInstallShowsSelectedVersion(t *testing.T) {
	var out bytes.Buffer
	c := &commandSet{reader: bufio.NewReader(strings.NewReader("yes\n")), out: &out}
	ok, err := c.confirmInstall(domain.CoreXray, install.Options{Beta: true})
	if err != nil {
		t.Fatal(err)
	}
	if !ok || !strings.Contains(out.String(), "安装版本：最新预发布") {
		t.Fatalf("confirmed=%v output=%q", ok, out.String())
	}
}
