package xray

import (
	"encoding/json"
	"testing"

	"proxyforge/internal/domain"
)

func TestRenderAndRemoveManagedLinks(t *testing.T) {
	p := New()
	n := domain.NodeSpec{
		InboundTag: "xray-one", Server: "relay.example.com", Port: 443, SNI: "relay.example.com", Target: "relay.example.com:443",
		UserName: "one", UUID: "base-uuid", PrivateKey: "private", PublicKey: "public", ShortID: "0123456789abcdef",
		LandingAccesses: []domain.LandingAccess{{Name: "us", UserName: "proxyforge-landing-us", UUID: "landing-uuid", Enabled: true}},
		RelayLinks: []domain.RelayLink{{Name: "la", UserName: "proxyforge-relay-la", UUID: "relay-uuid", Enabled: true, Upstream: domain.LandingPeer{
			Core: domain.CoreSingBox, Server: "exit.example.com", Port: 443, SNI: "exit.example.com", UUID: "upstream-uuid", PublicKey: "upstream-public", ShortID: "abcdef0123456789", Flow: domain.VisionFlow,
		}}},
	}
	b, err := p.RenderServer(n)
	if err != nil {
		t.Fatal(err)
	}
	var root map[string]any
	if err := json.Unmarshal(b, &root); err != nil {
		t.Fatal(err)
	}
	inbound := root["inbounds"].([]any)[0].(map[string]any)
	clients := inbound["settings"].(map[string]any)["clients"].([]any)
	if len(clients) != 3 {
		t.Fatalf("clients=%#v", clients)
	}
	if !xrayHasTaggedOutbound(root, "proxyforge-relay-la") {
		t.Fatalf("missing relay outbound: %s", b)
	}
	if !xrayHasRelayRule(root, "proxyforge-relay-la", "proxyforge-relay-la") {
		t.Fatalf("missing relay rule: %s", b)
	}
	root["manual_top_level"] = "keep"
	root["outbounds"] = append(root["outbounds"].([]any), map[string]any{"protocol": "freedom", "settings": map[string]any{}, "tag": "manual-out"})
	b, err = json.Marshal(root)
	if err != nil {
		t.Fatal(err)
	}

	next := n
	next.LandingAccesses, next.RelayLinks = nil, nil
	removed, err := p.PatchLinks(b, n, next)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(removed, &root); err != nil {
		t.Fatal(err)
	}
	inbound = root["inbounds"].([]any)[0].(map[string]any)
	clients = inbound["settings"].(map[string]any)["clients"].([]any)
	if len(clients) != 1 || xrayHasTaggedOutbound(root, "proxyforge-relay-la") {
		t.Fatalf("links not removed: %s", removed)
	}
	if root["manual_top_level"] != "keep" || !xrayHasTaggedOutbound(root, "manual-out") {
		t.Fatalf("manual fields were not preserved: %s", removed)
	}
}

func xrayHasTaggedOutbound(root map[string]any, tag string) bool {
	for _, raw := range root["outbounds"].([]any) {
		if raw.(map[string]any)["tag"] == tag {
			return true
		}
	}
	return false
}

func xrayHasRelayRule(root map[string]any, user, outbound string) bool {
	routing := root["routing"].(map[string]any)
	for _, raw := range routing["rules"].([]any) {
		rule := raw.(map[string]any)
		if rule["outboundTag"] == outbound && xrayStringListContains(rule["user"], user) {
			return true
		}
	}
	return false
}
