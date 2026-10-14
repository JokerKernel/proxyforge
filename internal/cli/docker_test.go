package cli

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"

	"proxyforge/internal/app"
	dockerfeature "proxyforge/internal/docker"
	"proxyforge/internal/provider"
	"proxyforge/internal/provider/singbox"
	"proxyforge/internal/provider/xray"
	"proxyforge/internal/system"
)

func TestDockerEntryReturnsToMainMenu(t *testing.T) {
	var out bytes.Buffer
	m := dockerfeature.New(dockerfeature.Options{Root: t.TempDir(), Out: &out, ErrOut: &out, RootCheck: func() error { return nil }, LookPath: func(string) (string, error) { return "", exec.ErrNotFound }})
	c := &commandSet{docker: m, reader: bufio.NewReader(strings.NewReader("3\n0\n2\n")), out: &out, errOut: &out}
	core, selected, err := c.selectCore(context.Background())
	if err != nil || !selected || core != "sing-box" {
		t.Fatalf("Docker return routing core=%s selected=%v err=%v", core, selected, err)
	}
	for _, part := range []string{"Xray（Docker）", "Docker 未安装", "部署/升级", "清理全部卸载残留"} {
		if !strings.Contains(out.String(), part) {
			t.Fatalf("menu missing %s: %s", part, out.String())
		}
	}
}
func TestDockerCommandTreeIsSeparate(t *testing.T) {
	root := New("test")
	for _, args := range [][]string{{"docker"}, {"docker", "prepare"}, {"docker", "deploy"}, {"docker", "upgrade"}, {"docker", "recover"}, {"docker", "config", "generate"}, {"docker", "config", "client"}, {"docker", "config", "access"}, {"docker", "service"}} {
		cmd, remaining, err := root.Find(args)
		if err != nil || len(remaining) != 0 || cmd.Name() != args[len(args)-1] {
			t.Fatalf("find %v: %v %v %v", args, cmd, remaining, err)
		}
	}
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetArgs([]string{"docker", "--help"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
}
func TestGlobalCleanupRejectsUnverifiableDockerBeforeNativeDeletion(t *testing.T) {
	root := t.TempDir()
	layout := system.Layout{Root: root}
	var out bytes.Buffer
	r := &cleanupMenuRunner{t: t}
	a := app.New(provider.NewRegistry(xray.New(), singbox.New()), r, layout, &out)
	a.RootCheck = func() error { return nil }
	a.LookPath = func(string) (string, error) { return "", exec.ErrNotFound }
	marker := layout.StatePath("xray")
	if err := system.AtomicWrite(marker, []byte("native-data"), 0600); err != nil {
		t.Fatal(err)
	}
	dockerData := layout.Resolve("/var/lib/proxyforge/docker/xray/state.json")
	if err := system.AtomicWrite(dockerData, []byte("docker-data"), 0600); err != nil {
		t.Fatal(err)
	}
	m := dockerfeature.New(dockerfeature.Options{Root: root, Out: &out, RootCheck: func() error { return nil }, LookPath: func(string) (string, error) { return "", errors.New("missing Docker") }})
	c := &commandSet{app: a, docker: m, out: &out, errOut: &out}
	if err := c.cleanupTarget(context.Background(), "all"); err == nil {
		t.Fatal("unverifiable Docker was skipped")
	}
	b, err := os.ReadFile(marker)
	if err != nil || string(b) != "native-data" {
		t.Fatal("native data deleted before Docker precheck")
	}
	if r.reloaded {
		t.Fatal("native mutation ran before Docker precheck")
	}
}
