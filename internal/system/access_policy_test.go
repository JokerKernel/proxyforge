package system

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"proxyforge/internal/domain"
)

func TestAccessPolicyPreservesModeSelectionAndLegacyPortScope(t *testing.T) {
	store := AccessPolicyStore{Layout: Layout{Root: t.TempDir()}}
	for _, mode := range []string{"", domain.AccessModeDefault, domain.AccessModeWhitelist} {
		policy := domain.AccessPolicy{Mode: mode, Whitelist: []domain.AccessEntry{
			{Type: "domain", Value: "legacy.example.com", IncludeSubdomains: true},
			{Type: "domain", Value: "new.example.com", Ports: "443.80"},
		}}
		if err := store.Save(domain.CoreXray, policy); err != nil {
			t.Fatal(err)
		}
		got, err := store.Load(domain.CoreXray)
		if err != nil || got.Mode != mode || got.Whitelist[0].Ports != "" || got.Whitelist[1].Ports != "80,443" {
			t.Fatalf("mode or port scope changed: %+v, %v", got, err)
		}
	}
	policy := domain.AccessPolicy{AutoWhitelist: true}
	if err := store.Save(domain.CoreXray, policy); err != nil {
		t.Fatal(err)
	}
	got, err := store.Load(domain.CoreXray)
	if err != nil || got.Mode != "" || !got.AutoWhitelist || got.EffectiveMode() != domain.AccessModeWhitelist || !got.Active() {
		t.Fatalf("standalone storage lost automatic restriction after clear: %+v, %v", got, err)
	}
}

func TestAccessPolicyStoredSeparatelyAndIsolatedByCore(t *testing.T) {
	store := StateStore{Layout: Layout{Root: t.TempDir()}}
	policies := map[string]domain.AccessPolicy{
		domain.CoreXray: {Mode: domain.AccessModeWhitelist, Whitelist: []domain.AccessEntry{
			{Type: domain.AccessEntryDomain, Value: "Example.COM", IncludeSubdomains: true},
			{Type: domain.AccessEntryCIDR, Value: "192.168.1.10/24"},
		}},
		domain.CoreSingBox: {Disabled: true, Mode: domain.AccessModeDefault, Blacklist: []domain.AccessEntry{
			{Type: domain.AccessEntryIP, Value: "192.168.1.1"},
		}},
	}
	for core, policy := range policies {
		n := domain.NodeSpec{ManagedBy: "proxyforge", Core: core, SchemaVersion: domain.StateSchemaVersion, AccessPolicy: policy}
		if err := store.Save(n); err != nil {
			t.Fatal(err)
		}
		b, err := os.ReadFile(store.Layout.StatePath(core))
		if err != nil {
			t.Fatal(err)
		}
		var state map[string]any
		if err := json.Unmarshal(b, &state); err != nil {
			t.Fatal(err)
		}
		if _, exists := state["access_policy"]; exists {
			t.Fatal("node state still contains the access policy")
		}
		path := store.Layout.AccessPolicyPath(core)
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != 0600 {
			t.Fatalf("access policy permissions: info=%v err=%v", info, err)
		}
		info, err = os.Stat(filepath.Dir(path))
		if err != nil || info.Mode().Perm() != 0700 {
			t.Fatalf("access directory permissions: info=%v err=%v", info, err)
		}
		b, err = os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var saved map[string]any
		if err := json.Unmarshal(b, &saved); err != nil {
			t.Fatal(err)
		}
		if len(saved) != 4 || saved["disabled"] != policy.Disabled || saved["mode"] != policy.Mode || saved["whitelist"] == nil || saved["blacklist"] == nil {
			t.Fatalf("standalone file is not a complete policy: %s", b)
		}
	}
	for core, policy := range policies {
		n, err := store.Load(core)
		if err != nil {
			t.Fatal(err)
		}
		want, err := policy.Normalize()
		if err != nil || !reflect.DeepEqual(n.AccessPolicy, want) {
			t.Fatalf("core=%s policy=%#v want=%#v err=%v", core, n.AccessPolicy, want, err)
		}
	}
	if err := store.Delete(domain.CoreXray); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(store.Layout.AccessPolicyPath(domain.CoreXray)); !os.IsNotExist(err) {
		t.Fatalf("deleted node retained its policy: %v", err)
	}
	if _, err := store.Load(domain.CoreSingBox); err != nil {
		t.Fatalf("deleting Xray affected sing-box: %v", err)
	}
}

func TestStateDoesNotReadOrMigrateInlineAccessPolicy(t *testing.T) {
	store := StateStore{Layout: Layout{Root: t.TempDir()}}
	path := store.Layout.StatePath(domain.CoreXray)
	legacy := []byte(`{"managed_by":"proxyforge","core":"xray","schema_version":5,"access_policy":{"mode":"whitelist","whitelist":[{"type":"domain","value":"example.com"}]}}`)
	if err := AtomicWrite(path, legacy, 0600); err != nil {
		t.Fatal(err)
	}
	n, err := store.Load(domain.CoreXray)
	if err != nil {
		t.Fatal(err)
	}
	if n.AccessPolicy.EffectiveMode() != domain.AccessModeDefault || len(n.AccessPolicy.Whitelist) != 0 {
		t.Fatal("inline policy was unexpectedly read")
	}
	if _, err := os.Stat(store.Layout.AccessPolicyPath(domain.CoreXray)); !os.IsNotExist(err) {
		t.Fatalf("inline policy was unexpectedly migrated: %v", err)
	}
	policy := domain.AccessPolicy{Mode: domain.AccessModeDefault}
	if err := (AccessPolicyStore{Layout: store.Layout}).Save(domain.CoreXray, policy); err != nil {
		t.Fatal(err)
	}
	n, err = store.Load(domain.CoreXray)
	if err != nil || !reflect.DeepEqual(n.AccessPolicy, policy) {
		t.Fatalf("standalone policy was not loaded: %#v %v", n.AccessPolicy, err)
	}
}

func TestAccessPolicyRejectsInvalidFiles(t *testing.T) {
	store := StateStore{Layout: Layout{Root: t.TempDir()}}
	if err := store.Save(domain.NodeSpec{ManagedBy: "proxyforge", Core: domain.CoreXray}); err != nil {
		t.Fatal(err)
	}
	path := store.Layout.AccessPolicyPath(domain.CoreXray)
	for _, content := range []string{
		`null`, `{`, `{"mode":"unknown"}`, `{"mode":"blacklist"}`, `{"mode":"default"} {}`, `{"disabled":"true"}`,
		`{"whitelist":[{"type":"cidr","value":"192.168.1.0/99"}]}`,
		`{"mode":"default","whitelst":[]}`,
	} {
		if err := AtomicWrite(path, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := store.Load(domain.CoreXray); err == nil {
			t.Fatalf("accepted invalid access policy: %s", content)
		}
	}
	if err := AtomicWrite(path, []byte(`{"mode":"default"}`), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(domain.CoreXray); err == nil {
		t.Fatal("accepted unsafe access policy permissions")
	}
}

func TestStateSaveFailureRestoresAccessPolicyFile(t *testing.T) {
	for _, hadPolicy := range []bool{false, true} {
		name := "missing"
		if hadPolicy {
			name = "existing"
		}
		t.Run(name, func(t *testing.T) {
			store := StateStore{Layout: Layout{Root: t.TempDir()}}
			policyPath := store.Layout.AccessPolicyPath(domain.CoreXray)
			before := []byte(`{"mode":"default", "whitelist":[], "blacklist":[]}`)
			if hadPolicy {
				if err := AtomicWrite(policyPath, before, 0600); err != nil {
					t.Fatal(err)
				}
			}
			// An invalid node state destination must leave policy files untouched.
			if err := os.MkdirAll(store.Layout.StatePath(domain.CoreXray), 0700); err != nil {
				t.Fatal(err)
			}
			n := domain.NodeSpec{ManagedBy: "proxyforge", Core: domain.CoreXray, AccessPolicy: domain.AccessPolicy{Mode: domain.AccessModeWhitelist}}
			if err := store.Save(n); err == nil {
				t.Fatal("expected node state write failure")
			}
			after, err := os.ReadFile(policyPath)
			if hadPolicy {
				if err != nil || !bytes.Equal(before, after) {
					t.Fatalf("failed save changed the policy file: %q err=%v", after, err)
				}
			} else if !os.IsNotExist(err) {
				t.Fatalf("failed save left a new policy file: %v", err)
			}
		})
	}
}
