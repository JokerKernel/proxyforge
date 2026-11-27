package accesspolicy_test

import (
	"encoding/json"
	"reflect"
	"testing"

	"proxyforge/internal/domain"
	"proxyforge/internal/provider"
	"proxyforge/internal/provider/accesspolicy"
	"proxyforge/internal/provider/singbox"
	"proxyforge/internal/provider/xray"
)

func TestIndependentRelayPoliciesRebuildAndRestore(t *testing.T) {
	for _, p := range []provider.CoreProvider{xray.New(), singbox.New()} {
		t.Run(p.Name(), func(t *testing.T) {
			n := policyNode(p.Name())
			first := n.RelayLinks[0]
			first.AccessPolicy = &domain.AccessPolicy{Mode: domain.AccessModeWhitelist, Whitelist: []domain.AccessEntry{
				{Type: "domain", Value: "relay.test", Ports: "80"},
				{Type: "domain", Value: "relay.test", Ports: "443"},
			}}
			second := first
			second.Name, second.UserName, second.UUID = "other", "other", "123e4567-e89b-42d3-a456-426614174099"
			second.AccessPolicy = &domain.AccessPolicy{Disabled: true, Mode: domain.AccessModeWhitelist}
			n.RelayLinks = []domain.RelayLink{first, second}
			base := n.WithoutAccessPolicies()
			original, err := p.RenderServer(base)
			if err != nil {
				t.Fatal(err)
			}
			n.AccessPolicySniffing, err = accesspolicy.SnapshotSniffing(original, n)
			if err != nil {
				t.Fatal(err)
			}
			handler := p.(provider.AccessPolicyProvider)
			active, err := handler.PatchAccessPolicy(original, base, n)
			if err != nil {
				t.Fatal(err)
			}
			for _, operation := range []string{"reapply", "links", "node-policy"} {
				t.Run(operation, func(t *testing.T) {
					var next []byte
					var err error
					if operation == "links" {
						next, err = p.PatchLinks(active, n, n)
					} else if operation == "node-policy" {
						changed := n
						changed.AccessPolicy = domain.AccessPolicy{Mode: domain.AccessModeWhitelist}
						next, err = handler.PatchAccessPolicy(active, n, changed)
						if err == nil {
							next, err = handler.PatchAccessPolicy(next, changed, n)
						}
					} else {
						next, err = handler.PatchAccessPolicy(active, n, n)
					}
					if err != nil || !reflect.DeepEqual(rootOf(t, next), rootOf(t, active)) {
						t.Fatalf("independent policy rebuild changed routing: %v", err)
					}
				})
			}
			off := n.WithoutAccessPolicies()
			restored, err := handler.PatchAccessPolicy(active, n, off)
			if err != nil || !reflect.DeepEqual(rootOf(t, restored), rootOf(t, original)) {
				t.Fatalf("disabling last relay policy did not restore original config: %v", err)
			}
			if len(n.RelayLinks[0].AccessPolicy.Whitelist) != 2 {
				t.Fatal("generated port merge changed independently removable records")
			}
		})
	}
}

func TestRelayRouteOrderSurvivesDNSAndIPChanges(t *testing.T) {
	for _, p := range []provider.CoreProvider{xray.New(), singbox.New()} {
		t.Run(p.Name(), func(t *testing.T) {
			n := policyNode(p.Name())
			n.SimplifiedConfig = p.Name() == domain.CoreSingBox
			n.RelayLinks[0].AccessPolicy = &domain.AccessPolicy{Mode: domain.AccessModeWhitelist}
			base := n.WithoutAccessPolicies()
			original, err := p.RenderServer(base)
			if err != nil {
				t.Fatal(err)
			}
			root := rootOf(t, original)
			key := "route"
			custom := map[string]any{"domain": []string{"manual.test"}, "action": "route", "outbound": "direct"}
			if p.Name() == domain.CoreXray {
				key = "routing"
				custom = map[string]any{"domain": []string{"full:manual.test"}, "outboundTag": "direct"}
			}
			root[key].(map[string]any)["rules"] = append(root[key].(map[string]any)["rules"].([]any), custom)
			original, _ = json.Marshal(root)
			n.AccessPolicySniffing, err = accesspolicy.SnapshotSniffing(original, n)
			if err != nil {
				t.Fatal(err)
			}
			active, err := p.(provider.AccessPolicyProvider).PatchAccessPolicy(original, base, n)
			if err != nil {
				t.Fatal(err)
			}
			applySettings := func(data []byte) []byte {
				t.Helper()
				data, err := p.(provider.DNSProfileProvider).PatchDNSProfile(data, provider.DNSProfilePublicGoogle)
				if err != nil {
					t.Fatal(err)
				}
				data, err = p.(provider.OutboundIPProvider).PatchOutboundIPStrategy(data, provider.OutboundIPPreferIPv4)
				if err != nil {
					t.Fatal(err)
				}
				return data
			}
			active, expected := applySettings(active), applySettings(original)
			active, err = p.(provider.AccessPolicyProvider).PatchAccessPolicy(active, n, n)
			if err != nil {
				t.Fatal(err)
			}
			restored, err := p.(provider.AccessPolicyProvider).PatchAccessPolicy(active, n, base)
			if err != nil || !reflect.DeepEqual(rootOf(t, restored), rootOf(t, expected)) {
				t.Fatalf("settings update moved the relay ahead of original rules: %v\ngot=%s\nwant=%s", err, restored, expected)
			}
		})
	}
}
