package app

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"proxyforge/internal/domain"
	"proxyforge/internal/install"
	"proxyforge/internal/provider"
	"proxyforge/internal/provider/singbox"
	"proxyforge/internal/provider/xray"
	"proxyforge/internal/system"
)

type allowTarget struct{}

func (allowTarget) Validate(context.Context, string, string, string) ([]string, error) {
	return nil, nil
}

type fakeRunner struct {
	mu            sync.Mutex
	calls         []string
	port          int
	failRestart   bool
	failRemove    bool
	keyGeneration int
}

func (f *fakeRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	call := name + " " + strings.Join(args, " ")
	f.calls = append(f.calls, call)
	if name == "sing-box" && len(args) > 0 && args[0] == "version" {
		return []byte("sing-box version 1.14.0\n"), nil
	}
	if name == "sing-box" && len(args) > 1 && args[0] == "generate" {
		f.keyGeneration++
		return []byte(fmt.Sprintf("PrivateKey: private-key-%d\nPublicKey: public-key-%d\n", f.keyGeneration, f.keyGeneration)), nil
	}
	if name == "sing-box" && len(args) > 0 && args[0] == "check" {
		return nil, nil
	}
	if name == "xray" && len(args) > 0 && args[0] == "version" {
		return []byte("Xray 25.1.1\n"), nil
	}
	if name == "systemctl" && len(args) > 0 && args[0] == "show" {
		return []byte("root\n"), nil
	}
	if name == "systemctl" && len(args) > 0 && args[0] == "is-active" {
		return []byte("active\n"), nil
	}
	if name == "systemctl" && len(args) > 0 && args[0] == "restart" {
		if f.failRestart {
			f.failRestart = false
			return nil, errors.New("injected restart failure")
		}
		return nil, nil
	}
	if (name == "dpkg" || name == "rpm") && f.failRemove {
		return nil, errors.New("injected package removal failure")
	}
	if name == "sh" {
		return nil, errors.New("not installed")
	}
	return nil, nil
}

func (f *fakeRunner) close() {}
func (f *fakeRunner) callLog() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return strings.Join(f.calls, "\n")
}

func testApp(t *testing.T, runner *fakeRunner) (*App, string) {
	t.Helper()
	root := t.TempDir()
	var out bytes.Buffer
	a := New(provider.NewRegistry(singbox.New(), xray.New()), runner, system.Layout{Root: root}, &out)
	a.RootCheck = func() error { return nil }
	a.Targets = allowTarget{}
	a.PortFree = func(int) error { return nil }
	a.Listening = func(context.Context, int, time.Duration) error { return nil }
	return a, root
}

func freePort(t *testing.T) int {
	t.Helper()
	return 15443
}

func writeSupportedPlatform(t *testing.T, root string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(root, "proc/1"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "etc"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "proc/1/comm"), []byte("systemd\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "etc/os-release"), []byte("ID=debian\n"), 0644); err != nil {
		t.Fatal(err)
	}
}

func TestGenerateTakeoverPreserveRotateAndRollback(t *testing.T) {
	r := &fakeRunner{port: freePort(t)}
	defer r.close()
	a, root := testApp(t, r)
	configPath := a.Layout.Resolve(singbox.New().ConfigPath())
	if err := os.MkdirAll(filepath.Dir(configPath), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, []byte("foreign config"), 0600); err != nil {
		t.Fatal(err)
	}
	o := domain.GenerateOptions{Server: "server.example.com", Port: r.port, SNI: "www.example.com", Target: "www.example.com:443", TakeOver: true, NonInteractive: true}
	first, err := a.Generate(context.Background(), domain.CoreSingBox, o)
	if err != nil {
		t.Fatal(err)
	}
	if first.UUID == "" || first.ShortID == "" {
		t.Fatalf("missing credentials: %#v", first)
	}
	backups, err := filepath.Glob(filepath.Join(root, "var/lib/proxyforge/backups/sing-box/*/config.json"))
	if err != nil || len(backups) != 1 {
		t.Fatalf("backups=%v error=%v", backups, err)
	}
	if b, _ := os.ReadFile(backups[0]); string(b) != "foreign config" {
		t.Fatalf("backup=%q", b)
	}
	o.TakeOver = false
	second, err := a.Generate(context.Background(), domain.CoreSingBox, o)
	if err != nil {
		t.Fatal(err)
	}
	if second.UUID != first.UUID || second.ShortID != first.ShortID || second.PrivateKey != first.PrivateKey {
		t.Fatal("credentials changed without rotation")
	}
	third, err := a.ResetCredentials(context.Background(), domain.CoreSingBox, domain.ResetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if third.UUID == second.UUID || third.ShortID == second.ShortID || third.PrivateKey == second.PrivateKey || third.PublicKey == second.PublicKey {
		t.Fatal("credentials were not rotated")
	}
	if third.Server != second.Server || third.Port != second.Port || third.SNI != second.SNI || third.Target != second.Target {
		t.Fatal("reset changed node connection settings")
	}
	changedSNI, err := a.ResetCredentials(context.Background(), domain.CoreSingBox, domain.ResetOptions{SNI: "alt.example.com"})
	if err != nil {
		t.Fatal(err)
	}
	if changedSNI.SNI != "alt.example.com" || changedSNI.Target != "alt.example.com:443" {
		t.Fatalf("SNI reset result = SNI %q target %q", changedSNI.SNI, changedSNI.Target)
	}
	if changedSNI.Server != third.Server || changedSNI.Port != third.Port {
		t.Fatal("SNI reset changed server address or port")
	}
	beforeConfig, _ := os.ReadFile(configPath)
	beforeState, _ := os.ReadFile(a.Layout.StatePath(domain.CoreSingBox))
	r.failRestart = true
	_, err = a.ResetCredentials(context.Background(), domain.CoreSingBox, domain.ResetOptions{})
	if err == nil || !strings.Contains(err.Error(), "已恢复") {
		t.Fatalf("rollback error=%v", err)
	}
	afterConfig, _ := os.ReadFile(configPath)
	afterState, _ := os.ReadFile(a.Layout.StatePath(domain.CoreSingBox))
	if !bytes.Equal(beforeConfig, afterConfig) || !bytes.Equal(beforeState, afterState) {
		t.Fatal("rollback did not restore config and state")
	}
	log := r.callLog()
	if strings.Contains(log, "xray.service") {
		t.Fatalf("other provider was touched:\n%s", log)
	}
}

func TestGenerateRejectsManagedPortConflict(t *testing.T) {
	r := &fakeRunner{port: freePort(t)}
	a, _ := testApp(t, r)
	other := domain.NodeSpec{ManagedBy: "proxyforge", Core: domain.CoreXray, Port: r.port}
	if err := a.Store.Save(other); err != nil {
		t.Fatal(err)
	}
	o := domain.GenerateOptions{Server: "server.example.com", Port: r.port, SNI: "www.example.com", NonInteractive: true}
	_, err := a.Generate(context.Background(), domain.CoreSingBox, o)
	if err == nil || !strings.Contains(err.Error(), "xray") {
		t.Fatalf("error=%v", err)
	}
	if strings.Contains(r.callLog(), "systemctl restart") {
		t.Fatalf("service changed on conflict: %s", r.callLog())
	}
}

func TestUninstallBacksUpAndRemovesManagedConfigAndState(t *testing.T) {
	r := &fakeRunner{}
	a, root := testApp(t, r)
	writeSupportedPlatform(t, root)
	config := []byte("managed config")
	configPath := a.Layout.Resolve(singbox.New().ConfigPath())
	if err := os.MkdirAll(filepath.Dir(configPath), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, config, 0600); err != nil {
		t.Fatal(err)
	}
	if err := a.Store.Save(domain.NodeSpec{
		ManagedBy: "proxyforge", Core: domain.CoreSingBox, ConfigSHA256: system.SHA256(config),
	}); err != nil {
		t.Fatal(err)
	}
	otherState := domain.NodeSpec{ManagedBy: "proxyforge", Core: domain.CoreXray, Port: 8443}
	if err := a.Store.Save(otherState); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(a.Layout.TrustPath(domain.CoreSingBox)), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(a.Layout.TrustPath(domain.CoreSingBox), []byte("trusted\n"), 0600); err != nil {
		t.Fatal(err)
	}

	if err := a.Uninstall(context.Background(), domain.CoreSingBox, install.Options{}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(configPath); !os.IsNotExist(err) {
		t.Fatalf("managed config still exists: %v", err)
	}
	if _, err := a.Store.Load(domain.CoreSingBox); !errors.Is(err, system.ErrNoState) {
		t.Fatalf("state error = %v, want ErrNoState", err)
	}
	if got, err := a.Store.Load(domain.CoreXray); err != nil || got.Port != otherState.Port {
		t.Fatalf("other core state changed: state=%#v error=%v", got, err)
	}
	backups, err := filepath.Glob(filepath.Join(root, "var/lib/proxyforge/backups/sing-box/*/config.json"))
	if err != nil || len(backups) != 1 {
		t.Fatalf("backups=%v error=%v", backups, err)
	}
	if got, _ := os.ReadFile(backups[0]); !bytes.Equal(got, config) {
		t.Fatalf("backup=%q, want %q", got, config)
	}
	if _, err := os.Stat(a.Layout.TrustPath(domain.CoreSingBox)); err != nil {
		t.Fatalf("trust record was not retained: %v", err)
	}
	if !strings.Contains(r.callLog(), "dpkg --remove sing-box") {
		t.Fatalf("package removal was not called: %s", r.callLog())
	}
}

func TestUninstallFailureKeepsManagedConfigAndState(t *testing.T) {
	r := &fakeRunner{failRemove: true}
	a, root := testApp(t, r)
	writeSupportedPlatform(t, root)
	config := []byte("managed config")
	configPath := a.Layout.Resolve(singbox.New().ConfigPath())
	if err := os.MkdirAll(filepath.Dir(configPath), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, config, 0600); err != nil {
		t.Fatal(err)
	}
	state := domain.NodeSpec{ManagedBy: "proxyforge", Core: domain.CoreSingBox, ConfigSHA256: system.SHA256(config)}
	if err := a.Store.Save(state); err != nil {
		t.Fatal(err)
	}

	err := a.Uninstall(context.Background(), domain.CoreSingBox, install.Options{})
	if err == nil || !strings.Contains(err.Error(), "injected package removal failure") {
		t.Fatalf("error=%v, want package removal failure", err)
	}
	if got, readErr := os.ReadFile(configPath); readErr != nil || !bytes.Equal(got, config) {
		t.Fatalf("config=%q error=%v", got, readErr)
	}
	if _, loadErr := a.Store.Load(domain.CoreSingBox); loadErr != nil {
		t.Fatalf("state was removed after failed uninstall: %v", loadErr)
	}
}

func TestUninstallPreservesExternallyModifiedConfig(t *testing.T) {
	r := &fakeRunner{}
	a, root := testApp(t, r)
	writeSupportedPlatform(t, root)
	configPath := a.Layout.Resolve(singbox.New().ConfigPath())
	if err := os.MkdirAll(filepath.Dir(configPath), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, []byte("external edit"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := a.Store.Save(domain.NodeSpec{
		ManagedBy: "proxyforge", Core: domain.CoreSingBox, ConfigSHA256: system.SHA256([]byte("old managed config")),
	}); err != nil {
		t.Fatal(err)
	}

	if err := a.Uninstall(context.Background(), domain.CoreSingBox, install.Options{}); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(configPath); err != nil || string(got) != "external edit" {
		t.Fatalf("external config=%q error=%v", got, err)
	}
	if _, err := a.Store.Load(domain.CoreSingBox); !errors.Is(err, system.ErrNoState) {
		t.Fatalf("state error = %v, want ErrNoState", err)
	}
}

func TestClientOutputPermissionsAndForce(t *testing.T) {
	r := &fakeRunner{}
	a, _ := testApp(t, r)
	n := domain.NodeSpec{ManagedBy: "proxyforge", Core: domain.CoreSingBox, Server: "server.example.com", Port: 443, SNI: "www.example.com", UUID: "123e4567-e89b-42d3-a456-426614174000", PublicKey: "public", ShortID: "0123456789abcdef"}
	if err := a.Store.Save(n); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "client.json")
	if _, err := a.Client(context.Background(), domain.CoreSingBox, path, false); err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0600 {
		t.Fatalf("mode=%o", info.Mode().Perm())
	}
	if _, err := a.Client(context.Background(), domain.CoreSingBox, path, false); err == nil {
		t.Fatal("expected existing-file refusal")
	}
	if _, err := a.Client(context.Background(), domain.CoreSingBox, path, true); err != nil {
		t.Fatal(err)
	}
}

func TestClientRequiresRoot(t *testing.T) {
	a := &App{RootCheck: func() error { return errors.New("root required") }}
	if _, err := a.Client(context.Background(), domain.CoreSingBox, "", false); err == nil || !strings.Contains(err.Error(), "root required") {
		t.Fatalf("error = %v, want root requirement", err)
	}
}

func TestServiceRequiresRoot(t *testing.T) {
	a := &App{RootCheck: func() error { return errors.New("root required") }}
	if _, err := a.Service(context.Background(), domain.CoreSingBox, "status"); err == nil || !strings.Contains(err.Error(), "root required") {
		t.Fatalf("error = %v, want root requirement", err)
	}
}

func TestVersionMatches(t *testing.T) {
	for _, tt := range []struct {
		actual, requested string
		want              bool
	}{{"sing-box version 1.14.0", "1.14.0", true}, {"Xray 25.1.1 (Xray, Penetrates Everything.)", "v25.1.1", true}, {"Xray 25.1.10", "25.1.1", false}} {
		if got := versionMatches(tt.actual, tt.requested); got != tt.want {
			t.Errorf("versionMatches(%q,%q)=%v", tt.actual, tt.requested, got)
		}
	}
}

func TestInstalledServiceRunning(t *testing.T) {
	tests := []struct {
		name     string
		status   domain.ServiceStatus
		checkErr error
		running  bool
		wantErr  bool
	}{
		{name: "active", status: domain.ServiceStatus{Active: true, Detail: "active"}, running: true},
		{name: "inactive is valid after first install", status: domain.ServiceStatus{Detail: "inactive"}, checkErr: errors.New("exit status 3")},
		{name: "failed", status: domain.ServiceStatus{Detail: "failed"}, checkErr: errors.New("exit status 3"), wantErr: true},
		{name: "unknown", status: domain.ServiceStatus{Detail: "activating"}, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			running, err := installedServiceRunning(tt.status, tt.checkErr)
			if running != tt.running || (err != nil) != tt.wantErr {
				t.Fatalf("running=%v error=%v", running, err)
			}
		})
	}
}
