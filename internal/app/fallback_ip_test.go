package app

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"proxyforge/internal/domain"
	"proxyforge/internal/provider"
	"proxyforge/internal/provider/xray"
	"proxyforge/internal/system"
)

func TestSetFallbackIPStrategyUpdatesFallbackOnly(t *testing.T) {
	runner := &fakeRunner{}
	a, _ := testApp(t, runner)
	p := xray.New()
	node := domain.NodeSpec{
		ManagedBy: "proxyforge", Core: domain.CoreXray, InboundTag: "xray-one", UserName: "one",
		Server: "203.0.113.10", Port: 443, SNI: "example.com", Target: "example.com:443",
		UUID: "123e4567-e89b-42d3-a456-426614174000", PrivateKey: "private", PublicKey: "public", ShortID: "0123456789abcdef",
		XrayFallbackGuard: true, XrayFallbackPort: domain.DefaultXrayFallbackPort,
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

	change, err := a.SetFallbackIPStrategy(context.Background(), domain.CoreXray, provider.OutboundIPIPv4Only)
	if err != nil {
		t.Fatal(err)
	}
	if !change.Changed || change.Previous != provider.OutboundIPUnset || change.Current != provider.OutboundIPIPv4Only {
		t.Fatalf("change=%#v", change)
	}
	updated, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if current, err := p.CurrentFallbackIPStrategy(updated); err != nil || current != provider.OutboundIPIPv4Only {
		t.Fatalf("fallback=%q error=%v", current, err)
	}
	if current, err := p.CurrentOutboundIPStrategy(updated); err != nil || current != provider.OutboundIPUnset {
		t.Fatalf("user outbound=%q error=%v", current, err)
	}
}

func TestHasFallbackUsesNodeState(t *testing.T) {
	a, _ := testApp(t, &fakeRunner{})
	if a.HasFallback(domain.CoreXray) {
		t.Fatal("expected no fallback")
	}
	if err := a.Store.Save(domain.NodeSpec{ManagedBy: "proxyforge", Core: domain.CoreXray, XrayFallbackGuard: true}); err != nil {
		t.Fatal(err)
	}
	if !a.HasFallback(domain.CoreXray) || a.HasFallback(domain.CoreSingBox) {
		t.Fatal("fallback detection mismatch")
	}
}
