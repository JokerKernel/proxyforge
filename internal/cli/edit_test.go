package cli

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"proxyforge/internal/app"
	"proxyforge/internal/domain"
	"proxyforge/internal/provider"
	"proxyforge/internal/provider/singbox"
	"proxyforge/internal/provider/xray"
	"proxyforge/internal/system"
)

func TestFindConfigEditorPrefersVimThenNanoThenVi(t *testing.T) {
	seen := []string{}
	c := &commandSet{
		lookPath: func(name string) (string, error) {
			seen = append(seen, name)
			if name == "nano" {
				return "/usr/bin/nano", nil
			}
			return "", exec.ErrNotFound
		},
	}
	got, err := c.findConfigEditor()
	if err != nil {
		t.Fatal(err)
	}
	if got != "/usr/bin/nano" {
		t.Fatalf("editor=%q", got)
	}
	if strings.Join(seen, ",") != "vim,nano" {
		t.Fatalf("lookup order=%v", seen)
	}
}

func TestEditServerConfigOpensResolvedPathWithPreferredEditor(t *testing.T) {
	layout := system.Layout{Root: t.TempDir()}
	path := layout.Resolve(singbox.New().ConfigPath())
	if err := system.AtomicWrite(path, []byte(`{"inbounds":[]}`), 0600); err != nil {
		t.Fatal(err)
	}
	a := &app.App{
		Registry:  provider.NewRegistry(singbox.New(), xray.New()),
		Layout:    layout,
		RootCheck: func() error { return nil },
	}
	var openedEditor, openedPath string
	var out bytes.Buffer
	c := &commandSet{
		app:    a,
		reader: bufio.NewReader(strings.NewReader("4\n0\n")),
		out:    &out,
		lookPath: func(name string) (string, error) {
			if name == "vim" {
				return "/usr/bin/vim", nil
			}
			return "", exec.ErrNotFound
		},
		runEditor: func(editor, configPath string) error {
			openedEditor, openedPath = editor, configPath
			return nil
		},
	}
	if err := c.serverConfigMenu(context.Background(), domain.CoreSingBox); err != nil {
		t.Fatal(err)
	}
	if openedEditor != "/usr/bin/vim" || openedPath != path {
		t.Fatalf("opened editor=%q path=%q, want vim %q", openedEditor, openedPath, path)
	}
	if !strings.Contains(out.String(), "使用 vim 打开") || !strings.Contains(out.String(), "已关闭编辑器") {
		t.Fatalf("edit output=%q", out.String())
	}
}

func TestEditServerConfigRequiresExistingFileAndEditor(t *testing.T) {
	a := &app.App{
		Registry:  provider.NewRegistry(singbox.New(), xray.New()),
		Layout:    system.Layout{Root: t.TempDir()},
		RootCheck: func() error { return nil },
	}
	c := &commandSet{
		app:       a,
		out:       &bytes.Buffer{},
		runEditor: func(string, string) error { t.Fatal("editor should not run"); return nil },
	}
	if err := c.editServerConfig(domain.CoreSingBox); err == nil || !strings.Contains(err.Error(), "尚未找到") {
		t.Fatalf("missing config error=%v", err)
	}

	path := a.Layout.Resolve(singbox.New().ConfigPath())
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	c.lookPath = func(string) (string, error) { return "", exec.ErrNotFound }
	if err := c.editServerConfig(domain.CoreSingBox); err == nil || !strings.Contains(err.Error(), "未找到可用编辑器") {
		t.Fatalf("missing editor error=%v", err)
	}
}

func TestEditServerConfigRequiresInteractiveTerminal(t *testing.T) {
	err := (&commandSet{out: &bytes.Buffer{}}).editServerConfig(domain.CoreSingBox)
	if err == nil || !strings.Contains(err.Error(), "交互式终端") {
		t.Fatalf("error=%v", err)
	}
}

func TestXrayServerConfigMenuNumbersEditBeforeModify(t *testing.T) {
	var out bytes.Buffer
	c := &commandSet{reader: bufio.NewReader(strings.NewReader("0\n")), out: &out}
	if err := c.serverConfigMenu(context.Background(), domain.CoreXray); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "3   修改配置") || !strings.Contains(out.String(), "4   编辑配置") ||
		!strings.Contains(out.String(), "5   专用运行用户") {
		t.Fatalf("xray menu=%q", out.String())
	}
}

func TestFindConfigEditorReportsWhenNoneAvailable(t *testing.T) {
	c := &commandSet{lookPath: func(string) (string, error) { return "", errors.New("missing") }}
	if _, err := c.findConfigEditor(); err == nil || !strings.Contains(err.Error(), "vim、nano、vi") {
		t.Fatalf("error=%v", err)
	}
}
