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
			if err := a.AddAccessEntry(ctx, core, "white", "192.168.1.10", false); err != nil {
				t.Fatal(err)
			}
			if err := a.AddAccessEntry(ctx, core, "white", "Allowed.COM.", false); err != nil {
				t.Fatal(err)
			}
			if err := a.AddAccessEntry(ctx, core, "white", "allowed.com", false); err != nil {
				t.Fatal(err)
			}
			if err := a.AddAccessEntry(ctx, core, "black", "denied.com", false); err != nil {
				t.Fatal(err)
			}
			if err := a.SetAccessMode(ctx, core, domain.AccessModeBlacklist); err != nil {
				t.Fatal(err)
			}
			policy, err := a.AccessPolicy(core)
			if err != nil || len(policy.Whitelist) != 2 || len(policy.Blacklist) != 1 {
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
			if err := a.SetAccessMode(ctx, core, domain.AccessModeWhitelist); err != nil {
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
			if err := a.AddAccessEntry(ctx, core, "white", "192.168.1.0/24", false); err != nil {
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
			if err := a.SetAccessMode(ctx, core, domain.AccessModeWhitelist); err == nil {
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
			if err := a.RemoveAccessEntry(ctx, core, "white", "192.168.1.0/24", false); err != nil {
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
			for _, mode := range []string{"whitelist", "blacklist", "default"} {
				if err := a.SetAccessMode(ctx, core, mode); err != nil {
					t.Fatal(err)
				}
			}
			if err := a.AddAccessEntry(ctx, core, "white", "https://example.com", false); err == nil {
				t.Fatal("accepted URL")
			}
			if err := a.SetAccessMode(ctx, core, "other"); err == nil {
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
	if err := a.AddAccessEntry(ctx, domain.CoreXray, "white", "allowed.com", false); err != nil {
		t.Fatal(err)
	}
	if err := a.RemoveAccessEntry(ctx, domain.CoreXray, "white", "allowed.com", false); err != nil {
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
