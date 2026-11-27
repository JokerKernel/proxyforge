package cli

import (
	"bufio"
	"context"
	"strings"
	"testing"

	"proxyforge/internal/domain"
	"proxyforge/internal/system"
)

func TestRelayAccessCommandsAndMenuOnlyChangeSelectedLine(t *testing.T) {
	for _, core := range []string{domain.CoreXray, domain.CoreSingBox} {
		t.Run(core, func(t *testing.T) {
			c, out := accessCLI(t, core)
			n, err := c.app.Store.Load(core)
			if err != nil {
				t.Fatal(err)
			}
			for i, name := range []string{"first", "second"} {
				n.RelayLinks = append(n.RelayLinks, domain.RelayLink{Name: name, UserName: name, UUID: []string{"223e4567-e89b-42d3-a456-426614174000", "323e4567-e89b-42d3-a456-426614174000"}[i], Enabled: true,
					Upstream: domain.LandingPeer{Server: "exit.example.com", Port: 443}, AccessPolicy: &domain.AccessPolicy{Disabled: true}})
			}
			p, _ := c.app.Registry.Get(core)
			config, err := p.RenderServer(n)
			if err != nil {
				t.Fatal(err)
			}
			if err := system.AtomicWrite(c.app.Layout.Resolve(p.ConfigPath()), config, 0600); err != nil {
				t.Fatal(err)
			}
			n.ConfigSHA256 = system.SHA256(config)
			if err := c.app.Store.Save(n); err != nil {
				t.Fatal(err)
			}
			for _, args := range [][]string{
				{"add", core, "white", "first.test", "--relay", "first", "--ports", "443.80"},
				{"mode", core, "whitelist", "--relay", "first"},
				{"enable", core, "--relay", "first"},
				{"list", core, "--relay", "first"},
			} {
				cmd := c.accessCommand()
				cmd.SetArgs(args)
				if err := cmd.Execute(); err != nil {
					t.Fatal(err)
				}
			}
			// Enter through the line's actual details menu, then clear its whitelist.
			c.reader = bufio.NewReader(strings.NewReader("7\n2\n3\nyes\n0\n0\n0\nparent-choice\n"))
			if err := c.manageRelayDetails(context.Background(), core, "first"); err != nil {
				t.Fatal(err)
			}
			remaining, _ := c.reader.ReadString('\n')
			if remaining != "parent-choice\n" {
				t.Fatalf("relay access menu skipped its parent: %q", remaining)
			}
			n, err = c.app.Store.Load(core)
			if err != nil || n.AccessPolicy.Active() || !n.RelayLinks[1].AccessPolicy.Disabled || len(n.RelayLinks[1].AccessPolicy.Whitelist) != 0 {
				t.Fatalf("selected relay changed unrelated policy: %+v, %v", n, err)
			}
			first := n.RelayLinks[0].AccessPolicy
			if first.Disabled || len(first.Whitelist) != 0 || first.Mode != domain.AccessModeWhitelist {
				t.Fatalf("relay menu did not retain empty manual whitelist restriction: %+v", first)
			}
			if !strings.Contains(out.String(), "放行出站") || !strings.Contains(out.String(), "first-out") || !strings.Contains(out.String(), "80,443") {
				t.Fatalf("relay scope/ports missing from output: %s", out.String())
			}
		})
	}
}
