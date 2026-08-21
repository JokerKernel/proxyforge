package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"proxyforge/internal/domain"
	"proxyforge/internal/install"
	"proxyforge/internal/provider/singbox"
	"proxyforge/internal/provider/xray"
	"proxyforge/internal/system"
)

func TestUninstallAutomaticallyCleansSelectedCoreData(t *testing.T) {
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
	for _, path := range []string{a.Layout.BackupRoot(domain.CoreSingBox), a.Layout.TrustPath(domain.CoreSingBox)} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("automatic cleanup left %s: %v", path, err)
		}
	}
	if !strings.Contains(r.callLog(), "dpkg --purge sing-box") {
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
	if _, err := os.Stat(a.Layout.Resolve("/var/lib/proxyforge")); !os.IsNotExist(err) {
		t.Fatalf("empty ProxyForge data root still exists: %v", err)
	}
}

func TestUninstallCleanupRemovesExternallyModifiedConfig(t *testing.T) {
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
	if _, err := os.Stat(configPath); !os.IsNotExist(err) {
		t.Fatalf("external config was not removed by confirmed cleanup: %v", err)
	}
	if _, err := a.Store.Load(domain.CoreSingBox); !errors.Is(err, system.ErrNoState) {
		t.Fatalf("state error = %v, want ErrNoState", err)
	}
}

func TestCleanupRemovesOnlySelectedCoreResidue(t *testing.T) {
	r := &fakeRunner{missingBinary: true, unitRemoved: true, serviceStopped: true}
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
	r := &fakeRunner{missingBinary: true, unitRemoved: true, serviceStopped: true}
	a, _ := testApp(t, r)
	paths := []string{
		a.Layout.Resolve("/etc/sing-box/config.json"),
		a.Layout.Resolve("/var/lib/sing-box/cache.db"),
		a.Layout.Resolve("/usr/local/etc/xray/config.json"),
		a.Layout.Resolve("/usr/local/bin/xray"),
		a.Layout.Resolve("/usr/local/share/xray/geosite.dat"),
		a.Layout.Resolve("/var/log/xray/access.log"),
		a.Layout.Resolve("/etc/systemd/system/xray@.service"),
		a.Layout.Resolve("/etc/systemd/system/xray.service.d/20-proxyforge-user.conf"),
		a.Layout.Resolve("/usr/lib/sysusers.d/proxyforge-xray.conf"),
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

func TestCleanupRemovesOnlyOwnedXrayServiceAccount(t *testing.T) {
	r := &fakeRunner{
		missingBinary: true, unitRemoved: true, serviceStopped: true,
		xrayUserExists: true, xrayGroupExists: true,
	}
	a, _ := testApp(t, r)
	marker := xrayServiceAccountOwnership{Version: 1, UID: "999", GID: "999"}
	data, err := json.Marshal(marker)
	if err != nil {
		t.Fatal(err)
	}
	if err := system.AtomicWrite(a.Layout.XrayServiceAccountMarkerPath(), append(data, '\n'), 0600); err != nil {
		t.Fatal(err)
	}

	if err := a.Cleanup(context.Background(), domain.CoreXray); err != nil {
		t.Fatal(err)
	}
	if r.xrayUserExists || r.xrayGroupExists {
		t.Fatal("owned xray service account was not removed")
	}
	for _, want := range []string{"userdel xray", "groupdel xray"} {
		if !strings.Contains(r.callLog(), want) {
			t.Fatalf("cleanup calls missing %q: %s", want, r.callLog())
		}
	}
	if _, err := os.Stat(a.Layout.XrayServiceAccountMarkerPath()); !os.IsNotExist(err) {
		t.Fatalf("ownership marker remains: %v", err)
	}
}

func TestCleanupPreservesUnmarkedExistingXrayServiceAccount(t *testing.T) {
	r := &fakeRunner{
		missingBinary: true, unitRemoved: true, serviceStopped: true,
		xrayUserExists: true, xrayGroupExists: true,
	}
	a, _ := testApp(t, r)
	if err := a.Cleanup(context.Background(), domain.CoreXray); err != nil {
		t.Fatal(err)
	}
	if !r.xrayUserExists || !r.xrayGroupExists {
		t.Fatal("unmarked pre-existing xray account was removed")
	}
	if strings.Contains(r.callLog(), "userdel ") || strings.Contains(r.callLog(), "groupdel ") {
		t.Fatalf("unexpected account deletion: %s", r.callLog())
	}
}

func TestCleanupRefusesChangedOwnedXrayServiceAccountIdentity(t *testing.T) {
	r := &fakeRunner{
		missingBinary: true, unitRemoved: true, serviceStopped: true,
		xrayUserExists: true, xrayGroupExists: true,
	}
	a, _ := testApp(t, r)
	marker := xrayServiceAccountOwnership{Version: 1, UID: "998", GID: "998"}
	data, err := json.Marshal(marker)
	if err != nil {
		t.Fatal(err)
	}
	if err := system.AtomicWrite(a.Layout.XrayServiceAccountMarkerPath(), append(data, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	config := a.Layout.Resolve("/usr/local/etc/xray/config.json")
	if err := os.MkdirAll(filepath.Dir(config), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(config, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}

	err = a.Cleanup(context.Background(), domain.CoreXray)
	if err == nil || !strings.Contains(err.Error(), "账号身份已变化") || !strings.Contains(err.Error(), "尚未删除其他残留") {
		t.Fatalf("error=%v", err)
	}
	if !r.xrayUserExists || !r.xrayGroupExists {
		t.Fatal("changed xray identity was removed")
	}
	if _, err := os.Stat(config); err != nil {
		t.Fatalf("cleanup changed files after account safety failure: %v", err)
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

func TestCleanupRefusesRemainingUnitWhenBinaryIsMissing(t *testing.T) {
	r := &fakeRunner{missingBinary: true, serviceStopped: true}
	a, _ := testApp(t, r)
	marker := a.Layout.Resolve("/usr/local/etc/xray/config.json")
	if err := os.MkdirAll(filepath.Dir(marker), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(marker, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	err := a.Cleanup(context.Background(), domain.CoreXray)
	if err == nil || !strings.Contains(err.Error(), "systemd unit xray.service") || !strings.Contains(err.Error(), "请先执行 uninstall") {
		t.Fatalf("error=%v, want remaining unit rejection", err)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("cleanup changed data before all uninstall checks passed: %v", err)
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
