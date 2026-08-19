package singbox

import (
	"encoding/json"
	"testing"

	"proxyforge/internal/domain"
	"proxyforge/internal/provider"
)

func TestPatchOutboundIPStrategyUsesDomainResolver(t *testing.T) {
	p := New()
	config, err := p.RenderServer(domain.NodeSpec{
		InboundTag: "singbox-one", UserName: "one", Server: "203.0.113.10", Port: 443,
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
	if profile, err := p.CurrentDNSProfile(patched); err != nil || profile != provider.DNSProfileSystem {
		t.Fatalf("dns profile=%q error=%v", profile, err)
	}

	var root map[string]any
	if err := json.Unmarshal(patched, &root); err != nil {
		t.Fatal(err)
	}
	direct := singBoxOutboundByTag(t, root, "direct")
	if _, exists := direct["domain_strategy"]; exists {
		t.Fatalf("deprecated domain_strategy was written: %#v", direct)
	}
	resolver := direct["domain_resolver"].(map[string]any)
	if resolver["server"] != "local" || resolver["strategy"] != "prefer_ipv4" {
		t.Fatalf("domain_resolver=%#v", resolver)
	}
	route := root["route"].(map[string]any)
	defaultResolver := route["default_domain_resolver"].(map[string]any)
	if defaultResolver["server"] != "local" || defaultResolver["strategy"] != "prefer_ipv4" {
		t.Fatalf("default_domain_resolver=%#v", defaultResolver)
	}
	foundResolve := false
	for _, raw := range route["rules"].([]any) {
		rule := raw.(map[string]any)
		if rule["action"] == "resolve" {
			foundResolve = true
			if rule["strategy"] != "prefer_ipv4" || rule["server"] != "local" {
				t.Fatalf("resolve rule=%#v", rule)
			}
		}
	}
	if !foundResolve {
		t.Fatal("missing resolve rule")
	}
}

func TestCurrentOutboundIPStrategyReportsConflictAsCustom(t *testing.T) {
	p := New()
	config := []byte(`{
  "outbounds": [{"type":"direct","tag":"direct","domain_resolver":{"server":"local","strategy":"prefer_ipv4"}}],
  "route": {"default_domain_resolver":{"server":"local","strategy":"ipv4_only"},"rules":[]}
}`)
	if current, err := p.CurrentOutboundIPStrategy(config); err != nil || current != "custom" {
		t.Fatalf("current=%q error=%v", current, err)
	}
}

func singBoxOutboundByTag(t *testing.T, root map[string]any, tag string) map[string]any {
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
