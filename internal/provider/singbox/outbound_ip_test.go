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
	if _, exists := direct["domain_resolver"]; exists {
		t.Fatalf("outbound domain_resolver should stay unset when resolve rules exist: %#v", direct)
	}
	route := root["route"].(map[string]any)
	if resolver, ok := route["default_domain_resolver"].(string); !ok || resolver != "local" {
		t.Fatalf("default_domain_resolver should stay a server tag: %#v", route["default_domain_resolver"])
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

	restored, err := p.PatchOutboundIPStrategy(patched, provider.OutboundIPUnset)
	if err != nil {
		t.Fatal(err)
	}
	if current, err := p.CurrentOutboundIPStrategy(restored); err != nil || current != provider.OutboundIPUnset {
		t.Fatalf("restored current=%q error=%v", current, err)
	}
	if profile, err := p.CurrentDNSProfile(restored); err != nil || profile != provider.DNSProfileSystem {
		t.Fatalf("restored dns profile=%q error=%v", profile, err)
	}
	root = nil
	if err := json.Unmarshal(restored, &root); err != nil {
		t.Fatal(err)
	}
	direct = singBoxOutboundByTag(t, root, "direct")
	if _, exists := direct["domain_resolver"]; exists {
		t.Fatalf("restored outbound still has domain_resolver: %#v", direct)
	}
	if resolver, ok := root["route"].(map[string]any)["default_domain_resolver"].(string); !ok || resolver != "local" {
		t.Fatalf("restored default_domain_resolver=%#v", root["route"].(map[string]any)["default_domain_resolver"])
	}
	for _, raw := range root["route"].(map[string]any)["rules"].([]any) {
		rule := raw.(map[string]any)
		if rule["action"] == "resolve" {
			if _, exists := rule["strategy"]; exists {
				t.Fatalf("restored resolve still has strategy: %#v", rule)
			}
		}
	}
}

func TestCurrentOutboundIPStrategyReportsConflictAsCustom(t *testing.T) {
	p := New()
	config := []byte(`{
  "outbounds": [{"type":"direct","tag":"direct"}],
  "route": {"rules":[
    {"action":"resolve","server":"local","strategy":"prefer_ipv4"},
    {"action":"resolve","server":"local","strategy":"ipv4_only"}
  ]}
}`)
	if current, err := p.CurrentOutboundIPStrategy(config); err != nil || current != "custom" {
		t.Fatalf("current=%q error=%v", current, err)
	}
}

func TestPatchOutboundIPStrategyAddsResolveRuleForSimplifiedConfig(t *testing.T) {
	p := New()
	config, err := p.RenderServer(domain.NodeSpec{
		InboundTag: "singbox-one", UserName: "one", SimplifiedConfig: true,
		Server: "203.0.113.10", Port: 443, SNI: "example.com", Target: "example.com:443",
		UUID: "123e4567-e89b-42d3-a456-426614174000", PrivateKey: "private", ShortID: "abcd",
	})
	if err != nil {
		t.Fatal(err)
	}
	patched, err := p.PatchOutboundIPStrategy(config, provider.OutboundIPPreferIPv4)
	if err != nil {
		t.Fatal(err)
	}
	if current, err := p.CurrentOutboundIPStrategy(patched); err != nil || current != provider.OutboundIPPreferIPv4 {
		t.Fatalf("current=%q error=%v", current, err)
	}
	var root map[string]any
	if err := json.Unmarshal(patched, &root); err != nil {
		t.Fatal(err)
	}
	direct := singBoxOutboundByTag(t, root, "direct")
	if _, exists := direct["domain_resolver"]; exists {
		t.Fatalf("simplified config should use resolve rules: %#v", direct)
	}
	rules := root["route"].(map[string]any)["rules"].([]any)
	first := rules[0].(map[string]any)
	if first["action"] != "resolve" || first["server"] != "local" || first["strategy"] != "prefer_ipv4" {
		t.Fatalf("resolve rule=%#v", first)
	}
	if _, exists := root["route"].(map[string]any)["default_domain_resolver"]; exists {
		t.Fatalf("simplified config should not gain default_domain_resolver")
	}
	if !dnsIsOnlyLocal(root) {
		t.Fatalf("simplified config should only add a local DNS server for the resolve rule: %#v", root["dns"])
	}

	restored, err := p.PatchOutboundIPStrategy(patched, provider.OutboundIPUnset)
	if err != nil {
		t.Fatal(err)
	}
	if current, err := p.CurrentOutboundIPStrategy(restored); err != nil || current != provider.OutboundIPUnset {
		t.Fatalf("restored current=%q error=%v", current, err)
	}
	root = nil
	if err := json.Unmarshal(restored, &root); err != nil {
		t.Fatal(err)
	}
	if _, exists := root["dns"]; exists {
		t.Fatalf("simplified restore left dns: %#v route=%#v", root["dns"], root["route"])
	}
	for _, raw := range root["route"].(map[string]any)["rules"].([]any) {
		if raw.(map[string]any)["action"] == "resolve" {
			t.Fatalf("simplified restore left resolve rule: %#v", raw)
		}
	}
}

func TestPatchOutboundIPStrategyLeavesFallbackOnDualStack(t *testing.T) {
	p := New()
	config, err := p.RenderServer(domain.NodeSpec{
		InboundTag: "singbox-one", UserName: "one",
		Server: "203.0.113.10", Port: 443, SNI: "example.com", Target: "example.com:443",
		UUID: "123e4567-e89b-42d3-a456-426614174000", PrivateKey: "private", ShortID: "abcd",
		SingBoxFallbackGuard: true, SingBoxFallbackPort: 61432,
	})
	if err != nil {
		t.Fatal(err)
	}
	patched, err := p.PatchOutboundIPStrategy(config, provider.OutboundIPPreferIPv4)
	if err != nil {
		t.Fatal(err)
	}
	var root map[string]any
	if err := json.Unmarshal(patched, &root); err != nil {
		t.Fatal(err)
	}
	if current, err := p.CurrentOutboundIPStrategy(patched); err != nil || current != provider.OutboundIPPreferIPv4 {
		t.Fatalf("current=%q error=%v", current, err)
	}
	direct := singBoxOutboundByTag(t, root, "direct")
	if _, exists := direct["domain_resolver"]; exists {
		t.Fatalf("user outbound should not gain domain_resolver: %#v", direct)
	}
	fallback := singBoxOutboundByTag(t, root, fallbackDirectOutboundTag)
	if _, exists := fallback["domain_resolver"]; exists {
		t.Fatalf("fallback-direct should not gain domain_resolver: %#v", fallback)
	}
	if strategy, _ := fallback["domain_strategy"].(string); strategy != "" {
		t.Fatalf("fallback-direct should stay dual-stack: %#v", fallback)
	}
	rules := root["route"].(map[string]any)["rules"].([]any)
	allow := rules[1].(map[string]any)
	if allow["outbound"] != fallbackDirectOutboundTag || allow["strategy"] != nil {
		t.Fatalf("fallback allow rule=%#v", allow)
	}
	foundUserResolve := false
	for _, raw := range rules {
		rule := raw.(map[string]any)
		if rule["action"] == "resolve" && rule["strategy"] == "prefer_ipv4" {
			foundUserResolve = true
		}
	}
	if !foundUserResolve {
		t.Fatal("user resolve rule missing strategy")
	}
}

func TestPatchFallbackIPStrategyUpdatesFallbackDirectOnly(t *testing.T) {
	p := New()
	config, err := p.RenderServer(domain.NodeSpec{
		InboundTag: "singbox-one", UserName: "one",
		Server: "203.0.113.10", Port: 443, SNI: "example.com", Target: "example.com:443",
		UUID: "123e4567-e89b-42d3-a456-426614174000", PrivateKey: "private", ShortID: "abcd",
		SingBoxFallbackGuard: true, SingBoxFallbackPort: 61432,
	})
	if err != nil {
		t.Fatal(err)
	}
	userPatched, err := p.PatchOutboundIPStrategy(config, provider.OutboundIPPreferIPv6)
	if err != nil {
		t.Fatal(err)
	}
	patched, err := p.PatchFallbackIPStrategy(userPatched, provider.OutboundIPPreferIPv4)
	if err != nil {
		t.Fatal(err)
	}
	if current, err := p.CurrentFallbackIPStrategy(patched); err != nil || current != provider.OutboundIPPreferIPv4 {
		t.Fatalf("fallback current=%q error=%v", current, err)
	}
	if current, err := p.CurrentOutboundIPStrategy(patched); err != nil || current != provider.OutboundIPPreferIPv6 {
		t.Fatalf("user outbound=%q error=%v", current, err)
	}
	var root map[string]any
	if err := json.Unmarshal(patched, &root); err != nil {
		t.Fatal(err)
	}
	fallback := singBoxOutboundByTag(t, root, fallbackDirectOutboundTag)
	if singBoxStrategyValue(fallback["domain_resolver"]) != "prefer_ipv4" {
		t.Fatalf("fallback-direct=%#v", fallback)
	}
	if _, exists := fallback["domain_strategy"]; exists {
		t.Fatalf("deprecated domain_strategy was written: %#v", fallback)
	}
	restored, err := p.PatchFallbackIPStrategy(patched, provider.OutboundIPUnset)
	if err != nil {
		t.Fatal(err)
	}
	if current, err := p.CurrentFallbackIPStrategy(restored); err != nil || current != provider.OutboundIPUnset {
		t.Fatalf("restored=%q error=%v", current, err)
	}
}

func TestPatchOutboundIPStrategyMigratesLegacyFallbackRoute(t *testing.T) {
	p := New()
	config := []byte(`{
  "inbounds": [{"type":"direct","tag":"singbox-fallback-in","listen":"127.0.0.1","listen_port":61432}],
  "outbounds": [{"type":"direct","tag":"direct"}],
  "route": {"rules":[
    {"inbound":["singbox-fallback-in"],"action":"sniff"},
    {"inbound":["singbox-fallback-in"],"protocol":["tls"],"domain":["example.com"],"action":"route","outbound":"direct"},
    {"inbound":["singbox-fallback-in"],"action":"reject"}
  ]}
}`)
	patched, err := p.PatchOutboundIPStrategy(config, provider.OutboundIPIPv4Only)
	if err != nil {
		t.Fatal(err)
	}
	var root map[string]any
	if err := json.Unmarshal(patched, &root); err != nil {
		t.Fatal(err)
	}
	if _, exists := root["outbounds"].([]any); !exists {
		t.Fatal("outbounds missing")
	}
	_ = singBoxOutboundByTag(t, root, fallbackDirectOutboundTag)
	var allow map[string]any
	for _, raw := range root["route"].(map[string]any)["rules"].([]any) {
		rule := raw.(map[string]any)
		if rule["action"] == "route" {
			allow = rule
			break
		}
	}
	if allow == nil || allow["outbound"] != fallbackDirectOutboundTag {
		t.Fatalf("legacy fallback rule was not moved: %#v", allow)
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
