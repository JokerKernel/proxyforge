package cli

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os"
	"strings"
	"testing"

	"proxyforge/internal/app"
	"proxyforge/internal/domain"
	"proxyforge/internal/system"
)

func TestMenusReturnToImmediateParentWithoutExtraPause(t *testing.T) {
	t.Setenv("TERM", "xterm")
	input, err := os.Open("/dev/null")
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	tests := []struct {
		name, choices string
		pauses        int
		fallback      bool
	}{
		{name: "install confirmation", choices: "1 1 BACK BACK BACK"},
		{name: "install channel", choices: "1 2 BACK BACK BACK"},
		{name: "install version", choices: "1 3 BACK BACK BACK"},
		{name: "uninstall confirmation", choices: "4 BACK BACK"},
		{name: "server view", choices: "2 2 ENTER BACK BACK", pauses: 1},
		{name: "log confirmation", choices: "2 5 LOG_OTHER BACK BACK BACK BACK"},
		{name: "log result", choices: "2 5 LOG_CURRENT ENTER BACK BACK BACK", pauses: 1},
		{name: "DNS confirmation", choices: "2 3 5 DNS_OTHER BACK BACK BACK BACK BACK"},
		{name: "outbound confirmation", choices: "2 3 4 OUT_OTHER BACK BACK BACK BACK BACK"},
		{name: "outbound result", choices: "2 3 4 OUT_CURRENT ENTER BACK BACK BACK BACK", pauses: 1},
		{name: "fallback confirmation", choices: "2 3 5 FALL_OTHER BACK BACK BACK BACK BACK", fallback: true},
		{name: "access mode", choices: "2 3 1 1 BACK BACK BACK BACK BACK"},
		{name: "access domain match", choices: "2 3 1 2 1 example.com BACK BACK BACK BACK BACK BACK BACK"},
		{name: "access ports", choices: "2 3 1 2 1 example.com 1 BACK BACK BACK BACK BACK BACK BACK"},
		{name: "SNI results", choices: "2 3 6 BACK BACK BACK BACK"},
		{name: "relay client format", choices: "3 3 1 BACK BACK BACK BACK"},
		{name: "relay management export", choices: "2 7 2 1 1 BACK BACK BACK BACK BACK BACK"},
		{name: "relay management confirmation", choices: "2 7 2 1 4 BACK BACK BACK BACK BACK BACK"},
		{name: "landing management confirmation", choices: "2 7 4 1 2 BACK BACK BACK BACK BACK BACK"},
		{name: "add landing name", choices: "2 7 3 1 BACK BACK BACK BACK BACK"},
		{name: "add landing confirmation", choices: "2 7 3 1 new-access BACK BACK BACK BACK BACK"},
		{name: "add relay form", choices: "2 7 1 2 BACK BACK BACK BACK BACK"},
	}
	for _, core := range []string{domain.CoreXray, domain.CoreSingBox} {
		for _, back := range []string{"0", "q", "Q"} {
			for _, test := range tests {
				t.Run(core+"/"+test.name+"/"+back, func(t *testing.T) {
					c, _ := accessCLI(t, core)
					node, err := c.app.Store.Load(core)
					if err != nil {
						t.Fatal(err)
					}
					if test.fallback {
						if core == domain.CoreXray {
							node.XrayFallbackGuard = true
							node.XrayFallbackPort = 15444
						} else {
							node.SingBoxFallbackGuard = true
							node.SingBoxFallbackPort = 15444
						}
					}
					p, _ := c.app.Registry.Get(core)
					config, err := p.RenderServer(node)
					if err != nil {
						t.Fatal(err)
					}
					if err := system.AtomicWrite(c.app.Layout.Resolve(p.ConfigPath()), config, 0600); err != nil {
						t.Fatal(err)
					}
					node.ConfigSHA256 = system.SHA256(config)
					node.RelayLinks = []domain.RelayLink{{Name: "relay", UserName: "relay", UUID: "223e4567-e89b-42d3-a456-426614174000", Enabled: true, Upstream: domain.LandingPeer{Server: "exit.example.com", Port: 443}}}
					node.LandingAccesses = []domain.LandingAccess{{Name: "landing", UserName: "landing", UUID: "323e4567-e89b-42d3-a456-426614174000", Enabled: true}}
					if err := c.app.Store.Save(node); err != nil {
						t.Fatal(err)
					}
					paths := []string{c.app.Layout.Resolve(p.ConfigPath()), c.app.Layout.StatePath(core), c.app.Layout.AccessPolicyPath(core), c.app.Layout.LandingPath(core), c.app.Layout.RelayPath(core)}
					original := make([][]byte, len(paths))
					for i, path := range paths {
						original[i], err = os.ReadFile(path)
						if err != nil {
							t.Fatal(err)
						}
					}
					logSettings, err := c.app.LogLevelSettings(context.Background(), core)
					if err != nil {
						t.Fatal(err)
					}
					dns, err := c.app.DNSSettings(context.Background(), core)
					if err != nil {
						t.Fatal(err)
					}
					outbound, err := c.app.OutboundIPSettings(context.Background(), core)
					if err != nil {
						t.Fatal(err)
					}
					choices := test.choices
					replacements := []string{"BACK", back, "ENTER", "", "LOG_OTHER", navigationChoice(t, logSettings.Levels, logSettings.Current, false), "LOG_CURRENT", navigationChoice(t, logSettings.Levels, logSettings.Current, true), "DNS_OTHER", navigationChoice(t, dns.Profiles, dns.Current, false), "OUT_OTHER", navigationChoice(t, outbound.Strategies, outbound.Current, false), "OUT_CURRENT", navigationChoice(t, outbound.Strategies, outbound.Current, true)}
					if test.fallback {
						fallback, err := c.app.FallbackIPSettings(context.Background(), core)
						if err != nil {
							t.Fatal(err)
						}
						replacements = append(replacements, "FALL_OTHER", navigationChoice(t, fallback.Strategies, fallback.Current, false))
					}
					choices = strings.NewReplacer(replacements...).Replace(choices)
					sequence := strings.ReplaceAll(choices, " ", "\n") + "\nparent-choice\n"
					var out menuTerminalBuffer
					c.in, c.out, c.errOut, c.yes = input, &out, &out, false
					c.reader = bufio.NewReader(menuByteReader{strings.NewReader(sequence)})
					c.probeSNI = func(context.Context, []string, string, int) ([]app.SNICandidate, error) {
						return []app.SNICandidate{{Domain: node.SNI}}, nil
					}
					if err := c.coreMenu(context.Background(), core); err != nil {
						t.Fatalf("menu skipped a level or consumed a parent choice: %v\n%s", err, out.String())
					}
					remaining, err := c.reader.ReadString('\n')
					if err != nil || remaining != "parent-choice\n" {
						t.Fatalf("wrong return depth, remaining=%q err=%v\n%s", remaining, err, out.String())
					}
					if got := strings.Count(out.String(), "按 Enter 返回菜单"); got != test.pauses {
						t.Fatalf("unexpected pause count=%d want=%d\n%s", got, test.pauses, out.String())
					}
					if strings.Contains(out.String(), "操作失败") {
						t.Fatalf("navigation reported an action failure: %s", out.String())
					}
					for i, path := range paths {
						data, err := os.ReadFile(path)
						if err != nil || !bytes.Equal(data, original[i]) {
							t.Fatalf("cancelled navigation changed %s: %v", path, err)
						}
					}
				})
			}
		}
	}
}

func navigationChoice(t *testing.T, options []string, current string, same bool) string {
	t.Helper()
	for i, option := range options {
		if (option == current) == same {
			return fmt.Sprint(i + 1)
		}
	}
	t.Fatalf("no suitable menu option for current=%q same=%v: %v", current, same, options)
	return ""
}

func TestNestedAddressAndSNIPickersReturnToTheirOptions(t *testing.T) {
	for _, back := range []string{"0", "q", "Q"} {
		t.Run("physical address/"+back, func(t *testing.T) {
			var out bytes.Buffer
			c := &commandSet{
				reader: bufio.NewReader(strings.NewReader("1\n" + back + "\n3\nparent-choice\n")), out: &out,
				physicalIPs: func() ([]app.PublicInterfaceAddress, error) {
					return []app.PublicInterfaceAddress{{Interface: "eth0", Address: "8.8.8.8"}, {Interface: "eth1", Address: "1.1.1.1"}}, nil
				},
			}
			selected, err := c.selectPublicAddress(context.Background())
			if err != nil || selected != "" {
				t.Fatalf("address picker skipped its source options: %q, %v", selected, err)
			}
			remaining, _ := c.reader.ReadString('\n')
			if remaining != "parent-choice\n" || strings.Contains(out.String(), "获取公网地址失败") {
				t.Fatalf("cancel became a failure or consumed parent input: %q\n%s", remaining, out.String())
			}
		})
		for _, confirm := range []bool{false, true} {
			t.Run(fmt.Sprintf("manual SNI/%s/confirmation=%t", back, confirm), func(t *testing.T) {
				var out bytes.Buffer
				sequence := "m\n"
				if confirm {
					sequence += "manual.example.com\n" + back + "\n"
				}
				sequence += back + "\n1\nparent-choice\n"
				c := &commandSet{
					reader: bufio.NewReader(strings.NewReader(sequence)), out: &out,
					probeSNI: func(_ context.Context, domains []string, _ string, _ int) ([]app.SNICandidate, error) {
						return []app.SNICandidate{{Domain: domains[0]}}, nil
					},
				}
				selected, err := c.selectSNICandidate(context.Background(), "server.example.com")
				if err != nil || selected != defaultSNICandidates[0] {
					t.Fatalf("manual SNI did not return through its parent options: %q, %v\n%s", selected, err, out.String())
				}
				remaining, _ := c.reader.ReadString('\n')
				if remaining != "parent-choice\n" {
					t.Fatalf("SNI return consumed parent input: %q", remaining)
				}
			})
		}
	}
}
