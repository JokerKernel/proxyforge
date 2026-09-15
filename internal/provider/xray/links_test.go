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
		LandingAccesses: []domain.LandingAccess{{Name: "us", UserName: "us", UUID: "landing-uuid", Enabled: true}},
		RelayLinks: []domain.RelayLink{{Name: "la", UserName: "la", UUID: "relay-uuid", Enabled: true, Upstream: domain.LandingPeer{
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
	if !xrayHasTaggedOutbound(root, "la") {
		t.Fatalf("missing relay outbound: %s", b)
	}
	if !xrayHasRelayRule(root, "la", "la") {
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
	if len(clients) != 1 || xrayHasTaggedOutbound(root, "la") {
		t.Fatalf("links not removed: %s", removed)
	}
	if root["manual_top_level"] != "keep" || !xrayHasTaggedOutbound(root, "manual-out") {
		t.Fatalf("manual fields were not preserved: %s", removed)
	}
}

func TestRenderTLSLandingAndRelay(t *testing.T) {
	p := New()
	n := domain.NodeSpec{
		InboundTag: "xray-one", Server: "relay.example.com", Port: 443, SNI: "relay.example.com", Target: "relay.example.com:443",
		UserName: "one", UUID: "base-uuid", PrivateKey: "private", PublicKey: "public", ShortID: "0123456789abcdef",
		LandingAccesses: []domain.LandingAccess{{
			Name: "tls-exit", UserName: "tls-exit", UUID: "landing-tls-uuid", Security: domain.LandingSecurityTLS,
			Port: 8443, SNI: "tls.example.com", CertificateFile: "/etc/tls/fullchain.pem", KeyFile: "/etc/tls/privkey.pem", Enabled: true,
		}},
		RelayLinks: []domain.RelayLink{{Name: "tls-upstream", UserName: "tls-upstream", UUID: "relay-uuid", Enabled: true, Upstream: domain.LandingPeer{
			Core: domain.CoreSingBox, Security: domain.LandingSecurityTLS, Server: "exit.example.com", Port: 8443,
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
	inbound := xrayTaggedObject(root["inbounds"].([]any), xrayLandingTLSInboundTag("tls-exit"))
	if inbound == nil || inbound["port"] != float64(8443) {
		t.Fatalf("TLS landing inbound missing: %s", b)
	}
	stream := inbound["streamSettings"].(map[string]any)
	tlsSettings := stream["tlsSettings"].(map[string]any)
	certificates := tlsSettings["certificates"].([]any)
	if stream["network"] != "raw" || stream["security"] != "tls" || certificates[0].(map[string]any)["keyFile"] != "/etc/tls/privkey.pem" {
		t.Fatalf("invalid TLS landing settings: %#v", stream)
	}
	outbound := xrayTaggedObject(root["outbounds"].([]any), xrayRelayOutboundTag("tls-upstream"))
	outboundStream := outbound["streamSettings"].(map[string]any)
	if outboundStream["security"] != "tls" || outboundStream["realitySettings"] != nil {
		t.Fatalf("invalid TLS relay outbound: %#v", outboundStream)
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
	if xrayTaggedObject(root["inbounds"].([]any), xrayLandingTLSInboundTag("tls-exit")) != nil || xrayHasTaggedOutbound(root, xrayRelayOutboundTag("tls-upstream")) {
		t.Fatalf("TLS links not removed: %s", removed)
	}
}

func xrayTaggedObject(items []any, tag string) map[string]any {
	for _, raw := range items {
		if object, ok := raw.(map[string]any); ok && object["tag"] == tag {
			return object
		}
	}
	return nil
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
