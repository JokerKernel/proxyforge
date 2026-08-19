package xray

import (
	"encoding/json"
	"strings"
	"testing"

	"proxyforge/internal/domain"
	"proxyforge/internal/provider"
)

func TestPatchOutboundIPStrategyUpdatesFreedomOnly(t *testing.T) {
	p := New()
	config, err := p.RenderServer(domain.NodeSpec{
		InboundTag: "xray-one", UserName: "one", Server: "203.0.113.10", Port: 443,
		SNI: "example.com", Target: "example.com:443",
		UUID: "123e4567-e89b-42d3-a456-426614174000", PrivateKey: "private", ShortID: "abcd",
	})
	if err != nil {
		t.Fatal(err)
	}
	if current, err := p.CurrentOutboundIPStrategy(config); err != nil || current != provider.OutboundIPUnset {
		t.Fatalf("current=%q error=%v", current, err)
	}

	patched, err := p.PatchOutboundIPStrategy(config, provider.OutboundIPPreferIPv4)
	if err != nil {
		t.Fatal(err)
	}
	if current, err := p.CurrentOutboundIPStrategy(patched); err != nil || current != provider.OutboundIPPreferIPv4 {
		t.Fatalf("patched current=%q error=%v", current, err)
	}
	var root map[string]any
	if err := json.Unmarshal(patched, &root); err != nil {
		t.Fatal(err)
	}
	if root["dns"].(map[string]any)["queryStrategy"] != "UseIP" {
		t.Fatalf("dns was changed: %#v", root["dns"])
	}
	direct := xrayOutboundByTag(t, root, "direct")
	if direct["settings"].(map[string]any)["domainStrategy"] != "UseIPv4v6" {
		t.Fatalf("direct=%#v", direct)
	}

	only, err := p.PatchOutboundIPStrategy(patched, provider.OutboundIPIPv6Only)
	if err != nil {
		t.Fatal(err)
	}
	if current, err := p.CurrentOutboundIPStrategy(only); err != nil || current != provider.OutboundIPIPv6Only {
		t.Fatalf("only current=%q error=%v", current, err)
	}
	if err := json.Unmarshal(only, &root); err != nil {
		t.Fatal(err)
	}
	direct = xrayOutboundByTag(t, root, "direct")
	if direct["settings"].(map[string]any)["domainStrategy"] != "ForceIPv6" {
		t.Fatalf("only direct=%#v", direct)
	}
}

func TestCurrentOutboundIPStrategyReportsCustomUnknownValue(t *testing.T) {
	p := New()
	config := []byte(`{"outbounds":[{"protocol":"freedom","settings":{"domainStrategy":"UseIPv4"},"tag":"direct"}]}`)
	if current, err := p.CurrentOutboundIPStrategy(config); err != nil || current != "custom" {
		t.Fatalf("current=%q error=%v", current, err)
	}
	if _, err := p.PatchOutboundIPStrategy(config, "prefer-ipv4-typo"); err == nil || !strings.Contains(err.Error(), "无效") {
		t.Fatalf("error=%v", err)
	}
}

func xrayOutboundByTag(t *testing.T, root map[string]any, tag string) map[string]any {
	t.Helper()
	for _, raw := range root["outbounds"].([]any) {
		outbound := raw.(map[string]any)
		if outbound["tag"] == tag {
			return outbound
		}
	}
	t.Fatalf("missing outbound %s", tag)
	return nil
}
