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
	"proxyforge/internal/system"
)

func TestAccessClearPreservesOtherListAndUpdatesModeAndRules(t *testing.T) {
	for _, core := range []string{domain.CoreXray, domain.CoreSingBox} {
		for _, mode := range []string{"", domain.AccessModeDefault, domain.AccessModeWhitelist} {
			for _, list := range []string{"white", "black"} {
				for _, source := range []string{"menu", "command"} {
					t.Run(core+"/"+mode+"/"+list+"/"+source, func(t *testing.T) {
						c, out := accessCLI(t, core)
						before := domain.AccessPolicy{
							Mode: mode,
							Whitelist: []domain.AccessEntry{
								{Type: domain.AccessEntryDomain, Value: "allowed.com", IncludeSubdomains: true},
								{Type: domain.AccessEntryIP, Value: "192.168.1.1"},
							},
							Blacklist: []domain.AccessEntry{
								{Type: domain.AccessEntryDomain, Value: "denied.com", IncludeSubdomains: true},
								{Type: domain.AccessEntryCIDR, Value: "192.168.2.0/24"},
							},
						}
						if err := c.app.SetAccessPolicy(context.Background(), core, before); err != nil {
							t.Fatal(err)
						}
						if source == "menu" {
							choice := "2"
							if list == "black" {
								choice = "3"
							}
							c.reader = bufio.NewReader(strings.NewReader(choice + "\n3\n1\n0\n0\n"))
							if err := c.accessPolicyMenu(context.Background(), core); err != nil {
								t.Fatal(err)
							}
							if !strings.Contains(out.String(), "清空") || strings.Contains(out.String(), "操作失败") {
								t.Fatalf("clear did not succeed: %s", out.String())
							}
							if mode == domain.AccessModeWhitelist && list == "white" && !strings.Contains(out.String(), "仍保持仅允许白名单模式") {
								t.Fatal("clear preview omitted retained restriction")
							}
						} else {
							cmd := c.accessCommand()
							cmd.SetArgs([]string{"clear", core, list})
							if err := cmd.Execute(); err != nil {
								t.Fatal(err)
							}
						}
						want := before
						if list == "white" {
							want.Whitelist = nil
						} else {
							want.Blacklist = nil
						}
						n, err := c.app.Store.Load(core)
						if err != nil || !reflect.DeepEqual(n.AccessPolicy, want) {
							t.Fatalf("policy=%#v, want=%#v, err=%v", n.AccessPolicy, want, err)
						}
						p, _ := c.app.Registry.Get(core)
						config, err := os.ReadFile(c.app.Layout.Resolve(p.ConfigPath()))
						if err != nil || n.ConfigSHA256 != system.SHA256(config) {
							t.Fatalf("clear did not persist applied config: %v", err)
						}
						expected, err := p.RenderServer(n)
						if err != nil {
							t.Fatal(err)
						}
						var actualRules, expectedRules any
						if err := json.Unmarshal(config, &actualRules); err != nil {
							t.Fatal(err)
						}
						if err := json.Unmarshal(expected, &expectedRules); err != nil {
							t.Fatal(err)
						}
						if !reflect.DeepEqual(actualRules, expectedRules) {
							t.Fatal("clear retained stale whitelist/blacklist rules or final deny")
						}
					})
				}
			}
		}
	}
}

func TestClearEmptyWhitelistPreservesWhitelistMode(t *testing.T) {
	for _, core := range []string{domain.CoreXray, domain.CoreSingBox} {
		t.Run(core, func(t *testing.T) {
			c, _ := accessCLI(t, core)
			if err := c.app.SetAccessPolicy(context.Background(), core, domain.AccessPolicy{Mode: domain.AccessModeWhitelist}); err != nil {
				t.Fatal(err)
			}
			c.reader = bufio.NewReader(strings.NewReader("2\n3\n0\n0\n"))
			if err := c.accessPolicyMenu(context.Background(), core); err != nil {
				t.Fatal(err)
			}
			n, err := c.app.Store.Load(core)
			if err != nil || n.AccessPolicy.EffectiveMode() != domain.AccessModeWhitelist || !n.AccessPolicy.Active() {
				t.Fatalf("empty whitelist mode was lost: %#v, err=%v", n.AccessPolicy, err)
			}
		})
	}
}

func TestAccessClearCommandRequiresConfirmation(t *testing.T) {
	c, _ := accessCLI(t, domain.CoreXray)
	if err := c.app.SetAccessPolicy(context.Background(), domain.CoreXray, domain.AccessPolicy{
		Whitelist: []domain.AccessEntry{{Type: domain.AccessEntryDomain, Value: "allowed.com", IncludeSubdomains: true}},
	}); err != nil {
		t.Fatal(err)
	}
	path := c.app.Layout.AccessPolicyPath(domain.CoreXray)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	c.yes = false
	cmd := c.accessCommand()
	cmd.SetArgs([]string{"clear", domain.CoreXray, "white"})
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "--yes") {
		t.Fatalf("unconfirmed clear accepted: %v", err)
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("unconfirmed clear changed stored policy")
	}
}
