package cli

import (
	"bufio"
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"

	"proxyforge/internal/app"
	dockerfeature "proxyforge/internal/docker"
	"proxyforge/internal/domain"
	"proxyforge/internal/provider"
	"proxyforge/internal/provider/singbox"
	"proxyforge/internal/provider/xray"
	"proxyforge/internal/system"
)

type cleanupMenuRunner struct {
	t        *testing.T
	reloaded bool
}

func (r *cleanupMenuRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	if name == "systemctl" && len(args) > 0 {
		switch args[0] {
		case "show":
			return []byte("not-found\n"), nil
		case "is-active":
			return []byte("inactive\n"), nil
		case "is-enabled":
			return []byte("disabled\n"), nil
		case "daemon-reload":
			r.reloaded = true
			return nil, nil
		}
	}
	r.t.Fatalf("cleanup must not run uninstall or service changes: %s %v", name, args)
	return nil, nil
}

func TestMainMenuCleanupAllConfirmationAndInstalledCoreProtection(t *testing.T) {
	t.Setenv("TERM", "xterm")
	input, err := os.Open("/dev/null")
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	for _, test := range []struct {
		name, sequence, remainingCore string
		cleaned                       bool
	}{
		{name: "cancel-zero", sequence: "4\n0\n2\n"},
		{name: "cancel-q", sequence: "4\nq\n2\n"},
		{name: "cancel-Q", sequence: "4\nQ\n2\n"},
		{name: "confirmed", sequence: "4\n1\n\n2\n", cleaned: true},
		{name: "xray-still-installed", sequence: "4\n1\n\n2\n", remainingCore: domain.CoreXray},
		{name: "singbox-still-installed", sequence: "4\n1\n\n2\n", remainingCore: domain.CoreSingBox},
	} {
		t.Run(test.name, func(t *testing.T) {
			layout := system.Layout{Root: t.TempDir()}
			runner := &cleanupMenuRunner{t: t}
			var out menuTerminalBuffer
			a := app.New(provider.NewRegistry(xray.New(), singbox.New()), runner, layout, &out)
			a.RootCheck = func() error { return nil }
			a.LookPath = func(name string) (string, error) {
				if name == test.remainingCore {
					return "/usr/bin/" + name, nil
				}
				return "", exec.ErrNotFound
			}
			custom := layout.Resolve("/var/lib/proxyforge/custom.json")
			if err := system.AtomicWrite(custom, []byte("keep"), 0600); err != nil {
				t.Fatal(err)
			}
			for _, core := range []string{domain.CoreXray, domain.CoreSingBox} {
				if err := a.Store.Save(domain.NodeSpec{ManagedBy: "proxyforge", Core: core}); err != nil {
					t.Fatal(err)
				}
			}
			c := &commandSet{
				app: a, in: input, out: &out, errOut: &out,
				docker: dockerfeature.New(dockerfeature.Options{Root: layout.Root, RootCheck: func() error { return nil }, LookPath: func(string) (string, error) { return "", exec.ErrNotFound }}),
				reader: bufio.NewReader(menuByteReader{strings.NewReader(test.sequence)}),
			}
			if !c.interactiveUI() {
				t.Fatal("test must exercise terminal confirmation and pause behavior")
			}
			core, selected, err := c.selectCore(context.Background())
			if err != nil || !selected || core != domain.CoreSingBox {
				t.Fatalf("cleanup did not return to main menu: core=%q selected=%v err=%v", core, selected, err)
			}
			if test.cleaned {
				if _, err := os.Stat(layout.Resolve("/var/lib/proxyforge")); !os.IsNotExist(err) {
					t.Fatalf("confirmed cleanup retained data: %v", err)
				}
			} else {
				if data, err := os.ReadFile(custom); err != nil || string(data) != "keep" {
					t.Fatalf("cancelled or rejected cleanup changed data: %q, err=%v", data, err)
				}
				for _, core := range []string{domain.CoreXray, domain.CoreSingBox} {
					if _, err := a.Store.Load(core); err != nil {
						t.Fatalf("cancelled or rejected cleanup removed %s records: %v", core, err)
					}
				}
			}
			if runner.reloaded != test.cleaned {
				t.Fatal("cancelled or rejected cleanup executed service changes")
			}
			for _, want := range []string{"清理全部卸载残留", "两个内核均须已卸载", "包括其中的自定义文件和目录"} {
				if !strings.Contains(out.String(), want) {
					t.Fatalf("confirmation omitted %q: %s", want, out.String())
				}
			}
			if test.remainingCore != "" && !strings.Contains(out.String(), "仍检测到 "+test.remainingCore) {
				t.Fatalf("installed core rejection not shown: %s", out.String())
			}
			if strings.HasPrefix(test.name, "cancel-") && strings.Contains(out.String(), "按 Enter 返回菜单") {
				t.Fatal("cancelled cleanup required an extra Enter to return")
			}
		})
	}
}
