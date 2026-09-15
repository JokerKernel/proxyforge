package singbox

import (
	"encoding/json"
	"testing"

	"proxyforge/internal/domain"
)

func TestRenderAndRemoveManagedLinks(t *testing.T) {
	p := New()
	n := domain.NodeSpec{
		InboundTag: "singbox-one", Server: "relay.example.com", Port: 443, SNI: "relay.example.com", Target: "relay.example.com:443",
		UserName: "one", UUID: "base-uuid", PrivateKey: "private", PublicKey: "public", ShortID: "0123456789abcdef",
		LandingAccesses: []domain.LandingAccess{{Name: "us", UserName: "proxyforge-landing-us", UUID: "landing-uuid", Enabled: true}},
		RelayLinks: []domain.RelayLink{{Name: "la", UserName: "la", UUID: "relay-uuid", Enabled: true, Upstream: domain.LandingPeer{
			Core: domain.CoreXray, Server: "exit.example.com", Port: 443, SNI: "exit.example.com", UUID: "upstream-uuid", PublicKey: "upstream-public", ShortID: "abcdef0123456789", Flow: domain.VisionFlow,
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
	users := root["inbounds"].([]any)[0].(map[string]any)["users"].([]any)
	if len(users) != 3 {
		t.Fatalf("users=%#v", users)
	}
	if !singBoxHasTaggedOutbound(root, "proxyforge-relay-la") {
		t.Fatalf("missing relay outbound: %s", b)
	}
	if !singBoxHasRelayRule(root, "la", "proxyforge-relay-la") {
		t.Fatalf("missing relay rule: %s", b)
	}
	root["manual_top_level"] = "keep"
	root["outbounds"] = append(root["outbounds"].([]any), map[string]any{"type": "direct", "tag": "manual-out"})
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
	users = root["inbounds"].([]any)[0].(map[string]any)["users"].([]any)
	if len(users) != 1 || singBoxHasTaggedOutbound(root, "proxyforge-relay-la") {
		t.Fatalf("links not removed: %s", removed)
	}
	if root["manual_top_level"] != "keep" || !singBoxHasTaggedOutbound(root, "manual-out") {
		t.Fatalf("manual fields were not preserved: %s", removed)
	}
}

func TestRenderTLSLandingAndRelay(t *testing.T) {
	p := New()
	n := domain.NodeSpec{
		InboundTag: "singbox-one", Server: "relay.example.com", Port: 443, SNI: "relay.example.com", Target: "relay.example.com:443",
		UserName: "one", UUID: "base-uuid", PrivateKey: "private", PublicKey: "public", ShortID: "0123456789abcdef",
		LandingAccesses: []domain.LandingAccess{{
			Name: "tls-exit", UserName: "proxyforge-landing-tls-exit", UUID: "landing-tls-uuid", Security: domain.LandingSecurityTLS,
			Port: 8443, SNI: "tls.example.com", CertificateFile: "/etc/tls/fullchain.pem", KeyFile: "/etc/tls/privkey.pem", Enabled: true,
		}},
		RelayLinks: []domain.RelayLink{{Name: "tls-upstream", UserName: "tls-upstream", UUID: "relay-uuid", Enabled: true, Upstream: domain.LandingPeer{
			Core: domain.CoreXray, Security: domain.LandingSecurityTLS, Server: "exit.example.com", Port: 8443,
			SNI: "tls.example.com", UUID: "upstream-uuid", Flow: domain.VisionFlow,
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
	inbound := singBoxTaggedObject(root["inbounds"].([]any), singBoxLandingTLSInboundTag("tls-exit"))
	if inbound == nil || inbound["listen_port"] != float64(8443) {
		t.Fatalf("TLS landing inbound missing: %s", b)
	}
	tlsSettings := inbound["tls"].(map[string]any)
	if tlsSettings["enabled"] != true || tlsSettings["min_version"] != "1.3" || tlsSettings["key_path"] != "/etc/tls/privkey.pem" || tlsSettings["reality"] != nil {
		t.Fatalf("invalid TLS landing settings: %#v", tlsSettings)
	}
	outbound := singBoxTaggedObject(root["outbounds"].([]any), relayOutboundTag("tls-upstream"))
	outboundTLS := outbound["tls"].(map[string]any)
	if outboundTLS["enabled"] != true || outboundTLS["server_name"] != "tls.example.com" || outboundTLS["reality"] != nil {
		t.Fatalf("invalid TLS relay outbound: %#v", outboundTLS)
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
	if singBoxTaggedObject(root["inbounds"].([]any), singBoxLandingTLSInboundTag("tls-exit")) != nil || singBoxHasTaggedOutbound(root, relayOutboundTag("tls-upstream")) {
		t.Fatalf("TLS links not removed: %s", removed)
	}
}

func singBoxTaggedObject(items []any, tag string) map[string]any {
	for _, raw := range items {
		if object, ok := raw.(map[string]any); ok && object["tag"] == tag {
			return object
		}
	}
	return nil
}

func singBoxHasTaggedOutbound(root map[string]any, tag string) bool {
	for _, raw := range root["outbounds"].([]any) {
		if raw.(map[string]any)["tag"] == tag {
			return true
		}
	}
	return false
}

func singBoxHasRelayRule(root map[string]any, user, outbound string) bool {
	route := root["route"].(map[string]any)
	for _, raw := range route["rules"].([]any) {
		rule := raw.(map[string]any)
		if rule["outbound"] == outbound && stringListContains(rule["auth_user"], user) {
			return true
		}
	}
	return false
}
