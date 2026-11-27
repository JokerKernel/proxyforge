package app

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"

	"proxyforge/internal/domain"
	"proxyforge/internal/provider"
	"proxyforge/internal/provider/accesspolicy"
	"proxyforge/internal/system"
)

func TestDefaultBlacklistOnlyAppliesAndRestoresConfig(t *testing.T) {
	for _, core := range []string{domain.CoreXray, domain.CoreSingBox} {
		t.Run(core, func(t *testing.T) {
			runner := &fakeRunner{port: freePort(t)}
			a, _ := testApp(t, runner)
			ctx := context.Background()
			if _, err := a.Generate(ctx, core, domain.GenerateOptions{Server: "server.example.com", Port: runner.port, SNI: "example.com", StandardConfig: true}); err != nil {
				t.Fatal(err)
			}
			p, _ := a.Registry.Get(core)
			path := a.Layout.Resolve(p.ConfigPath())
			original, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if err := a.SetAccessPolicy(ctx, core, domain.AccessPolicy{
				Blacklist: []domain.AccessEntry{{Type: domain.AccessEntryDomain, Value: "denied.com", IncludeSubdomains: true}},
			}); err != nil {
				t.Fatal(err)
			}
			current, err := a.Store.Load(core)
			if err != nil || current.AccessPolicy.EffectiveMode() != domain.AccessModeDefault || len(current.AccessPolicy.Blacklist) != 1 {
				t.Fatalf("default blacklist not persisted: %#v, err=%v", current.AccessPolicy, err)
			}
			config, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			tag := "black-domain"
			if core == domain.CoreSingBox {
				tag = "access-black"
			}
			if !bytes.Contains(config, []byte(tag)) || current.ConfigSHA256 != system.SHA256(config) {
				t.Fatal("adding a blacklist in default mode did not update managed config")
			}
			if core == domain.CoreXray && len(current.AccessPolicySniffing) == 0 {
				t.Fatal("default blacklist activation did not capture original sniffing")
			}
			if err := a.SetAccessPolicy(ctx, core, domain.AccessPolicy{}); err != nil {
				t.Fatal(err)
			}
			config, err = os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var before, after any
			if err := json.Unmarshal(original, &before); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(config, &after); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(before, after) {
				t.Fatal("removing last blacklist entry did not restore original config")
			}
		})
	}
}

func TestAccessPolicyLifecyclePersistsAcrossNodeAndLinkChanges(t *testing.T) {
	for _, core := range []string{domain.CoreXray, domain.CoreSingBox} {
		t.Run(core, func(t *testing.T) {
			runner := &fakeRunner{port: freePort(t)}
			a, _ := testApp(t, runner)
			ctx := context.Background()
			opts := domain.GenerateOptions{Server: "server.example.com", Port: runner.port, SNI: "example.com", StandardConfig: true}
			first, err := a.Generate(ctx, core, opts)
			if err != nil {
				t.Fatal(err)
			}
			if err := a.SetAccessPolicy(ctx, core, domain.AccessPolicy{
				Mode: domain.AccessModeDefault,
				Whitelist: []domain.AccessEntry{
					{Type: domain.AccessEntryIP, Value: "192.168.1.10"},
					{Type: domain.AccessEntryDomain, Value: "Allowed.COM.", IncludeSubdomains: true},
					{Type: domain.AccessEntryDomain, Value: "allowed.com", IncludeSubdomains: true},
				},
				Blacklist: []domain.AccessEntry{{Type: domain.AccessEntryDomain, Value: "denied.com", IncludeSubdomains: true}},
			}); err != nil {
				t.Fatal(err)
			}
			policy, err := a.AccessPolicy(core)
			if err != nil || policy.EffectiveMode() != domain.AccessModeDefault || len(policy.Whitelist) != 2 || len(policy.Blacklist) != 1 {
				t.Fatalf("policy=%#v err=%v", policy, err)
			}
			// Editing another setting keeps generated white-list exceptions in sync.
			if _, err := a.SetOutboundIPStrategy(ctx, core, provider.OutboundIPPreferIPv4); err != nil {
				t.Fatal(err)
			}
			if _, err := a.SetDNSProfile(ctx, core, provider.DNSProfilePublicGoogle); err != nil {
				t.Fatal(err)
			}
			if _, err := a.ResetCredentials(ctx, core, domain.ResetOptions{Credentials: true}); err != nil {
				t.Fatal(err)
			}
			current, err := a.Store.Load(core)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(current.AccessPolicy, policy) {
				t.Fatal("credential reset lost policy")
			}
			regenerated, err := a.Generate(ctx, core, opts)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(regenerated.AccessPolicy, policy) || regenerated.UUID == first.UUID {
				t.Fatal("regeneration lost policy/rotated identity incorrectly")
			}
			landing, err := a.AddLandingAccessWithOptions(ctx, core, "independent", LandingAddOptions{Security: "tls"})
			if err != nil {
				t.Fatal(err)
			}
			bundle, err := a.LandingBundle(core, landing.Name)
			if err != nil {
				t.Fatal(err)
			}
			peer := bundle.Peer
			peer.Server = "exit.example.com"
			if _, err := a.AddRelayLink(ctx, core, "relay", peer, RelayAddOptions{}); err != nil {
				t.Fatal(err)
			}
			policy.Mode = domain.AccessModeWhitelist
			if err := a.SetAccessPolicy(ctx, core, policy); err != nil {
				t.Fatal(err)
			}
			p, _ := a.Registry.Get(core)
			path := a.Layout.Resolve(p.ConfigPath())
			config, _ := os.ReadFile(path)
			if !bytes.Contains(config, []byte("independent-in")) || !bytes.Contains(config, []byte("relay-out")) {
				t.Fatal("policy omitted independent landing or relay route")
			}
			if _, err := a.RotateRelayLink(ctx, core, "relay"); err != nil {
				t.Fatal(err)
			}
			if err := a.SetRelayLinkEnabled(ctx, core, "relay", false); err != nil {
				t.Fatal(err)
			}
			if err := a.SetRelayLinkEnabled(ctx, core, "relay", true); err != nil {
				t.Fatal(err)
			}
			if err := a.RemoveLandingAccess(ctx, core, landing.Name); err != nil {
				t.Fatal(err)
			}
			state, err := a.Store.Load(core)
			if err != nil {
				t.Fatal(err)
			}
			config, _ = os.ReadFile(path)
			if bytes.Contains(config, []byte("independent-in")) {
				t.Fatal("removed landing retained policy scope")
			}
			if state.ConfigSHA256 != system.SHA256(config) || state.SchemaVersion != domain.StateSchemaVersion {
				t.Fatal("state does not describe active policy")
			}
		})
	}
}

func TestAccessPolicyRollbackAndManualConfigPreservation(t *testing.T) {
	for _, core := range []string{domain.CoreXray, domain.CoreSingBox} {
		t.Run(core, func(t *testing.T) {
			runner := &fakeRunner{port: freePort(t)}
			a, _ := testApp(t, runner)
			ctx := context.Background()
			_, err := a.Generate(ctx, core, domain.GenerateOptions{Server: "server.example.com", Port: runner.port, SNI: "example.com", StandardConfig: true})
			if err != nil {
				t.Fatal(err)
			}
			p, _ := a.Registry.Get(core)
			path := a.Layout.Resolve(p.ConfigPath())
			config, _ := os.ReadFile(path)
			var root map[string]any
			if err := json.Unmarshal(config, &root); err != nil {
				t.Fatal(err)
			}
			root["manual"] = map[string]any{"keep": true}
			manual, _ := json.Marshal(root)
			if err := system.AtomicWrite(path, manual, 0600); err != nil {
				t.Fatal(err)
			}
			if err := a.SetAccessPolicy(ctx, core, domain.AccessPolicy{
				Whitelist: []domain.AccessEntry{{Type: domain.AccessEntryCIDR, Value: "192.168.1.0/24"}},
			}); err != nil {
				t.Fatal(err)
			}
			old, err := a.Store.Load(core)
			if err != nil {
				t.Fatal(err)
			}
			before, _ := os.ReadFile(path)
			beforePolicy, err := os.ReadFile(a.Layout.AccessPolicyPath(core))
			if err != nil {
				t.Fatal(err)
			}
			runner.failRestart = true
			next := old.AccessPolicy
			next.Mode = domain.AccessModeWhitelist
			if err := a.SetAccessPolicy(ctx, core, next); err == nil {
				t.Fatal("expected injected restart failure")
			}
			afterState, err := a.Store.Load(core)
			if err != nil {
				t.Fatal(err)
			}
			after, _ := os.ReadFile(path)
			afterPolicy, err := os.ReadFile(a.Layout.AccessPolicyPath(core))
			if err != nil || !bytes.Equal(beforePolicy, afterPolicy) || !bytes.Equal(before, after) || !reflect.DeepEqual(old, afterState) {
				t.Fatal("policy failure changed prior config/state")
			}
			if err := a.SetAccessPolicy(ctx, core, domain.AccessPolicy{}); err != nil {
				t.Fatal(err)
			}
			after, _ = os.ReadFile(path)
			if !bytes.Contains(after, []byte(`"keep": true`)) {
				t.Fatal("manual settings lost")
			}
			var clean map[string]any
			if err := json.Unmarshal(after, &clean); err != nil {
				t.Fatal(err)
			}
			if accesspolicy.HasComponents(clean, core) {
				t.Fatal("disabling empty policy left generated components")
			}
			for _, mode := range []string{"whitelist", "default"} {
				if err := a.SetAccessPolicy(ctx, core, domain.AccessPolicy{Mode: mode}); err != nil {
					t.Fatal(err)
				}
			}
			if err := a.SetAccessPolicy(ctx, core, domain.AccessPolicy{
				Whitelist: []domain.AccessEntry{{Type: domain.AccessEntryDomain, Value: "https://example.com", IncludeSubdomains: true}},
			}); err == nil {
				t.Fatal("accepted URL")
			}
			if err := a.SetAccessPolicy(ctx, core, domain.AccessPolicy{Mode: "other"}); err == nil {
				t.Fatal("accepted unknown mode")
			}
		})
	}
}

func TestGenerateReadsStandaloneAccessPolicyBeforeFirstNode(t *testing.T) {
	for _, core := range []string{domain.CoreXray, domain.CoreSingBox} {
		t.Run(core, func(t *testing.T) {
			runner := &fakeRunner{port: freePort(t)}
			a, _ := testApp(t, runner)
			policy := domain.AccessPolicy{Mode: domain.AccessModeWhitelist, Whitelist: []domain.AccessEntry{
				{Type: domain.AccessEntryDomain, Value: "example.com", IncludeSubdomains: true},
			}}
			store := system.AccessPolicyStore{Layout: a.Layout}
			if err := store.Save(core, policy); err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(a.Layout.AccessPolicyPath(core))
			if err != nil {
				t.Fatal(err)
			}
			opts := domain.GenerateOptions{Server: "server.example.com", Port: runner.port, SNI: "example.com", StandardConfig: true}
			runner.failRestart = true
			if _, err := a.Generate(context.Background(), core, opts); err == nil {
				t.Fatal("expected injected service restart failure")
			}
			after, err := os.ReadFile(a.Layout.AccessPolicyPath(core))
			if err != nil || !bytes.Equal(before, after) {
				t.Fatal("failed first generation changed the preconfigured access policy")
			}
			runner.failRestart = false
			n, err := a.Generate(context.Background(), core, opts)
			if err != nil || !reflect.DeepEqual(n.AccessPolicy, policy) {
				t.Fatalf("first node did not use standalone policy: %#v err=%v", n.AccessPolicy, err)
			}
			p, _ := a.Registry.Get(core)
			config, err := os.ReadFile(a.Layout.Resolve(p.ConfigPath()))
			if err != nil || !bytes.Contains(config, []byte("example.com")) || !bytes.Contains(config, []byte("access-")) {
				t.Fatalf("first generation omitted access policy: %s err=%v", config, err)
			}
		})
	}
}

func TestAccessPolicyRestoresOriginalXraySniffing(t *testing.T) {
	runner := &fakeRunner{port: freePort(t)}
	a, _ := testApp(t, runner)
	ctx := context.Background()
	_, err := a.Generate(ctx, domain.CoreXray, domain.GenerateOptions{Server: "server.example.com", Port: runner.port, SNI: "example.com"})
	if err != nil {
		t.Fatal(err)
	}
	p, _ := a.Registry.Get(domain.CoreXray)
	path := a.Layout.Resolve(p.ConfigPath())
	before, _ := os.ReadFile(path)
	if !strings.Contains(string(before), `"routeOnly": true`) {
		t.Fatal("fixture did not enable sniffing")
	}
	if err := a.SetAccessPolicy(ctx, domain.CoreXray, domain.AccessPolicy{
		Whitelist: []domain.AccessEntry{{Type: domain.AccessEntryDomain, Value: "allowed.com", IncludeSubdomains: true}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := a.SetAccessPolicy(ctx, domain.CoreXray, domain.AccessPolicy{}); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(path)
	var original, restored map[string]any
	if err := json.Unmarshal(before, &original); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(after, &restored); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(original, restored) {
		t.Fatalf("original sniffing not restored\nbefore=%s\nafter=%s", before, after)
	}
}
