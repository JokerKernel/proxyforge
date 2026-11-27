package app

import (
	"bytes"
	"context"
	"os"
	"reflect"
	"strings"
	"testing"

	"proxyforge/internal/domain"
	"proxyforge/internal/provider"
	"proxyforge/internal/system"
)

func TestRelayAccessPolicyIsolationLifecycleAndRollback(t *testing.T) {
	for _, core := range []string{domain.CoreXray, domain.CoreSingBox} {
		t.Run(core, func(t *testing.T) {
			runner := &fakeRunner{port: freePort(t)}
			a, _ := testApp(t, runner)
			ctx := context.Background()
			opts := domain.GenerateOptions{Server: "server.example.com", Port: runner.port, SNI: "example.com", StandardConfig: true}
			n, err := a.Generate(ctx, core, opts)
			if err != nil {
				t.Fatal(err)
			}
			peer := domain.LandingPeer{Core: core, Server: "exit.example.com", Port: 443, SNI: n.SNI, UUID: n.UUID, PublicKey: n.PublicKey, ShortID: n.ShortID, Flow: domain.VisionFlow}
			for _, name := range []string{"first", "second"} {
				link, err := a.AddRelayLink(ctx, core, name, peer, RelayAddOptions{})
				if err != nil || link.AccessPolicy == nil || !link.AccessPolicy.Disabled {
					t.Fatalf("new relay must have its own disabled policy: %+v, %v", link, err)
				}
			}
			first := domain.AccessPolicy{Mode: domain.AccessModeWhitelist, Whitelist: []domain.AccessEntry{{Type: "ip", Value: "192.168.1.10", Ports: "443"}}}
			if err := a.SetRelayAccessPolicy(ctx, core, "first", first); err != nil {
				t.Fatal(err)
			}
			second, err := a.RelayAccessPolicy(core, "second")
			if err != nil || !second.Disabled {
				t.Fatalf("first policy changed the second: %+v, %v", second, err)
			}
			nodePolicy := domain.AccessPolicy{Mode: domain.AccessModeWhitelist, Whitelist: []domain.AccessEntry{{Type: "domain", Value: "node.test", Ports: "80"}}}
			if err := a.SetAccessPolicy(ctx, core, nodePolicy); err != nil {
				t.Fatal(err)
			}
			got, err := a.RelayAccessPolicy(core, "first")
			if err != nil || !reflect.DeepEqual(got, first) {
				t.Fatalf("node policy changed relay policy: %+v, %v", got, err)
			}
			client, err := a.RelayClientConfig(ctx, core, "first", ClientFormatNative, "", false)
			if err != nil || !bytes.Contains(client, []byte("192.168.1.10")) || bytes.Contains(client, []byte("node.test")) {
				t.Fatalf("relay client used the node policy: %s, %v", client, err)
			}
			client, err = a.RelayClientConfig(ctx, core, "second", ClientFormatNative, "", false)
			if err != nil || bytes.Contains(client, []byte("node.test")) || bytes.Contains(client, []byte("192.168.1.10")) {
				t.Fatalf("disabled relay inherited another whitelist: %s, %v", client, err)
			}
			// State, independent lists and config all roll back on an apply failure.
			p, _ := a.Registry.Get(core)
			paths := a.accessTransactionFiles(p, core)
			before := make([][]byte, len(paths))
			for i, path := range paths {
				before[i], err = os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
			}
			runner.failRestart = true
			changed := first.Clone()
			changed.Whitelist[0].Ports = "553"
			if err := a.SetRelayAccessPolicy(ctx, core, "first", changed, first); err == nil {
				t.Fatal("injected restart failure was ignored")
			}
			for i, path := range paths {
				after, err := os.ReadFile(path)
				if err != nil || !bytes.Equal(after, before[i]) {
					t.Fatalf("rollback changed %s: %v", path, err)
				}
			}
			if err := a.SetRelayAccessPolicy(ctx, core, "first", changed, first); err != nil {
				t.Fatal(err)
			}
			if err := a.SetRelayAccessPolicy(ctx, core, "first", first, first); err == nil || !strings.Contains(err.Error(), "其他操作修改") {
				t.Fatalf("stale confirmation overwrote policy: %v", err)
			}
			for _, invalid := range []struct {
				name string
				p    domain.AccessPolicy
			}{{"missing", first}, {"first", domain.AccessPolicy{Whitelist: []domain.AccessEntry{{Type: "ip", Value: "192.168.1.10", Ports: "65536"}}}}} {
				if err := a.SetRelayAccessPolicy(ctx, core, invalid.name, invalid.p); err == nil {
					t.Fatalf("accepted invalid update: %+v", invalid)
				}
			}
			// Settings, credential changes and regeneration retain each policy.
			if _, err := a.SetDNSProfile(ctx, core, provider.DNSProfilePublicGoogle); err != nil {
				t.Fatal(err)
			}
			if _, err := a.SetOutboundIPStrategy(ctx, core, provider.OutboundIPPreferIPv4); err != nil {
				t.Fatal(err)
			}
			if _, err := a.RotateRelayLink(ctx, core, "first"); err != nil {
				t.Fatal(err)
			}
			if err := a.SetRelayLinkEnabled(ctx, core, "first", false); err != nil {
				t.Fatal(err)
			}
			if err := a.SetRelayLinkEnabled(ctx, core, "first", true); err != nil {
				t.Fatal(err)
			}
			n, err = a.Generate(ctx, core, opts)
			if err != nil || !reflect.DeepEqual(*n.RelayLinks[0].AccessPolicy, changed) || !reflect.DeepEqual(*n.RelayLinks[1].AccessPolicy, second) {
				t.Fatalf("lifecycle lost relay policies: %+v, %v", n.RelayLinks, err)
			}
			// Removing the last active relay policy restores the shared sniffing.
			if err := a.SetAccessPolicy(ctx, core, domain.AccessPolicy{}); err != nil {
				t.Fatal(err)
			}
			if err := a.RemoveRelayLink(ctx, core, "first"); err != nil {
				t.Fatal(err)
			}
			n, err = a.Store.Load(core)
			if err != nil || n.HasActiveAccessPolicy() || len(n.AccessPolicySniffing) > 0 {
				t.Fatalf("removed relay left active policy: %+v, %v", n, err)
			}
			config, _ := os.ReadFile(a.Layout.Resolve(p.ConfigPath()))
			base, err := p.RenderServer(n)
			if err != nil || system.SHA256(config) != system.SHA256(base) {
				// DNS/IP settings differ after regeneration only if state was lost.
				t.Fatalf("removing last relay did not restore base config: %v", err)
			}
		})
	}
}

func TestNodePolicyUpdateFreezesLegacyRelayPolicy(t *testing.T) {
	for _, core := range []string{domain.CoreXray, domain.CoreSingBox} {
		t.Run(core, func(t *testing.T) {
			runner := &fakeRunner{port: freePort(t)}
			a, _ := testApp(t, runner)
			ctx := context.Background()
			n, err := a.Generate(ctx, core, domain.GenerateOptions{Server: "server.example.com", Port: runner.port, SNI: "example.com", StandardConfig: true})
			if err != nil {
				t.Fatal(err)
			}
			legacy := domain.AccessPolicy{Whitelist: []domain.AccessEntry{{Type: "domain", Value: "legacy.test", IncludeSubdomains: true}}}
			n.SchemaVersion, n.AccessPolicy = 7, legacy
			n.RelayLinks = []domain.RelayLink{{Name: "legacy", UserName: "legacy", UUID: "223e4567-e89b-42d3-a456-426614174000", Enabled: true, Upstream: domain.LandingPeer{Server: "exit.example.com", Port: 443}}}
			p, _ := a.Registry.Get(core)
			config, err := p.RenderServer(n)
			if err != nil {
				t.Fatal(err)
			}
			n.ConfigSHA256 = system.SHA256(config)
			if err := system.AtomicWrite(a.Layout.Resolve(p.ConfigPath()), config, 0600); err != nil {
				t.Fatal(err)
			}
			if err := a.Store.Save(n); err != nil {
				t.Fatal(err)
			}
			if err := a.SetAccessPolicy(ctx, core, domain.AccessPolicy{Mode: domain.AccessModeWhitelist}); err != nil {
				t.Fatal(err)
			}
			got, err := a.RelayAccessPolicy(core, "legacy")
			if err != nil || !reflect.DeepEqual(got, legacy) || got.Whitelist[0].Ports != "" {
				t.Fatalf("node policy changed legacy relay mode or all-port scope: %+v, %v", got, err)
			}
			data, err := os.ReadFile(a.Layout.RelayPath(core))
			if err != nil || !bytes.Contains(data, []byte(`"access_policy"`)) {
				t.Fatalf("migration was not committed with the policy update: %s, %v", data, err)
			}
			n, err = a.Store.Load(core)
			if err != nil || n.SchemaVersion != domain.StateSchemaVersion {
				t.Fatalf("migrated state version=%d, %v", n.SchemaVersion, err)
			}
		})
	}
}
