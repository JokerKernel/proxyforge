package cli

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"strings"
	"testing"

	"proxyforge/internal/domain"
	"proxyforge/internal/selfupdate"
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
	for _, want := range []string{"自动清理", "历史备份", "永久删除", "卸载或核验失败时不会"} {
		if !ok || !strings.Contains(out.String(), want) {
			t.Fatalf("confirmed=%v output missing %q: %q", ok, want, out.String())
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
