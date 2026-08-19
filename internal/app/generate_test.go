package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"proxyforge/internal/domain"
	"proxyforge/internal/provider/singbox"
	"proxyforge/internal/provider/xray"
	"proxyforge/internal/system"
)

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

func TestGenerateReplacesEmptyPlaceholderWithoutBackup(t *testing.T) {
	r := &fakeRunner{port: freePort(t)}
	a, root := testApp(t, r)
	configPath := a.Layout.Resolve(singbox.New().ConfigPath())
	if err := system.AtomicWrite(configPath, []byte("{\n}\n"), 0600); err != nil {
		t.Fatal(err)
	}

	o := domain.GenerateOptions{
		Server: "server.example.com", Port: r.port, SNI: "www.example.com", NonInteractive: true,
	}
	if _, err := a.Generate(context.Background(), domain.CoreSingBox, o); err != nil {
		t.Fatal(err)
	}
	backups, err := filepath.Glob(filepath.Join(root, "var/lib/proxyforge/backups/sing-box/*/config.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(backups) != 0 {
		t.Fatalf("empty placeholder was backed up: %v", backups)
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

func TestGenerateDefaultsToFallbackGuardAndStandardConfigOptsOut(t *testing.T) {
	tests := []struct {
		core         string
		fallbackPort int
		marker       string
		configPath   string
	}{
		{domain.CoreSingBox, domain.DefaultSingBoxFallbackPort, `"tag": "singbox-fallback-in"`, singbox.New().ConfigPath()},
		{domain.CoreXray, domain.DefaultXrayFallbackPort, `"protocol": "dokodemo-door"`, xray.New().ConfigPath()},
	}
	for _, tt := range tests {
		t.Run(tt.core+" default", func(t *testing.T) {
			r := &fakeRunner{port: freePort(t)}
			a, _ := testApp(t, r)
			n, err := a.Generate(context.Background(), tt.core, domain.GenerateOptions{
				Server: "server.example.com", Port: r.port, SNI: "speed.cloudflare.com", NonInteractive: true,
			})
			if err != nil {
				t.Fatal(err)
			}
			if optionsFallbackPort(domain.GenerateOptions{
				SingBoxFallbackGuard: n.SingBoxFallbackGuard, SingBoxFallbackPort: n.SingBoxFallbackPort,
				XrayFallbackGuard: n.XrayFallbackGuard, XrayFallbackPort: n.XrayFallbackPort,
			}) != tt.fallbackPort {
				t.Fatalf("node=%#v", n)
			}
			config, err := os.ReadFile(a.Layout.Resolve(tt.configPath))
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(config), tt.marker) {
				t.Fatalf("default config missing %s: %s", tt.marker, config)
			}
		})

		t.Run(tt.core+" standard opt-out", func(t *testing.T) {
			r := &fakeRunner{port: freePort(t)}
			a, _ := testApp(t, r)
			n, err := a.Generate(context.Background(), tt.core, domain.GenerateOptions{
				Server: "server.example.com", Port: r.port, SNI: "speed.cloudflare.com", StandardConfig: true, NonInteractive: true,
			})
			if err != nil {
				t.Fatal(err)
			}
			if n.SingBoxFallbackGuard || n.SingBoxFallbackPort != 0 || n.XrayFallbackGuard || n.XrayFallbackPort != 0 {
				t.Fatalf("node=%#v", n)
			}
			config, err := os.ReadFile(a.Layout.Resolve(tt.configPath))
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(config), tt.marker) {
				t.Fatalf("standard config contains %s: %s", tt.marker, config)
			}
		})
	}
}

func TestGenerateXrayFallbackGuardAndResetPreservesMode(t *testing.T) {
	r := &fakeRunner{port: freePort(t)}
	a, _ := testApp(t, r)
	checkedPorts := map[int]int{}
	a.PortFree = func(port int) error {
		checkedPorts[port]++
		return nil
	}
	o := domain.GenerateOptions{
		Server: "server.example.com", Port: r.port, SNI: "speed.cloudflare.com", Target: "speed.cloudflare.com:443",
		XrayFallbackGuard: true, XrayFallbackPort: 15444, NonInteractive: true,
	}
	n, err := a.Generate(context.Background(), domain.CoreXray, o)
	if err != nil {
		t.Fatal(err)
	}
	if !n.XrayFallbackGuard || n.XrayFallbackPort != 15444 || checkedPorts[r.port] != 1 || checkedPorts[15444] != 1 {
		t.Fatalf("node=%#v checkedPorts=%v", n, checkedPorts)
	}
	config, err := os.ReadFile(a.Layout.Resolve(xray.New().ConfigPath()))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(config), `"protocol": "dokodemo-door"`) || !strings.Contains(string(config), `"target": "127.0.0.1:15444"`) {
		t.Fatalf("fallback guard config=%s", config)
	}

	reset, err := a.ResetCredentials(context.Background(), domain.CoreXray, domain.ResetOptions{SNI: "www.example.com", Target: "origin.example.com:8443"})
	if err != nil {
		t.Fatal(err)
	}
	if !reset.XrayFallbackGuard || reset.XrayFallbackPort != 15444 {
		t.Fatalf("reset node=%#v", reset)
	}
	config, err = os.ReadFile(a.Layout.Resolve(xray.New().ConfigPath()))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"address": "origin.example.com"`, `"port": 8443`, `"serverNames": [`, `"www.example.com"`, `"domain": [`} {
		if !strings.Contains(string(config), want) {
			t.Fatalf("reset config missing %s: %s", want, config)
		}
	}
}

func TestGenerateSingBoxFallbackGuardAndResetPreservesMode(t *testing.T) {
	r := &fakeRunner{port: freePort(t)}
	a, _ := testApp(t, r)
	checkedPorts := map[int]int{}
	a.PortFree = func(port int) error {
		checkedPorts[port]++
		return nil
	}
	o := domain.GenerateOptions{
		Server: "server.example.com", Port: r.port, SNI: "speed.cloudflare.com", Target: "speed.cloudflare.com:443",
		SingBoxFallbackGuard: true, SingBoxFallbackPort: 15445, SingBoxFallbackHTTPDomain: true, NonInteractive: true,
	}
	n, err := a.Generate(context.Background(), domain.CoreSingBox, o)
	if err != nil {
		t.Fatal(err)
	}
	if !n.SingBoxFallbackGuard || n.SingBoxFallbackPort != 15445 || !n.SingBoxFallbackHTTPDomain || checkedPorts[r.port] != 1 || checkedPorts[15445] != 1 {
		t.Fatalf("node=%#v checkedPorts=%v", n, checkedPorts)
	}
	config, err := os.ReadFile(a.Layout.Resolve(singbox.New().ConfigPath()))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"type": "direct"`, `"override_address": "speed.cloudflare.com"`, `"server": "127.0.0.1"`, `"server_port": 15445`} {
		if !strings.Contains(string(config), want) {
			t.Fatalf("fallback guard config missing %s: %s", want, config)
		}
	}

	reset, err := a.ResetCredentials(context.Background(), domain.CoreSingBox, domain.ResetOptions{SNI: "www.example.com", Target: "origin.example.com:8443"})
	if err != nil {
		t.Fatal(err)
	}
	if !reset.SingBoxFallbackGuard || reset.SingBoxFallbackPort != 15445 || !reset.SingBoxFallbackHTTPDomain {
		t.Fatalf("reset node=%#v", reset)
	}
	config, err = os.ReadFile(a.Layout.Resolve(singbox.New().ConfigPath()))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"override_address": "origin.example.com"`, `"override_port": 8443`, `"server_name": "www.example.com"`, `"domain": [`} {
		if !strings.Contains(string(config), want) {
			t.Fatalf("reset config missing %s: %s", want, config)
		}
	}
}

func TestGenerateRejectsInvalidSingBoxFallbackGuardOptions(t *testing.T) {
	r := &fakeRunner{port: freePort(t)}
	a, _ := testApp(t, r)
	base := domain.GenerateOptions{Server: "server.example.com", Port: r.port, SNI: "www.example.com", NonInteractive: true}
	tests := []struct {
		name string
		core string
		opts domain.GenerateOptions
		want string
	}{
		{"standard conflict", domain.CoreSingBox, func() domain.GenerateOptions {
			o := base
			o.StandardConfig = true
			o.SingBoxFallbackGuard = true
			return o
		}(), "不能与简化或回落防偷跑配置参数同时使用"},
		{"same public and fallback port", domain.CoreSingBox, func() domain.GenerateOptions {
			o := base
			o.SingBoxFallbackGuard = true
			o.SingBoxFallbackPort = o.Port
			return o
		}(), "不能与公网监听端口相同"},
		{"simplified conflict", domain.CoreSingBox, func() domain.GenerateOptions {
			o := base
			o.SingBoxFallbackGuard = true
			o.SimplifiedConfig = true
			return o
		}(), "不能与回落防偷跑配置同时启用"},
		{"HTTP domain without fallback mode", domain.CoreSingBox, func() domain.GenerateOptions {
			o := base
			o.SimplifiedConfig = true
			o.SingBoxFallbackHTTPDomain = true
			return o
		}(), "必须与回落防偷跑配置同时启用"},
		{"unsupported core", domain.CoreXray, func() domain.GenerateOptions { o := base; o.SingBoxFallbackGuard = true; return o }(), "仅支持 sing-box"},
		{"HTTP domain unsupported core", domain.CoreXray, func() domain.GenerateOptions { o := base; o.SingBoxFallbackHTTPDomain = true; return o }(), "仅支持 sing-box"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := a.Generate(context.Background(), tt.core, tt.opts)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error=%v", err)
			}
		})
	}
}

func TestGenerateRejectsInvalidXrayFallbackGuardOptions(t *testing.T) {
	r := &fakeRunner{port: freePort(t)}
	a, _ := testApp(t, r)
	base := domain.GenerateOptions{Server: "server.example.com", Port: r.port, SNI: "www.example.com", NonInteractive: true}
	tests := []struct {
		name string
		core string
		opts domain.GenerateOptions
		want string
	}{
		{"standard conflict", domain.CoreXray, func() domain.GenerateOptions {
			o := base
			o.StandardConfig = true
			o.XrayFallbackGuard = true
			return o
		}(), "不能与简化或回落防偷跑配置参数同时使用"},
		{"same public and fallback port", domain.CoreXray, func() domain.GenerateOptions {
			o := base
			o.XrayFallbackGuard = true
			o.XrayFallbackPort = o.Port
			return o
		}(), "不能与公网监听端口相同"},
		{"unsupported core", domain.CoreSingBox, func() domain.GenerateOptions { o := base; o.XrayFallbackGuard = true; return o }(), "仅支持 xray"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := a.Generate(context.Background(), tt.core, tt.opts)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error=%v", err)
			}
		})
	}
}

func TestCheckCoreInstalled(t *testing.T) {
	t.Run("installed", func(t *testing.T) {
		a, _ := testApp(t, &fakeRunner{})
		if err := a.CheckCoreInstalled(context.Background(), domain.CoreXray); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("missing binary", func(t *testing.T) {
		a, _ := testApp(t, &fakeRunner{missingBinary: true})
		err := a.CheckCoreInstalled(context.Background(), domain.CoreXray)
		if err == nil || !strings.Contains(err.Error(), "尚未安装 xray") || !strings.Contains(err.Error(), "内核二进制 xray") {
			t.Fatalf("error=%v", err)
		}
	})
	t.Run("missing systemd unit", func(t *testing.T) {
		a, _ := testApp(t, &fakeRunner{unitRemoved: true})
		err := a.CheckCoreInstalled(context.Background(), domain.CoreXray)
		if err == nil || !strings.Contains(err.Error(), "安装不完整") || !strings.Contains(err.Error(), "xray.service") {
			t.Fatalf("error=%v", err)
		}
	})
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

func TestGenerateRejectsManagedFallbackPortConflicts(t *testing.T) {
	tests := []struct {
		name  string
		core  string
		other domain.NodeSpec
		opts  domain.GenerateOptions
	}{
		{
			name: "public port conflicts with xray fallback", core: domain.CoreSingBox,
			other: domain.NodeSpec{ManagedBy: "proxyforge", Core: domain.CoreXray, Port: 8443, XrayFallbackGuard: true, XrayFallbackPort: 15443},
			opts:  domain.GenerateOptions{Server: "server.example.com", Port: 15443, SNI: "www.example.com", NonInteractive: true},
		},
		{
			name: "sing-box fallback conflicts with xray public", core: domain.CoreSingBox,
			other: domain.NodeSpec{ManagedBy: "proxyforge", Core: domain.CoreXray, Port: 15445},
			opts: domain.GenerateOptions{Server: "server.example.com", Port: 15443, SNI: "www.example.com", NonInteractive: true,
				SingBoxFallbackGuard: true, SingBoxFallbackPort: 15445},
		},
		{
			name: "fallback ports conflict", core: domain.CoreXray,
			other: domain.NodeSpec{ManagedBy: "proxyforge", Core: domain.CoreSingBox, Port: 15443, SingBoxFallbackGuard: true, SingBoxFallbackPort: 15445},
			opts: domain.GenerateOptions{Server: "server.example.com", Port: 8443, SNI: "www.example.com", NonInteractive: true,
				XrayFallbackGuard: true, XrayFallbackPort: 15445},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &fakeRunner{port: tt.opts.Port}
			a, _ := testApp(t, r)
			if err := a.Store.Save(tt.other); err != nil {
				t.Fatal(err)
			}
			_, err := a.Generate(context.Background(), tt.core, tt.opts)
			if err == nil || !strings.Contains(err.Error(), "端口") || !strings.Contains(err.Error(), tt.other.Core) {
				t.Fatalf("error=%v", err)
			}
			if strings.Contains(r.callLog(), "systemctl restart") {
				t.Fatalf("service changed on conflict: %s", r.callLog())
			}
		})
	}
}
