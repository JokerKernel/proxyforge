package app

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"proxyforge/internal/domain"
	"proxyforge/internal/provider/singbox"
	"proxyforge/internal/provider/xray"
	"proxyforge/internal/system"
)

func TestSetLogLevelValidatesBacksUpRestartsAndTracksManagedConfig(t *testing.T) {
	runner := &fakeRunner{}
	a, root := testApp(t, runner)
	p := singbox.New()
	node := domain.NodeSpec{
		ManagedBy: "proxyforge", Core: domain.CoreSingBox, InboundTag: "singbox-one", UserName: "one",
		Server: "203.0.113.10", Port: 443, SNI: "example.com", Target: "example.com:443",
		UUID: "123e4567-e89b-42d3-a456-426614174000", PrivateKey: "private", PublicKey: "public", ShortID: "0123456789abcdef",
	}
	config, err := p.RenderServer(node)
	if err != nil {
		t.Fatal(err)
	}
	configPath := a.Layout.Resolve(p.ConfigPath())
	if err := os.MkdirAll(filepath.Dir(configPath), 0750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, config, 0600); err != nil {
		t.Fatal(err)
	}
	node.ConfigSHA256 = system.SHA256(config)
	if err := a.Store.Save(node); err != nil {
		t.Fatal(err)
	}

	change, err := a.SetLogLevel(context.Background(), domain.CoreSingBox, "debug")
	if err != nil {
		t.Fatal(err)
	}
	if !change.Changed || !change.Restarted || change.Previous != "info" || change.Current != "debug" {
		t.Fatalf("change=%#v", change)
	}
	updated, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if current, err := p.CurrentLogLevel(updated); err != nil || current != "debug" {
		t.Fatalf("current=%q error=%v", current, err)
	}
	state, err := a.Store.Load(domain.CoreSingBox)
	if err != nil {
		t.Fatal(err)
	}
	if state.ConfigSHA256 != system.SHA256(updated) {
		t.Fatalf("state hash=%q config hash=%q", state.ConfigSHA256, system.SHA256(updated))
	}
	if !strings.Contains(runner.callLog(), "sing-box check -c") || !strings.Contains(runner.callLog(), "systemctl restart sing-box.service") {
		t.Fatalf("calls=%s", runner.callLog())
	}
	backups, err := filepath.Glob(filepath.Join(root, "var/lib/proxyforge/backups/sing-box", "*", "config.json"))
	if err != nil || len(backups) != 1 {
		t.Fatalf("backups=%v error=%v", backups, err)
	}
}

func TestSetLogLevelKeepsStoppedServiceStopped(t *testing.T) {
	runner := &fakeRunner{serviceStopped: true}
	a, _ := testApp(t, runner)
	p := xray.New()
	config, err := p.RenderServer(domain.NodeSpec{
		InboundTag: "xray-one", UserName: "one", Port: 443, SNI: "example.com", Target: "example.com:443",
		UUID: "123e4567-e89b-42d3-a456-426614174000", PrivateKey: "private", ShortID: "0123456789abcdef",
	})
	if err != nil {
		t.Fatal(err)
	}
	configPath := a.Layout.Resolve(p.ConfigPath())
	if err := os.MkdirAll(filepath.Dir(configPath), 0750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, config, 0600); err != nil {
		t.Fatal(err)
	}
	change, err := a.SetLogLevel(context.Background(), domain.CoreXray, "error")
	if err != nil {
		t.Fatal(err)
	}
	if !change.Changed || change.Restarted || !runner.stopped() {
		t.Fatalf("change=%#v stopped=%v", change, runner.stopped())
	}
	if strings.Contains(runner.callLog(), "systemctl restart") {
		t.Fatalf("stopped service was restarted: %s", runner.callLog())
	}
}

func TestSetLogLevelRestoresConfigWhenRestartFails(t *testing.T) {
	runner := &fakeRunner{failRestart: true}
	a, _ := testApp(t, runner)
	p := singbox.New()
	config, err := p.RenderServer(domain.NodeSpec{
		InboundTag: "singbox-one", UserName: "one", Port: 443, SNI: "example.com", Target: "example.com:443",
		UUID: "123e4567-e89b-42d3-a456-426614174000", PrivateKey: "private", ShortID: "0123456789abcdef",
	})
	if err != nil {
		t.Fatal(err)
	}
	configPath := a.Layout.Resolve(p.ConfigPath())
	if err := os.MkdirAll(filepath.Dir(configPath), 0750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, config, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := a.SetLogLevel(context.Background(), domain.CoreSingBox, "debug"); err == nil || !strings.Contains(err.Error(), "已恢复旧配置") {
		t.Fatalf("error=%v", err)
	}
	restored, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(restored, config) {
		t.Fatalf("config was not restored\nwant: %s\ngot: %s", config, restored)
	}
	if runner.stopped() {
		t.Fatal("running service was not restored")
	}
}

func TestServiceRequiresRoot(t *testing.T) {
	a := &App{RootCheck: func() error { return errors.New("root required") }}
	if _, err := a.Service(context.Background(), domain.CoreSingBox, "status"); err == nil || !strings.Contains(err.Error(), "root required") {
		t.Fatalf("error = %v, want root requirement", err)
	}
}
