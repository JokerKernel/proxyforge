package cli

import (
	"bufio"
	"context"
	"io"
	"os"
	"reflect"
	"strings"
	"testing"

	"proxyforge/internal/domain"
)

func runAccess(t *testing.T, c *commandSet, args ...string) error {
	t.Helper()
	cmd := c.accessCommand()
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs(args)
	return cmd.Execute()
}

func TestAccessPortRecordsDeletionAndModeLifecycle(t *testing.T) {
	for _, core := range []string{domain.CoreXray, domain.CoreSingBox} {
		t.Run(core, func(t *testing.T) {
			c, _ := accessCLI(t, core)
			for _, args := range [][]string{
				{"add", core, "white", "a.example.com"},
				{"add", core, "white", "a.example.com", "--ports", "553"},
				{"add", core, "white", "b.example.com", "--ports", "443.80"},
			} {
				if err := runAccess(t, c, args...); err != nil {
					t.Fatal(err)
				}
			}
			policy, err := c.app.AccessPolicy(core)
			if err != nil || policy.Mode != "" || !policy.AutoWhitelist || policy.EffectiveMode() != domain.AccessModeWhitelist || len(policy.Whitelist) != 3 || policy.Whitelist[0].Ports != "80,443" || policy.Whitelist[1].Ports != "553" || policy.Whitelist[2].Ports != "80,443" {
				t.Fatalf("incorrect raw records: %+v, %v", policy, err)
			}
			if err := runAccess(t, c, "remove", core, "white", "a.example.com", "--ports", "80,443,553"); err == nil {
				t.Fatal("removed a compiled union instead of a raw record")
			}
			if err := runAccess(t, c, "remove", core, "white", "a.example.com", "--ports", "553"); err != nil {
				t.Fatal(err)
			}
			policy, _ = c.app.AccessPolicy(core)
			if len(policy.Whitelist) != 2 || policy.Whitelist[0].Ports != "80,443" || policy.Whitelist[1].Value != "b.example.com" {
				t.Fatalf("wrong record removed: %+v", policy)
			}
			for _, args := range [][]string{
				{"mode", core, "default"}, {"clear", core, "white"}, {"add", core, "white", "manual.example.com"},
			} {
				if err := runAccess(t, c, args...); err != nil {
					t.Fatal(err)
				}
			}
			policy, _ = c.app.AccessPolicy(core)
			if policy.Mode != domain.AccessModeDefault {
				t.Fatal("addition changed explicit default mode")
			}
			for _, args := range [][]string{{"mode", core, "auto"}, {"clear", core, "white"}} {
				if err := runAccess(t, c, args...); err != nil {
					t.Fatal(err)
				}
			}
			policy, _ = c.app.AccessPolicy(core)
			if policy.Mode != "" || !policy.AutoWhitelist || policy.EffectiveMode() != domain.AccessModeWhitelist || !policy.Active() {
				t.Fatal("clear opened whitelist mode")
			}
			for _, args := range [][]string{{"mode", core, "auto"}, {"disable", core}, {"add", core, "white", "new.example.com", "--ports", "all"}} {
				if err := runAccess(t, c, args...); err != nil {
					t.Fatal(err)
				}
			}
			policy, _ = c.app.AccessPolicy(core)
			if !policy.Disabled || policy.Mode != "" || !policy.AutoWhitelist || policy.EffectiveMode() != domain.AccessModeWhitelist || policy.Whitelist[0].Ports != "" {
				t.Fatalf("auto selection or switch lost: %+v", policy)
			}
		})
	}
}

func TestAccessMenuPerRecordPortsAndInvalidBatch(t *testing.T) {
	for _, core := range []string{domain.CoreXray, domain.CoreSingBox} {
		t.Run(core, func(t *testing.T) {
			c, _ := accessCLI(t, core)
			c.reader = bufio.NewReader(strings.NewReader("2\n1\na.example.com 192.168.1.1\n1\n443.80\n8000-8050,8040-8100\n1\n0\n0\n"))
			if err := c.accessPolicyMenu(context.Background(), core); err != nil {
				t.Fatal(err)
			}
			got, _ := c.app.AccessPolicy(core)
			want := []domain.AccessEntry{{Type: "domain", Value: "a.example.com", IncludeSubdomains: true, Ports: "80,443"}, {Type: "ip", Value: "192.168.1.1", Ports: "8000-8100"}}
			if !reflect.DeepEqual(got.Whitelist, want) {
				t.Fatalf("menu ports lost: %+v", got)
			}
			path := c.app.Layout.AccessPolicyPath(core)
			before, _ := os.ReadFile(path)
			if err := runAccess(t, c, "add", core, "white", "valid.example.com", "192.168.1.1", "--ports", "80,65536"); err == nil {
				t.Fatal("invalid port batch accepted")
			}
			c.reader = bufio.NewReader(strings.NewReader("2\n1\nvalid.example.com 192.168.1.2\n1\n443\n0\n0\n0\n"))
			if err := c.accessPolicyMenu(context.Background(), core); err != nil {
				t.Fatal(err)
			}
			after, _ := os.ReadFile(path)
			if string(before) != string(after) {
				t.Fatal("invalid per-entry batch committed partially")
			}
		})
	}
}
