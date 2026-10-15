package app

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"reflect"
	"testing"

	"proxyforge/internal/domain"
	"proxyforge/internal/provider/accesspolicy"
)

func TestAccessSwitchPersistsThroughEditsAndRegeneration(t *testing.T) {
	for _, core := range []string{domain.CoreXray, domain.CoreSingBox} {
		t.Run(core, func(t *testing.T) {
			runner := &fakeRunner{port: freePort(t)}
			a, _ := testApp(t, runner)
			ctx := context.Background()
			opts := domain.GenerateOptions{Server: "server.example.com", Port: runner.port, SNI: "example.com", StandardConfig: true}
			if _, err := a.Generate(ctx, core, opts); err != nil {
				t.Fatal(err)
			}
			p, _ := a.Registry.Get(core)
			path := a.Layout.Resolve(p.ConfigPath())
			readConfig := func() []byte {
				b, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				return b
			}
			assertBaseline := func(want []byte) {
				var gotRoot, wantRoot any
				if err := json.Unmarshal(readConfig(), &gotRoot); err != nil {
					t.Fatal(err)
				}
				if err := json.Unmarshal(want, &wantRoot); err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(gotRoot, wantRoot) {
					t.Fatal("disabled access control changed baseline rules or sniffing")
				}
			}
			baseline := readConfig()
			policy := domain.AccessPolicy{Mode: domain.AccessModeWhitelist,
				Whitelist: []domain.AccessEntry{{Type: domain.AccessEntryDomain, Value: "allowed.com", IncludeSubdomains: true}},
				Blacklist: []domain.AccessEntry{{Type: domain.AccessEntryDomain, Value: "denied.com", IncludeSubdomains: true}},
			}
			if err := a.SetAccessPolicy(ctx, core, policy); err != nil {
				t.Fatal(err)
			}
			// A failed disable must retain both the switch and its active rules.
			paths := []string{path, a.Layout.StatePath(core), a.Layout.AccessPolicyPath(core)}
			before := make([][]byte, len(paths))
			for i, path := range paths {
				b, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				before[i] = b
			}
			runner.failRestart = true
			policy.Disabled = true
			if err := a.SetAccessPolicy(ctx, core, policy); err == nil {
				t.Fatal("injected restart failure did not stop disable")
			}
			for i, path := range paths {
				b, err := os.ReadFile(path)
				if err != nil || !bytes.Equal(b, before[i]) {
					t.Fatalf("failed disable changed %s: %v", path, err)
				}
			}
			runner.failRestart = false
			if err := a.SetAccessPolicy(ctx, core, policy); err != nil {
				t.Fatal(err)
			}
			assertBaseline(baseline)
			got, err := a.AccessPolicy(core)
			if err != nil || !reflect.DeepEqual(got, policy) {
				t.Fatalf("disabled switch did not retain policy: %#v, err=%v", got, err)
			}
			policy.Mode = domain.AccessModeDefault
			policy.Whitelist = append(policy.Whitelist, domain.AccessEntry{Type: domain.AccessEntryCIDR, Value: "192.168.1.0/24"})
			if err := a.SetAccessPolicy(ctx, core, policy); err != nil {
				t.Fatal(err)
			}
			assertBaseline(baseline)
			regenerated, err := a.Generate(ctx, core, opts)
			if err != nil || !regenerated.AccessPolicy.Disabled || len(regenerated.AccessPolicy.Whitelist) != 2 || len(regenerated.AccessPolicy.Blacklist) != 1 {
				t.Fatalf("regeneration lost switch or saved lists: %#v, err=%v", regenerated.AccessPolicy, err)
			}
			var root map[string]any
			if err := json.Unmarshal(readConfig(), &root); err != nil {
				t.Fatal(err)
			}
			if accesspolicy.HasComponents(root, core) {
				t.Fatal("regeneration enabled a disabled policy")
			}
			baseline = readConfig()
			policy.Disabled = false
			if err := a.SetAccessPolicy(ctx, core, policy); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(readConfig(), &root); err != nil {
				t.Fatal(err)
			}
			if !accesspolicy.HasComponents(root, core) {
				t.Fatal("reenabling did not apply saved rules")
			}
			policy.Disabled = true
			if err := a.SetAccessPolicy(ctx, core, policy); err != nil {
				t.Fatal(err)
			}
			assertBaseline(baseline)
		})
	}
}
