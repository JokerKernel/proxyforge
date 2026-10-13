package cli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"

	"proxyforge/internal/domain"
	"proxyforge/internal/provider/accesspolicy"
)

func switchPolicy() domain.AccessPolicy {
	return domain.AccessPolicy{Mode: domain.AccessModeWhitelist,
		Whitelist: []domain.AccessEntry{{Type: domain.AccessEntryDomain, Value: "allowed.com", IncludeSubdomains: true}},
		Blacklist: []domain.AccessEntry{{Type: domain.AccessEntryDomain, Value: "denied.com", IncludeSubdomains: true}},
	}
}

func TestAccessMenuSwitchPreservesSettingsAndAppliesSavedRules(t *testing.T) {
	for _, core := range []string{domain.CoreXray, domain.CoreSingBox} {
		t.Run(core, func(t *testing.T) {
			c, out := accessCLI(t, core)
			ctx := context.Background()
			policy := switchPolicy()
			if err := c.app.SetAccessPolicy(ctx, core, policy); err != nil {
				t.Fatal(err)
			}
			p, _ := c.app.Registry.Get(core)
			path := c.app.Layout.Resolve(p.ConfigPath())
			for _, step := range []struct {
				input    string
				disabled bool
				mode     string
			}{
				{"4\n1\n0\n", true, domain.AccessModeWhitelist},
				{"1\n1\n1\n0\n0\n", true, domain.AccessModeDefault},
				{"4\n1\n0\n", false, domain.AccessModeDefault},
			} {
				out.Reset()
				c.reader = bufio.NewReader(strings.NewReader(step.input))
				if err := c.accessPolicyMenu(ctx, core); err != nil {
					t.Fatal(err)
				}
				n, err := c.app.Store.Load(core)
				want := policy
				want.Disabled, want.Mode = step.disabled, step.mode
				if err != nil || !reflect.DeepEqual(n.AccessPolicy, want) {
					t.Fatalf("switch changed saved lists or ignored mode/switch: %#v, err=%v", n.AccessPolicy, err)
				}
				config, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				var root map[string]any
				if err := json.Unmarshal(config, &root); err != nil {
					t.Fatal(err)
				}
				if accesspolicy.HasComponents(root, core) == step.disabled {
					t.Fatal("applied rules do not reflect independent switch")
				}
				if step.disabled && !strings.Contains(out.String(), "原有拦截 → 其余放行") || strings.Contains(out.String(), "[错误]") {
					t.Fatalf("disabled rule display or switch failed: %s", out.String())
				}
			}
		})
	}
}

func TestAccessSwitchCommandsRequireConfirmation(t *testing.T) {
	for _, core := range []string{domain.CoreXray, domain.CoreSingBox} {
		t.Run(core, func(t *testing.T) {
			c, _ := accessCLI(t, core)
			policy := switchPolicy()
			if err := c.app.SetAccessPolicy(context.Background(), core, policy); err != nil {
				t.Fatal(err)
			}
			path := c.app.Layout.AccessPolicyPath(core)
			for _, action := range []string{"disable", "enable"} {
				before, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				c.yes = false
				cmd := c.accessCommand()
				cmd.SetArgs([]string{action, core})
				if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "--yes") {
					t.Fatalf("unconfirmed switch accepted: %v", err)
				}
				after, err := os.ReadFile(path)
				if err != nil || !bytes.Equal(before, after) {
					t.Fatal("unconfirmed switch changed policy")
				}
				c.yes = true
				cmd = c.accessCommand()
				cmd.SetArgs([]string{action, core})
				if err := cmd.Execute(); err != nil {
					t.Fatal(err)
				}
				want := policy
				want.Disabled = action == "disable"
				got, err := c.app.AccessPolicy(core)
				if err != nil || !reflect.DeepEqual(got, want) {
					t.Fatalf("switch lost saved policy: %#v, err=%v", got, err)
				}
			}
		})
	}
}
