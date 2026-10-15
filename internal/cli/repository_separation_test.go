package cli

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"testing"

	"proxyforge/internal/app"
	"proxyforge/internal/provider"
	"proxyforge/internal/provider/singbox"
	"proxyforge/internal/provider/xray"
	"proxyforge/internal/system"
)

func TestNativeCommandTreeRejectsDocker(t *testing.T) {
	root := New("test")
	if _, _, err := root.Find([]string{"docker"}); err == nil {
		t.Fatal("native command still exposes Docker management")
	}
}

func TestNativeCleanupRemovesDataRootWithoutCallingDocker(t *testing.T) {
	layout := system.Layout{Root: t.TempDir()}
	var out bytes.Buffer
	runner := &cleanupMenuRunner{t: t}
	a := app.New(provider.NewRegistry(xray.New(), singbox.New()), runner, layout, &out)
	a.RootCheck = func() error { return nil }
	a.LookPath = func(name string) (string, error) {
		if name == "docker" {
			t.Fatal("native cleanup must not inspect Docker")
		}
		return "", exec.ErrNotFound
	}
	marker := layout.StatePath("xray")
	if err := system.AtomicWrite(marker, []byte("native-data"), 0600); err != nil {
		t.Fatal(err)
	}
	dockerData := layout.Resolve("/var/lib/proxyforge/docker/xray/state.json")
	if err := system.AtomicWrite(dockerData, []byte("docker-data"), 0600); err != nil {
		t.Fatal(err)
	}
	c := &commandSet{app: a, out: &out, errOut: &out, yes: true}
	cmd := c.cleanupCommand()
	if err := cmd.RunE(cmd, []string{"all"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("native cleanup retained its own data: %v", err)
	}
	if _, err := os.Stat(dockerData); !os.IsNotExist(err) {
		t.Fatalf("cleanup all retained %s: %v", dockerData, err)
	}
	if !runner.reloaded {
		t.Fatal("native cleanup did not refresh systemd")
	}
	// A second pass stays successful after the data root is already gone.
	if err := a.Cleanup(context.Background(), "all"); err != nil {
		t.Fatal(err)
	}
}
