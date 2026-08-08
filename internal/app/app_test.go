package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
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
	mu               sync.Mutex
	calls            []string
	port             int
	failRestart      bool
	failEnable       bool
	failDisable      bool
	failRemove       bool
	incompleteRemove bool
	missingBinary    bool
	unitRemoved      bool
	keyGeneration    int
	serviceStopped   bool
	serviceEnabled   bool
}

func (f *fakeRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	call := name + " " + strings.Join(args, " ")
	f.calls = append(f.calls, call)
	if name == "sing-box" && len(args) > 0 && args[0] == "version" {
		if f.missingBinary {
			return nil, errors.New("sing-box not found")
		}
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
		if f.missingBinary {
			return nil, errors.New("xray not found")
		}
		return []byte("Xray 25.1.1\n"), nil
	}
	if name == "systemctl" && len(args) > 0 && args[0] == "show" && strings.Contains(strings.Join(args, " "), "LoadState") {
		if f.unitRemoved {
			return []byte("not-found\n"), nil
		}
		return []byte("loaded\n"), nil
	}
	if name == "systemctl" && len(args) > 0 && args[0] == "show" {
		return []byte("root\n"), nil
	}
	if name == "systemctl" && len(args) > 0 && args[0] == "is-active" {
		if f.serviceStopped {
			return []byte("inactive\n"), errors.New("service inactive")
		}
		return []byte("active\n"), nil
	}
	if name == "systemctl" && len(args) > 0 && args[0] == "is-enabled" {
		if f.unitRemoved {
			return []byte("not-found\n"), errors.New("unit not found")
		}
		if f.serviceEnabled {
			return []byte("enabled\n"), nil
		}
		return []byte("disabled\n"), errors.New("service disabled")
	}
	if name == "systemctl" && len(args) > 0 && args[0] == "stop" {
		f.serviceStopped = true
		return nil, nil
	}
	if name == "systemctl" && len(args) > 0 && args[0] == "start" {
		f.serviceStopped = false
		return nil, nil
	}
	if name == "systemctl" && len(args) > 0 && args[0] == "restart" {
		if f.failRestart {
			f.failRestart = false
			return nil, errors.New("injected restart failure")
		}
		f.serviceStopped = false
		return nil, nil
	}
	if name == "systemctl" && len(args) > 0 && args[0] == "enable" {
		if f.failEnable {
			return nil, errors.New("injected enable failure")
		}
		f.serviceEnabled = true
		return nil, nil
	}
	if name == "systemctl" && len(args) > 0 && args[0] == "disable" {
		if f.failDisable {
			return nil, errors.New("injected disable failure")
		}
		f.serviceStopped = true
		f.serviceEnabled = false
		return nil, nil
	}
	if name == "systemctl" && len(args) > 0 && args[0] == "daemon-reload" {
		return nil, nil
	}
	if name == "dpkg" || name == "rpm" {
		if f.failRemove {
			return nil, errors.New("injected package removal failure")
		}
		if !f.incompleteRemove {
			f.missingBinary = true
			f.unitRemoved = true
			f.serviceStopped = true
		}
		return nil, nil
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

func (f *fakeRunner) stopped() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.serviceStopped
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
	a.LookPath = func(name string) (string, error) {
		runner.mu.Lock()
		defer runner.mu.Unlock()
		if runner.missingBinary {
			return "", exec.ErrNotFound
		}
		return "/usr/bin/" + name, nil
	}
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

func TestGenerateOverwritePreserveRotateAndRollback(t *testing.T) {
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
	o := domain.GenerateOptions{Server: "server.example.com", Port: r.port, SNI: "www.example.com", Target: "www.example.com:443", UserName: "phone", InboundTag: "phone-in", NonInteractive: true}
	first, err := a.Generate(context.Background(), domain.CoreSingBox, o)
	if err != nil {
		t.Fatal(err)
	}
	if first.UUID == "" || first.ShortID == "" || first.UserName != "phone" || first.InboundTag != "phone-in" {
		t.Fatalf("missing credentials: %#v", first)
	}
	backups, err := filepath.Glob(filepath.Join(root, "var/lib/proxyforge/backups/sing-box/*/config.json"))
	if err != nil || len(backups) != 1 {
		t.Fatalf("backups=%v error=%v", backups, err)
	}
	if b, _ := os.ReadFile(backups[0]); string(b) != "foreign config" {
		t.Fatalf("backup=%q", b)
	}
	second, err := a.Generate(context.Background(), domain.CoreSingBox, o)
	if err != nil {
		t.Fatal(err)
	}
	if second.UUID != first.UUID || second.ShortID != first.ShortID || second.PrivateKey != first.PrivateKey {
		t.Fatal("credentials changed without rotation")
	}
	secondConfig, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	var customized map[string]any
	if err := json.Unmarshal(secondConfig, &customized); err != nil {
		t.Fatal(err)
	}
	customized["manual_top_level"] = map[string]any{"keep": true}
	customizedConfig, err := json.Marshal(customized)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, customizedConfig, 0600); err != nil {
		t.Fatal(err)
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
	if third.UserName != second.UserName {
		t.Fatal("reset changed user name")
	}
	if third.InboundTag != second.InboundTag {
		t.Fatal("reset changed inbound tag")
	}
	if third.ConfigSHA256 != "" {
		t.Fatal("manually modified config was incorrectly marked as fully managed")
	}
	assertManualConfigRetained := func() {
		t.Helper()
		b, readErr := os.ReadFile(configPath)
		if readErr != nil {
			t.Fatal(readErr)
		}
		var value map[string]any
		if err := json.Unmarshal(b, &value); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(value["manual_top_level"], map[string]any{"keep": true}) {
			t.Fatalf("manual configuration was not retained: %s", b)
		}
	}
	assertManualConfigRetained()
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
	assertManualConfigRetained()
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

func TestGenerateStopsActiveServiceBeforeFirstPortCheck(t *testing.T) {
	r := &fakeRunner{port: freePort(t)}
	a, _ := testApp(t, r)
	a.PortFree = func(int) error {
		if !r.stopped() {
			return errors.New("port is still held by the active service")
		}
		return nil
	}
	o := domain.GenerateOptions{Server: "server.example.com", Port: r.port, SNI: "www.example.com", NonInteractive: true}
	n, err := a.Generate(context.Background(), domain.CoreSingBox, o)
	if err != nil {
		t.Fatal(err)
	}
	if n.UserName != domain.DefaultUserName {
		t.Fatalf("default user name=%q", n.UserName)
	}
	if n.InboundTag != domain.DefaultInboundTag(domain.CoreSingBox) {
		t.Fatalf("default inbound tag=%q", n.InboundTag)
	}
	if r.stopped() {
		t.Fatal("service was not restarted after applying the config")
	}
	log := r.callLog()
	if !strings.Contains(log, "systemctl enable sing-box.service") {
		t.Fatalf("service was not enabled after applying the config:\n%s", log)
	}
	stopAt := strings.Index(log, "systemctl stop sing-box.service")
	restartAt := strings.Index(log, "systemctl restart sing-box.service")
	enableAt := strings.Index(log, "systemctl enable sing-box.service")
	if stopAt < 0 || restartAt < 0 || enableAt < 0 || stopAt >= restartAt || restartAt >= enableAt {
		t.Fatalf("service was not stopped before the config was applied:\n%s", log)
	}
}

func TestGenerateSimplifiedSingBoxConfigAndResetPreservesMode(t *testing.T) {
	r := &fakeRunner{port: freePort(t)}
	a, _ := testApp(t, r)
	o := domain.GenerateOptions{
		Server: "server.example.com", Port: r.port, SNI: "www.example.com",
		SimplifiedConfig: true, NonInteractive: true,
	}
	n, err := a.Generate(context.Background(), domain.CoreSingBox, o)
	if err != nil {
		t.Fatal(err)
	}
	if !n.SimplifiedConfig {
		t.Fatal("simplified mode was not saved")
	}
	config, err := os.ReadFile(a.Layout.Resolve(singbox.New().ConfigPath()))
	if err != nil {
		t.Fatal(err)
	}
	for _, unwanted := range []string{`"dns"`, `"default_domain_resolver"`, `"action": "resolve"`, `"tag": "block"`} {
		if strings.Contains(string(config), unwanted) {
			t.Fatalf("simplified config contains %s: %s", unwanted, config)
		}
	}
	reset, err := a.ResetCredentials(context.Background(), domain.CoreSingBox, domain.ResetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !reset.SimplifiedConfig {
		t.Fatal("credential reset changed simplified mode")
	}
}

func TestGenerateRollsBackWhenEnablingServiceFails(t *testing.T) {
	r := &fakeRunner{port: freePort(t), failEnable: true}
	a, root := testApp(t, r)
	o := domain.GenerateOptions{
		Server: "server.example.com", Port: r.port, SNI: "www.example.com", NonInteractive: true,
	}
	_, err := a.Generate(context.Background(), domain.CoreSingBox, o)
	if err == nil || !strings.Contains(err.Error(), "启用 sing-box.service 开机启动失败") {
		t.Fatalf("error=%v", err)
	}
	if _, statErr := os.Stat(filepath.Join(root, "etc/sing-box/config.json")); !os.IsNotExist(statErr) {
		t.Fatalf("new config was not rolled back: %v", statErr)
	}
	if _, loadErr := a.Store.Load(domain.CoreSingBox); !errors.Is(loadErr, system.ErrNoState) {
		t.Fatalf("new state was not rolled back: %v", loadErr)
	}
}

func TestGenerateRejectsSimplifiedConfigForXray(t *testing.T) {
	r := &fakeRunner{port: freePort(t)}
	a, _ := testApp(t, r)
	_, err := a.Generate(context.Background(), domain.CoreXray, domain.GenerateOptions{SimplifiedConfig: true})
	if err == nil || !strings.Contains(err.Error(), "仅支持 sing-box") {
		t.Fatalf("error=%v", err)
	}
}

func TestGenerateRestartsServiceWhenPortRemainsOccupied(t *testing.T) {
	r := &fakeRunner{port: freePort(t)}
	a, root := testApp(t, r)
	a.PortFree = func(int) error { return errors.New("address already in use") }
	o := domain.GenerateOptions{Server: "server.example.com", Port: r.port, SNI: "www.example.com", NonInteractive: true}
	if _, err := a.Generate(context.Background(), domain.CoreSingBox, o); err == nil || !strings.Contains(err.Error(), "address already in use") {
		t.Fatalf("error=%v", err)
	}
	if r.stopped() {
		t.Fatal("service was not restored after the port conflict")
	}
	log := r.callLog()
	stopAt := strings.Index(log, "systemctl stop sing-box.service")
	startAt := strings.Index(log, "systemctl start sing-box.service")
	if stopAt < 0 || startAt < 0 || stopAt >= startAt {
		t.Fatalf("service was not restored after the port conflict:\n%s", log)
	}
	if _, err := os.Stat(filepath.Join(root, "etc/sing-box/config.json")); !os.IsNotExist(err) {
		t.Fatalf("config should not be written on a port conflict: %v", err)
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
	for _, want := range []string{
		"systemctl disable --now sing-box.service",
		"systemctl daemon-reload",
		"systemctl is-enabled sing-box.service",
	} {
		if !strings.Contains(r.callLog(), want) {
			t.Fatalf("uninstall did not clean and verify systemd service with %q: %s", want, r.callLog())
		}
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

func TestUninstallVerificationFailureKeepsManagedConfigAndState(t *testing.T) {
	r := &fakeRunner{incompleteRemove: true}
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
	if err == nil || !strings.Contains(err.Error(), "卸载后核验失败") {
		t.Fatalf("error=%v, want verification failure", err)
	}
	for _, want := range []string{"二进制 sing-box", "systemd unit sing-box.service"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error=%v, want remaining artifact %q", err, want)
		}
	}
	if strings.Contains(err.Error(), "状态=active") || strings.Contains(err.Error(), "仍启用开机启动") {
		t.Fatalf("service was not stopped and disabled before package removal: %v", err)
	}
	if got, readErr := os.ReadFile(configPath); readErr != nil || !bytes.Equal(got, config) {
		t.Fatalf("config=%q error=%v", got, readErr)
	}
	if _, loadErr := a.Store.Load(domain.CoreSingBox); loadErr != nil {
		t.Fatalf("state was removed after failed verification: %v", loadErr)
	}
}

func TestUninstallAlreadyAbsentSkipsInstallerAndCleansManagedData(t *testing.T) {
	r := &fakeRunner{missingBinary: true, unitRemoved: true, serviceStopped: true}
	a, root := testApp(t, r)
	writeSupportedPlatform(t, root)
	config := []byte("managed xray config")
	configPath := a.Layout.Resolve(xray.New().ConfigPath())
	if err := os.MkdirAll(filepath.Dir(configPath), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, config, 0600); err != nil {
		t.Fatal(err)
	}
	if err := a.Store.Save(domain.NodeSpec{
		ManagedBy: "proxyforge", Core: domain.CoreXray, ConfigSHA256: system.SHA256(config),
	}); err != nil {
		t.Fatal(err)
	}

	if err := a.Uninstall(context.Background(), domain.CoreXray, install.Options{}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(r.callLog(), "bash ") {
		t.Fatalf("official installer should be skipped when already absent: %s", r.callLog())
	}
	if _, err := os.Stat(configPath); !os.IsNotExist(err) {
		t.Fatalf("managed config still exists: %v", err)
	}
	if _, err := a.Store.Load(domain.CoreXray); !errors.Is(err, system.ErrNoState) {
		t.Fatalf("state error = %v, want ErrNoState", err)
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

func TestCleanupRemovesOnlySelectedCoreResidue(t *testing.T) {
	r := &fakeRunner{missingBinary: true}
	a, root := testApp(t, r)
	singPaths := []string{
		a.Layout.Resolve("/etc/sing-box/config.json"),
		a.Layout.Resolve("/var/lib/sing-box/cache.db"),
		a.Layout.StatePath(domain.CoreSingBox),
		a.Layout.TrustPath(domain.CoreSingBox),
		filepath.Join(a.Layout.BackupRoot(domain.CoreSingBox), "old", "config.json"),
	}
	for _, path := range singPaths {
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("residue"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	xrayState := a.Layout.StatePath(domain.CoreXray)
	if err := os.MkdirAll(filepath.Dir(xrayState), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(xrayState, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	clientExport := filepath.Join(root, "user-client.json")
	if err := os.WriteFile(clientExport, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}

	if err := a.Cleanup(context.Background(), domain.CoreSingBox); err != nil {
		t.Fatal(err)
	}
	for _, path := range singPaths {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("residue still exists at %s: %v", path, err)
		}
	}
	for _, path := range []string{xrayState, clientExport} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("unrelated file was removed at %s: %v", path, err)
		}
	}
}

func TestCleanupAllRemovesBothCoresAndProxyForgeData(t *testing.T) {
	r := &fakeRunner{missingBinary: true}
	a, _ := testApp(t, r)
	paths := []string{
		a.Layout.Resolve("/etc/sing-box/config.json"),
		a.Layout.Resolve("/var/lib/sing-box/cache.db"),
		a.Layout.Resolve("/usr/local/etc/xray/config.json"),
		a.Layout.Resolve("/var/log/xray/access.log"),
		a.Layout.StatePath(domain.CoreSingBox),
		a.Layout.TrustPath(domain.CoreXray),
		filepath.Join(a.Layout.BackupRoot(domain.CoreXray), "old", "config.json"),
	}
	for _, path := range paths {
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("residue"), 0600); err != nil {
			t.Fatal(err)
		}
	}

	if err := a.Cleanup(context.Background(), "all"); err != nil {
		t.Fatal(err)
	}
	for _, path := range paths {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("residue still exists at %s: %v", path, err)
		}
	}
	if _, err := os.Stat(a.Layout.Resolve("/var/lib/proxyforge")); !os.IsNotExist(err) {
		t.Fatalf("ProxyForge data root still exists: %v", err)
	}
}

func TestCleanupRejectsUnknownTargetBeforeDeleting(t *testing.T) {
	r := &fakeRunner{missingBinary: true}
	a, root := testApp(t, r)
	marker := filepath.Join(root, "marker")
	if err := os.WriteFile(marker, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := a.Cleanup(context.Background(), "unknown"); err == nil {
		t.Fatal("expected unsupported target error")
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("marker changed after rejected cleanup: %v", err)
	}
}

func TestCleanupRefusesInstalledCoreBeforeDeleting(t *testing.T) {
	r := &fakeRunner{}
	a, _ := testApp(t, r)
	marker := a.Layout.Resolve("/etc/sing-box/config.json")
	if err := os.MkdirAll(filepath.Dir(marker), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(marker, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	err := a.Cleanup(context.Background(), domain.CoreSingBox)
	if err == nil || !strings.Contains(err.Error(), "请先执行 uninstall") {
		t.Fatalf("error=%v, want uninstall requirement", err)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("installed core data was removed: %v", err)
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

func TestServerConfigReadsCurrentActiveFile(t *testing.T) {
	r := &fakeRunner{}
	a, _ := testApp(t, r)
	path := a.Layout.Resolve(singbox.New().ConfigPath())
	want := []byte("{\"private_key\":\"server-secret\"}\n")
	if err := system.AtomicWrite(path, want, 0600); err != nil {
		t.Fatal(err)
	}
	got, err := a.ServerConfig(domain.CoreSingBox)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("server config=%q, want %q", got, want)
	}
	if _, err := a.ServerConfig(domain.CoreXray); err == nil || !strings.Contains(err.Error(), "尚未找到") {
		t.Fatalf("missing config error=%v", err)
	}
}

func TestClashClientOutput(t *testing.T) {
	r := &fakeRunner{}
	a, _ := testApp(t, r)
	n := domain.NodeSpec{
		ManagedBy: "proxyforge", Core: domain.CoreXray, Server: "server.example.com", Port: 8443,
		SNI: "www.example.com", UUID: "123e4567-e89b-42d3-a456-426614174000",
		PrivateKey: "must-not-leak", PublicKey: "public", ShortID: "0123456789abcdef",
	}
	if err := a.Store.Save(n); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "clash.yaml")
	b, err := a.ClientConfig(context.Background(), domain.CoreXray, ClientFormatClash, path, false)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "type: vless") || !strings.Contains(string(b), `public-key: "public"`) {
		t.Fatalf("unexpected Clash output:\n%s", b)
	}
	if strings.Contains(string(b), n.PrivateKey) {
		t.Fatal("Clash output leaked private key")
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("mode=%o", info.Mode().Perm())
	}
	if strings.Contains(r.callLog(), "xray run -test") {
		t.Fatal("Xray validator was incorrectly used for Clash YAML")
	}
}

func TestClientRejectsUnknownFormat(t *testing.T) {
	r := &fakeRunner{}
	a, _ := testApp(t, r)
	if _, err := a.ClientConfig(context.Background(), domain.CoreSingBox, "legacy-clash", "", false); err == nil || !strings.Contains(err.Error(), "native 或 clash") {
		t.Fatalf("error=%v, want supported formats", err)
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

func TestPrintInstallSuccessDistinguishesInstallAndUpgrade(t *testing.T) {
	var installed, upgraded bytes.Buffer
	printInstallSuccess(&installed, domain.CoreSingBox, "安装", "", "sing-box version 1.13.16", true)
	printInstallSuccess(&upgraded, domain.CoreXray, "升级", "Xray 25.1.1", "Xray 25.2.0", false)

	for _, want := range []string{"sing-box 安装成功", "版本：sing-box version 1.13.16", "服务：active（运行中）"} {
		if !strings.Contains(installed.String(), want) {
			t.Fatalf("install result missing %q: %q", want, installed.String())
		}
	}
	for _, want := range []string{"xray 升级成功", "版本：Xray 25.1.1  ->  Xray 25.2.0", "服务：inactive（尚未运行）"} {
		if !strings.Contains(upgraded.String(), want) {
			t.Fatalf("upgrade result missing %q: %q", want, upgraded.String())
		}
	}
}
