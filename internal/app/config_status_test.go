package app

import (
	"testing"

	"proxyforge/internal/domain"
	"proxyforge/internal/provider"
	"proxyforge/internal/provider/xray"
	"proxyforge/internal/system"
)

func TestModifyConfigStatusReadsGeneratedXray(t *testing.T) {
	layout := system.Layout{Root: t.TempDir()}
	p := xray.New()
	node := domain.NodeSpec{
		ManagedBy: "proxyforge", Core: domain.CoreXray, InboundTag: "xray-one", UserName: "one",
		Server: "203.0.113.10", Port: 443, SNI: "www.example.com", Target: "www.example.com:443",
		UUID: "123e4567-e89b-42d3-a456-426614174000", PrivateKey: "private", PublicKey: "public", ShortID: "abcd",
		XrayFallbackGuard: true, XrayFallbackPort: 61431,
	}
	config, err := p.RenderServer(node)
	if err != nil {
		t.Fatal(err)
	}
	if err := system.AtomicWrite(layout.Resolve(p.ConfigPath()), config, 0600); err != nil {
		t.Fatal(err)
	}
	a := &App{Registry: provider.NewRegistry(p), Layout: layout, Store: system.StateStore{Layout: layout}}
	if err := a.Store.Save(node); err != nil {
		t.Fatal(err)
	}
	got := a.ModifyConfigStatus(domain.CoreXray)
	if !got.HasConfig || !got.HasFallback || got.SNI != "www.example.com" ||
		got.DNS != provider.DNSProfileSystem || got.OutboundIP != provider.OutboundIPUnset || got.FallbackIP != provider.OutboundIPUnset {
		t.Fatalf("status=%#v", got)
	}
}
