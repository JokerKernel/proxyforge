package system

import (
	"os"
	"reflect"
	"testing"

	"proxyforge/internal/domain"
)

func TestLegacyRelayPoliciesAreIndependentSnapshots(t *testing.T) {
	for _, core := range []string{domain.CoreXray, domain.CoreSingBox} {
		t.Run(core, func(t *testing.T) {
			store := StateStore{Layout: Layout{Root: t.TempDir()}}
			shared := domain.AccessPolicy{Mode: domain.AccessModeWhitelist, Whitelist: []domain.AccessEntry{{Type: "domain", Value: "allowed.test", Ports: "80,443"}}}
			n := domain.NodeSpec{ManagedBy: "proxyforge", Core: core, AccessPolicy: shared,
				RelayLinks: []domain.RelayLink{{Name: "first"}, {Name: "second"}, {Name: "explicit", AccessPolicy: &domain.AccessPolicy{Disabled: true}}}}
			if err := store.Save(n); err != nil {
				t.Fatal(err)
			}
			before, _ := os.ReadFile(store.Layout.RelayPath(core))
			n, err := store.Load(core)
			if err != nil {
				t.Fatal(err)
			}
			for _, link := range n.RelayLinks[:2] {
				if link.AccessPolicy == nil || !reflect.DeepEqual(*link.AccessPolicy, shared) {
					t.Fatalf("legacy policy was not copied: %+v", link)
				}
			}
			if !n.RelayLinks[2].AccessPolicy.Disabled {
				t.Fatal("explicit disabled policy was overwritten")
			}
			after, _ := os.ReadFile(store.Layout.RelayPath(core))
			if string(before) != string(after) {
				t.Fatal("reading state wrote migration outside a transaction")
			}
			n.AccessPolicy.Whitelist[0].Value = "node.test"
			n.RelayLinks[0].AccessPolicy.Whitelist[0].Ports = "553"
			if !reflect.DeepEqual(*n.RelayLinks[1].AccessPolicy, shared) {
				t.Fatal("changing one policy changed another policy")
			}
			if err := store.Save(n); err != nil {
				t.Fatal(err)
			}
			reloaded, err := store.Load(core)
			if err != nil || reloaded.RelayLinks[0].AccessPolicy.Whitelist[0].Ports != "553" || !reflect.DeepEqual(*reloaded.RelayLinks[1].AccessPolicy, shared) {
				t.Fatalf("independent policies did not survive saving: %+v, %v", reloaded.RelayLinks, err)
			}
		})
	}
}
