package accesspolicy_test

import (
	"reflect"
	"testing"

	"proxyforge/internal/domain"
	"proxyforge/internal/provider"
	"proxyforge/internal/provider/accesspolicy"
	"proxyforge/internal/provider/singbox"
	"proxyforge/internal/provider/xray"
)

func TestAccessSwitchRestoresOriginalServerAndClientRules(t *testing.T) {
	for _, p := range []provider.CoreProvider{xray.New(), singbox.New()} {
		for _, mode := range []string{domain.AccessModeDefault, domain.AccessModeWhitelist} {
			t.Run(p.Name()+"/"+mode, func(t *testing.T) {
				n := policyNode(p.Name())
				n.XrayFallbackGuard, n.XrayFallbackPort = p.Name() == domain.CoreXray, 33444
				n.SingBoxFallbackGuard, n.SingBoxFallbackPort = p.Name() == domain.CoreSingBox, 33445
				original, err := p.RenderServer(n)
				if err != nil {
					t.Fatal(err)
				}
				enabled := n
				enabled.AccessPolicy = domain.AccessPolicy{Mode: mode,
					Whitelist: []domain.AccessEntry{{Type: domain.AccessEntryDomain, Value: "allowed.com", IncludeSubdomains: true}, {Type: domain.AccessEntryCIDR, Value: "192.168.1.0/24"}},
					Blacklist: []domain.AccessEntry{{Type: domain.AccessEntryDomain, Value: "denied.com", IncludeSubdomains: true}},
				}
				enabled.AccessPolicySniffing, err = accesspolicy.SnapshotSniffing(original, n)
				if err != nil {
					t.Fatal(err)
				}
				handler := p.(provider.AccessPolicyProvider)
				active, err := handler.PatchAccessPolicy(original, n, enabled)
				if err != nil {
					t.Fatal(err)
				}
				disabled := enabled
				disabled.AccessPolicy.Disabled = true
				off, err := handler.PatchAccessPolicy(active, enabled, disabled)
				if err != nil || !reflect.DeepEqual(rootOf(t, off), rootOf(t, original)) {
					t.Fatalf("disabling did not restore baseline, including sniffing and fallback: %v", err)
				}
				generatedOff, err := p.RenderServer(disabled)
				if err != nil || !reflect.DeepEqual(rootOf(t, generatedOff), rootOf(t, original)) {
					t.Fatalf("regeneration reactivated disabled policy: %v", err)
				}
				baseClient, err := p.RenderClient(n)
				if err != nil {
					t.Fatal(err)
				}
				offClient, err := p.RenderClient(disabled)
				if err != nil || !reflect.DeepEqual(rootOf(t, offClient), rootOf(t, baseClient)) {
					t.Fatalf("disabled policy still adds client whitelist exceptions: %v", err)
				}
				reenabled, err := handler.PatchAccessPolicy(off, disabled, enabled)
				if err != nil || !reflect.DeepEqual(rootOf(t, reenabled), rootOf(t, active)) {
					t.Fatalf("reenabling did not restore saved policy and relay routing: %v", err)
				}
			})
		}
	}
}
