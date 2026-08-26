package app

import (
	"context"
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
	a := &App{
		Registry: provider.NewRegistry(p), Layout: layout, Store: system.StateStore{Layout: layout},
		Services: system.ServiceManager{Runner: configStatusRunner{}},
	}
	if err := a.Store.Save(node); err != nil {
		t.Fatal(err)
	}
	orig := lookupSystemResolvers
	lookupSystemResolvers = func() []string { return []string{"9.9.9.9", "1.0.0.1"} }
	t.Cleanup(func() { lookupSystemResolvers = orig })
	got := a.ModifyConfigStatus(context.Background(), domain.CoreXray)
	if !got.HasConfig || !got.HasFallback || got.SNI != "www.example.com" ||
		got.DNS != provider.DNSProfileSystem || got.OutboundIP != provider.OutboundIPUnset ||
		got.FallbackIP != provider.OutboundIPUnset || got.ServiceUser != "xray" {
		t.Fatalf("status=%#v", got)
	}
	if len(got.DNSServers) != 2 || got.DNSServers[0] != "9.9.9.9" || got.DNSServers[1] != "1.0.0.1" {
		t.Fatalf("dns servers=%v", got.DNSServers)
	}
}

type configStatusRunner struct{}

func (configStatusRunner) Run(context.Context, string, ...string) ([]byte, error) {
	return []byte("xray\n"), nil
}
