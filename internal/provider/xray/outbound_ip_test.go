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

	restored, err := p.PatchOutboundIPStrategy(only, provider.OutboundIPUnset)
	if err != nil {
		t.Fatal(err)
	}
	if current, err := p.CurrentOutboundIPStrategy(restored); err != nil || current != provider.OutboundIPUnset {
		t.Fatalf("restored current=%q error=%v", current, err)
	}
	if err := json.Unmarshal(restored, &root); err != nil {
		t.Fatal(err)
	}
	direct = xrayOutboundByTag(t, root, "direct")
	assertDirectHappyEyeballs(t, direct)
}

func TestPatchOutboundIPStrategyLeavesFallbackOnDualStack(t *testing.T) {
	p := New()
	config, err := p.RenderServer(domain.NodeSpec{
		InboundTag: "xray-one", UserName: "one", Server: "203.0.113.10", Port: 443,
		SNI: "example.com", Target: "example.com:443",
		UUID: "123e4567-e89b-42d3-a456-426614174000", PrivateKey: "private", ShortID: "abcd",
		XrayFallbackGuard: true, XrayFallbackPort: domain.DefaultXrayFallbackPort,
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
	direct := xrayOutboundByTag(t, root, "direct")
	if direct["settings"].(map[string]any)["domainStrategy"] != "UseIPv4v6" {
		t.Fatalf("direct=%#v", direct)
	}
	assertDirectHappyEyeballs(t, xrayOutboundByTag(t, root, fallbackDirectOutboundTag))
	rule := root["routing"].(map[string]any)["rules"].([]any)[0].(map[string]any)
	if rule["outboundTag"] != fallbackDirectOutboundTag {
		t.Fatalf("fallback allow rule=%#v", rule)
	}
}

func TestPatchOutboundIPStrategyMigratesLegacyFallbackRoute(t *testing.T) {
	p := New()
	config := []byte(`{
  "inbounds": [{"tag":"dokodemo-in","protocol":"dokodemo-door","listen":"127.0.0.1","port":61431}],
  "outbounds": [{"protocol":"freedom","settings":{"domainStrategy":"UseIP"},"tag":"direct"}],
  "routing": {"rules":[
    {"inboundTag":["dokodemo-in"],"domain":["example.com"],"outboundTag":"direct"},
    {"inboundTag":["dokodemo-in"],"outboundTag":"blocked-private"}
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
	if xrayOutboundByTag(t, root, "direct")["settings"].(map[string]any)["domainStrategy"] != "ForceIPv4" {
		t.Fatal("user outbound was not updated")
	}
	assertDirectHappyEyeballs(t, xrayOutboundByTag(t, root, fallbackDirectOutboundTag))
	rule := root["routing"].(map[string]any)["rules"].([]any)[0].(map[string]any)
	if rule["outboundTag"] != fallbackDirectOutboundTag {
		t.Fatalf("legacy fallback rule was not moved: %#v", rule)
	}
}

func TestPatchFallbackIPStrategyUpdatesFallbackDirectOnly(t *testing.T) {
	p := New()
	config, err := p.RenderServer(domain.NodeSpec{
		InboundTag: "xray-one", UserName: "one", Server: "203.0.113.10", Port: 443,
		SNI: "example.com", Target: "example.com:443",
		UUID: "123e4567-e89b-42d3-a456-426614174000", PrivateKey: "private", ShortID: "abcd",
		XrayFallbackGuard: true, XrayFallbackPort: domain.DefaultXrayFallbackPort,
	})
	if err != nil {
		t.Fatal(err)
	}
	if current, err := p.CurrentFallbackIPStrategy(config); err != nil || current != provider.OutboundIPUnset {
		t.Fatalf("current=%q error=%v", current, err)
	}
	patched, err := p.PatchFallbackIPStrategy(config, provider.OutboundIPPreferIPv4)
	if err != nil {
		t.Fatal(err)
	}
	if current, err := p.CurrentFallbackIPStrategy(patched); err != nil || current != provider.OutboundIPPreferIPv4 {
		t.Fatalf("patched=%q error=%v", current, err)
	}
	if current, err := p.CurrentOutboundIPStrategy(patched); err != nil || current != provider.OutboundIPUnset {
		t.Fatalf("user outbound changed: %q", current)
	}
	var root map[string]any
	if err := json.Unmarshal(patched, &root); err != nil {
		t.Fatal(err)
	}
	if xrayOutboundByTag(t, root, fallbackDirectOutboundTag)["settings"].(map[string]any)["domainStrategy"] != "UseIPv4v6" {
		t.Fatalf("fallback-direct=%#v", xrayOutboundByTag(t, root, fallbackDirectOutboundTag))
	}
	assertDirectHappyEyeballs(t, xrayOutboundByTag(t, root, "direct"))
	restored, err := p.PatchFallbackIPStrategy(patched, provider.OutboundIPUnset)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(restored, &root); err != nil {
		t.Fatal(err)
	}
	assertDirectHappyEyeballs(t, xrayOutboundByTag(t, root, fallbackDirectOutboundTag))
	assertDirectHappyEyeballs(t, xrayOutboundByTag(t, root, "direct"))
}

func TestPatchFallbackIPStrategyRejectsStandardConfig(t *testing.T) {
	p := New()
	config, err := p.RenderServer(domain.NodeSpec{
		InboundTag: "xray-one", UserName: "one", Server: "203.0.113.10", Port: 443,
		SNI: "example.com", Target: "example.com:443",
		UUID: "123e4567-e89b-42d3-a456-426614174000", PrivateKey: "private", ShortID: "abcd",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.PatchFallbackIPStrategy(config, provider.OutboundIPPreferIPv4); err == nil || !strings.Contains(err.Error(), "未启用回落") {
		t.Fatalf("error=%v", err)
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

func assertDirectHappyEyeballs(t *testing.T, outbound map[string]any) {
	t.Helper()
	if outbound["settings"].(map[string]any)["domainStrategy"] != "AsIs" {
		t.Fatalf("direct settings=%#v", outbound["settings"])
	}
	sockopt := outbound["streamSettings"].(map[string]any)["sockopt"].(map[string]any)
	if sockopt["domainStrategy"] != "UseIP" {
		t.Fatalf("direct sockopt=%#v", sockopt)
	}
	happy := sockopt["happyEyeballs"].(map[string]any)
	if happy["tryDelayMs"] != float64(xrayHappyEyeballsDelayMs) || happy["prioritizeIPv6"] != false {
		t.Fatalf("direct happyEyeballs=%#v", happy)
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
