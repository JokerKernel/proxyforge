package accesspolicy_test

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"proxyforge/internal/domain"
	"proxyforge/internal/provider"
	"proxyforge/internal/provider/accesspolicy"
	"proxyforge/internal/provider/singbox"
	"proxyforge/internal/provider/xray"
)

func policyNode(core string) domain.NodeSpec {
	return domain.NodeSpec{Core: core, InboundTag: "main-in", Server: "server.example.com", Port: 443, SNI: "example.com", Target: "example.com:443", UserName: "one", UUID: "123e4567-e89b-42d3-a456-426614174000", PrivateKey: "private", PublicKey: "public", ShortID: "0123456789abcdef",
		RelayLinks:      []domain.RelayLink{{Name: "relay", UserName: "relay", UUID: "26831be5-da6c-4d2d-b983-9280601670ae", Enabled: true, Upstream: domain.LandingPeer{Server: "exit.example.com", Port: 443, SNI: "example.com", PublicKey: "public", ShortID: "0123456789abcdef", UUID: "5d5c5537-56a4-4b1a-a3bf-9e5a1b378769"}}},
		LandingAccesses: []domain.LandingAccess{{Name: "landing", UserName: "landing", UUID: "5d5c5537-56a4-4b1a-a3bf-9e5a1b378769", Security: "tls", Port: 33443, SNI: "pf-test.invalid", CertificateFile: "/tmp/cert.pem", KeyFile: "/tmp/key.pem", Enabled: true}}}
}

func rootOf(t *testing.T, b []byte) map[string]any {
	t.Helper()
	var root map[string]any
	if err := json.Unmarshal(b, &root); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestAccessRulesPreserveRelayAndFallbackAndRestoreOriginal(t *testing.T) {
	for _, p := range []provider.CoreProvider{xray.New(), singbox.New()} {
		t.Run(p.Name(), func(t *testing.T) {
			n := policyNode(p.Name())
			n.XrayFallbackGuard = p.Name() == "xray"
			n.XrayFallbackPort = 33444
			n.SingBoxFallbackGuard = p.Name() == "sing-box"
			n.SingBoxFallbackPort = 33445
			original, err := p.RenderServer(n)
			if err != nil {
				t.Fatal(err)
			}
			next := n
			next.AccessPolicy = domain.AccessPolicy{Mode: domain.AccessModeWhitelist, Whitelist: []domain.AccessEntry{{Type: "domain", Value: "example.com", IncludeSubdomains: true}, {Type: "ip", Value: "192.168.1.10"}}, Blacklist: []domain.AccessEntry{{Type: "domain", Value: "blocked.com", IncludeSubdomains: true}}}
			next.AccessPolicySniffing, err = accesspolicy.SnapshotSniffing(original, n)
			if err != nil {
				t.Fatal(err)
			}
			handler := p.(provider.AccessPolicyProvider)
			b, err := handler.PatchAccessPolicy(original, n, next)
			if err != nil {
				t.Fatal(err)
			}
			root := rootOf(t, b)
			key := "route"
			if p.Name() == "xray" {
				key = "routing"
			}
			rules := root[key].(map[string]any)["rules"].([]any)
			whiteRelay, whiteDirect, deny, relay := -1, -1, -1, -1
			for i, item := range rules {
				rule := item.(map[string]any)
				encoded, _ := json.Marshal(rule)
				if strings.Contains(string(encoded), "pf-access-") {
					if strings.Contains(string(encoded), "dokodemo-in") || strings.Contains(string(encoded), "singbox-fallback-in") {
						t.Fatal("policy includes REALITY control/fallback inbound")
					}
					out := rule["outbound"]
					if p.Name() == "xray" {
						out = rule["outboundTag"]
						if rule["domain"] != nil && rule["ip"] != nil {
							t.Fatal("Xray domain/IP matches incorrectly combined with AND")
						}
					}
					if out == "relay-out" {
						whiteRelay = i
					}
					if out == "direct" || out == "pf-access-direct" {
						whiteDirect = i
					}
					if rule["ruleTag"] == "pf-access-final" || (p.Name() == "sing-box" && rule["action"] == "reject") {
						deny = i
					}
				} else if rule["outboundTag"] == "relay-out" || rule["outbound"] == "relay-out" {
					relay = i
				}
			}
			if whiteRelay < 0 || whiteDirect <= whiteRelay || deny <= whiteDirect || relay <= deny {
				t.Fatalf("priority/order mismatch: whiteRelay=%d whiteDirect=%d deny=%d relay=%d", whiteRelay, whiteDirect, deny, relay)
			}
			// The policy must be reapplied after editing managed relay/landing users.
			changed := next
			changed.RelayLinks = append([]domain.RelayLink(nil), next.RelayLinks...)
			changed.RelayLinks[0].UUID = "11b50d17-8c92-4b5d-9449-245f3b70d7bd"
			if _, err := p.PatchLinks(b, next, changed); err != nil {
				t.Fatal(err)
			}
			restored, err := handler.PatchAccessPolicy(b, next, n)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(rootOf(t, original), rootOf(t, restored)) {
				t.Fatalf("disabling policy changed original configuration\noriginal=%s\nrestored=%s", original, restored)
			}
		})
	}
}

func TestPolicyDomainAndIPMatchesAndClientExceptions(t *testing.T) {
	for _, p := range []provider.CoreProvider{xray.New(), singbox.New()} {
		t.Run(p.Name(), func(t *testing.T) {
			n := policyNode(p.Name())
			n.AccessPolicy = domain.AccessPolicy{Mode: "blacklist", Whitelist: []domain.AccessEntry{{Type: "domain", Value: "allowed.com", IncludeSubdomains: true}, {Type: "domain", Value: "exact.com"}, {Type: "cidr", Value: "192.168.1.0/24"}}, Blacklist: []domain.AccessEntry{{Type: "domain", Value: "denied.com", IncludeSubdomains: true}}}
			config, err := p.RenderServer(n)
			if err != nil {
				t.Fatal(err)
			}
			root := rootOf(t, config)
			if p.Name() == "xray" {
				// White-list private exceptions get their own Freedom exit, preserving the original direct exit.
				var direct, white map[string]any
				for _, item := range root["outbounds"].([]any) {
					out := item.(map[string]any)
					if out["tag"] == "direct" {
						direct = out
					}
					if out["tag"] == "pf-access-direct" {
						white = out
					}
				}
				if white == nil || !reflect.DeepEqual(direct["streamSettings"], white["streamSettings"]) {
					t.Fatal("white-list exit lost direct IP settings")
				}
				if !strings.Contains(string(config), "domain:allowed.com") || !strings.Contains(string(config), "full:exact.com") {
					t.Fatal("domain match semantics incorrect")
				}
			} else {
				if !strings.Contains(string(config), `".allowed.com"`) || strings.Contains(string(config), `".exact.com"`) {
					t.Fatal("domain suffix/exact match semantics incorrect")
				}
			}
			client, err := p.RenderClient(n)
			if err != nil {
				t.Fatal(err)
			}
			root = rootOf(t, client)
			key := "route"
			if p.Name() == "xray" {
				key = "routing"
			}
			rules := root[key].(map[string]any)["rules"].([]any)
			first := rules[0].(map[string]any)
			if p.Name() == "xray" {
				if first["outboundTag"] != "proxy" {
					t.Fatal("private exception didn't use proxy")
				}
			} else if first["outbound"] != "proxy" {
				t.Fatal("private exception didn't use proxy")
			}
		})
	}
}

func TestPolicyRejectsMalformedManualRouting(t *testing.T) {
	for _, p := range []provider.CoreProvider{xray.New(), singbox.New()} {
		n := policyNode(p.Name())
		n.AccessPolicy = domain.AccessPolicy{Mode: "whitelist"}
		key := "route"
		if p.Name() == "xray" {
			key = "routing"
		}
		for _, config := range []string{"null", fmt.Sprintf(`{"%s":{"rules":"invalid"},"outbounds":[]}`, key), fmt.Sprintf(`{"%s":{"rules":["invalid"]},"outbounds":[]}`, key)} {
			if _, err := p.(provider.AccessPolicyProvider).PatchAccessPolicy([]byte(config), domain.NodeSpec{}, n); err == nil {
				t.Fatalf("%s accepted malformed routing: %s", p.Name(), config)
			}
		}
	}
}
