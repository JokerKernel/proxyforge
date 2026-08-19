package app

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"proxyforge/internal/domain"
	"proxyforge/internal/provider"
	"proxyforge/internal/provider/xray"
	"proxyforge/internal/system"
)

func TestSetDNSProfileValidatesRestartsAndTracksManagedConfig(t *testing.T) {
	runner := &fakeRunner{}
	a, _ := testApp(t, runner)
	p := xray.New()
	node := domain.NodeSpec{
		ManagedBy: "proxyforge", Core: domain.CoreXray, InboundTag: "xray-one", UserName: "one",
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

	change, err := a.SetDNSProfile(context.Background(), domain.CoreXray, provider.DNSProfilePublicCloudflare)
	if err != nil {
		t.Fatal(err)
	}
	if !change.Changed || !change.Restarted || change.Previous != provider.DNSProfileSystem || change.Current != provider.DNSProfilePublicCloudflare {
		t.Fatalf("change=%#v", change)
	}
	updated, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if current, err := p.CurrentDNSProfile(updated); err != nil || current != provider.DNSProfilePublicCloudflare {
		t.Fatalf("current=%q error=%v", current, err)
	}
	state, err := a.Store.Load(domain.CoreXray)
	if err != nil {
		t.Fatal(err)
	}
	if state.ConfigSHA256 != system.SHA256(updated) {
		t.Fatalf("state hash=%q config hash=%q", state.ConfigSHA256, system.SHA256(updated))
	}
	if !strings.Contains(runner.callLog(), "xray run -test -config") || !strings.Contains(runner.callLog(), "systemctl restart xray.service") {
		t.Fatalf("calls=%s", runner.callLog())
	}
}
