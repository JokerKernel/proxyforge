package system

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"proxyforge/internal/domain"
)

func TestLinkRecordsStoredSeparatelyAndIsolatedByCore(t *testing.T) {
	store := StateStore{Layout: Layout{Root: t.TempDir()}}
	nodes := map[string]domain.NodeSpec{}
	for _, core := range []string{domain.CoreXray, domain.CoreSingBox} {
		n := domain.NodeSpec{
			ManagedBy: "proxyforge", Core: core, SchemaVersion: domain.StateSchemaVersion,
			AccessPolicy: domain.AccessPolicy{Mode: domain.AccessModeDefault},
			LandingAccesses: []domain.LandingAccess{{
				Name: core + "-landing", UserName: "landing", UUID: "landing-uuid", Security: domain.LandingSecurityTLS,
				Port: 33443, SNI: "landing.invalid", CertificateFile: "/var/lib/proxyforge/tls/cert.pem", KeyFile: "/var/lib/proxyforge/tls/key.pem",
				CertificateSHA256: "certificate-pin", CertificatePublicKeySHA256: "public-key-pin", Enabled: true, UpdatedAt: time.Unix(100, 0).UTC(),
			}},
			RelayLinks: []domain.RelayLink{{
				AccessPolicy: &domain.AccessPolicy{Disabled: true},
				Name:         core + "-relay", UserName: "relay", UUID: "relay-uuid", Enabled: false, UpdatedAt: time.Unix(200, 0).UTC(),
				Upstream: domain.LandingPeer{Name: "remote", Core: domain.CoreXray, Security: domain.LandingSecurityReality,
					Server: "exit.example.com", Port: 443, SNI: "example.com", UUID: "remote-uuid", PublicKey: "remote-public-key", ShortID: "1234567890abcdef", Flow: domain.VisionFlow},
			}},
		}
		if err := store.Save(n); err != nil {
			t.Fatal(err)
		}
		nodes[core] = n
		b, err := os.ReadFile(store.Layout.StatePath(core))
		if err != nil {
			t.Fatal(err)
		}
		var state map[string]any
		if err := json.Unmarshal(b, &state); err != nil {
			t.Fatal(err)
		}
		for _, field := range []string{"landing_accesses", "relay_links"} {
			if _, exists := state[field]; exists {
				t.Fatalf("node state still contains %s", field)
			}
		}
		for _, path := range []string{store.Layout.LandingPath(core), store.Layout.RelayPath(core)} {
			info, err := os.Stat(path)
			if err != nil || info.Mode().Perm() != 0600 {
				t.Fatalf("link file permissions: info=%v err=%v", info, err)
			}
			info, err = os.Stat(filepath.Dir(path))
			if err != nil || info.Mode().Perm() != 0700 {
				t.Fatalf("link directory permissions: info=%v err=%v", info, err)
			}
			b, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var records []map[string]any
			if err := json.Unmarshal(b, &records); err != nil || len(records) != 1 {
				t.Fatalf("standalone records: %s err=%v", b, err)
			}
		}
	}
	for core, want := range nodes {
		got, err := store.Load(core)
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("core=%s records changed on round trip: got=%#v want=%#v err=%v", core, got, want, err)
		}
	}
	if err := store.Delete(domain.CoreXray); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{store.Layout.LandingPath(domain.CoreXray), store.Layout.RelayPath(domain.CoreXray)} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("deleted node retained link records: %s err=%v", path, err)
		}
	}
	got, err := store.Load(domain.CoreSingBox)
	if err != nil || !reflect.DeepEqual(got, nodes[domain.CoreSingBox]) {
		t.Fatal("deleting Xray affected sing-box link records")
	}
}

func TestStateDoesNotReadOrMigrateInlineLinks(t *testing.T) {
	store := StateStore{Layout: Layout{Root: t.TempDir()}}
	legacy := []byte(`{"managed_by":"proxyforge","core":"xray","schema_version":6,"landing_accesses":[{"name":"old-landing"}],"relay_links":[{"name":"old-relay"}]}`)
	if err := AtomicWrite(store.Layout.StatePath(domain.CoreXray), legacy, 0600); err != nil {
		t.Fatal(err)
	}
	n, err := store.Load(domain.CoreXray)
	if err != nil || len(n.LandingAccesses) != 0 || len(n.RelayLinks) != 0 {
		t.Fatalf("inline records were unexpectedly read: %#v err=%v", n, err)
	}
	for _, path := range []string{store.Layout.LandingPath(domain.CoreXray), store.Layout.RelayPath(domain.CoreXray)} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("inline records were unexpectedly migrated: %v", err)
		}
	}
	storeLinks := LinkStore{Layout: store.Layout}
	landings := []domain.LandingAccess{{Name: "new-landing"}}
	relays := []domain.RelayLink{{Name: "new-relay", AccessPolicy: &domain.AccessPolicy{Disabled: true}}}
	if err := storeLinks.Save(domain.CoreXray, landings, relays); err != nil {
		t.Fatal(err)
	}
	n, err = store.Load(domain.CoreXray)
	if err != nil || !reflect.DeepEqual(n.LandingAccesses, landings) || !reflect.DeepEqual(n.RelayLinks, relays) {
		t.Fatalf("standalone records were not loaded: %#v err=%v", n, err)
	}
}

func TestLinkStoreRejectsInvalidFiles(t *testing.T) {
	for _, kind := range []string{"landing", "relay"} {
		t.Run(kind, func(t *testing.T) {
			store := StateStore{Layout: Layout{Root: t.TempDir()}}
			if err := store.Save(domain.NodeSpec{ManagedBy: "proxyforge", Core: domain.CoreXray}); err != nil {
				t.Fatal(err)
			}
			path := store.Layout.LandingPath(domain.CoreXray)
			if kind == "relay" {
				path = store.Layout.RelayPath(domain.CoreXray)
			}
			for _, content := range []string{`null`, `{}`, `[null]`, `[`, `[] []`, `[{"unknown":true}]`, `[{"enabled":"yes"}]`} {
				if err := AtomicWrite(path, []byte(content), 0600); err != nil {
					t.Fatal(err)
				}
				if _, err := store.Load(domain.CoreXray); err == nil {
					t.Fatalf("accepted invalid %s config: %s", kind, content)
				}
			}
			if err := AtomicWrite(path, []byte(`[]`), 0644); err != nil {
				t.Fatal(err)
			}
			if _, err := store.Load(domain.CoreXray); err == nil {
				t.Fatal("accepted unsafe link file permissions")
			}
		})
	}
}
