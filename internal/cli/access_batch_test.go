package cli

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"proxyforge/internal/domain"
)

type accessBatchRunner struct {
	accessRunner
	restarts    int
	failRestart bool
}

func (r *accessBatchRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	if name == "systemctl" && len(args) > 0 && args[0] == "restart" {
		r.restarts++
		if r.failRestart {
			r.failRestart = false
			return nil, errors.New("injected restart failure")
		}
	}
	return r.accessRunner.Run(ctx, name, args...)
}

func prepareAccessBatch(t *testing.T, core string) (*commandSet, *accessBatchRunner, func()) {
	t.Helper()
	c, out := accessCLI(t, core)
	c.errOut = out
	policy := switchPolicy()
	policy.Mode = domain.AccessModeDefault
	if err := c.app.SetAccessPolicy(context.Background(), core, policy); err != nil {
		t.Fatal(err)
	}
	r := &accessBatchRunner{}
	c.app.Runner, c.app.Services.Runner = r, r
	p, _ := c.app.Registry.Get(core)
	paths := []string{c.app.Layout.Resolve(p.ConfigPath()), c.app.Layout.StatePath(core), c.app.Layout.AccessPolicyPath(core)}
	before := make([][]byte, len(paths))
	for i, path := range paths {
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		before[i] = b
	}
	return c, r, func() {
		t.Helper()
		for i, path := range paths {
			after, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(before[i], after) {
				t.Fatalf("unapplied batch changed %s: %v", path, err)
			}
		}
	}
}

func runBatchCommand(c *commandSet, core, list, input string, exact bool) error {
	cmd := c.accessCommand()
	args := append([]string{"add", core, list}, strings.Fields(input)...)
	if exact {
		args = append(args, "--exact")
	}
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs(args)
	return cmd.Execute()
}

func TestAccessBatchMenuAndCommandsApplyMixedTargetsOnce(t *testing.T) {
	for _, core := range []string{domain.CoreXray, domain.CoreSingBox} {
		for _, list := range []string{"white", "black"} {
			for _, source := range []string{"menu", "command"} {
				for _, exact := range []bool{false, true} {
					t.Run(core+"/"+list+"/"+source+"/exact="+strconv.FormatBool(exact), func(t *testing.T) {
						c, r, _ := prepareAccessBatch(t, core)
						input := "NEW.example.com,new.example.com 192.168.1.10，192.168.1.99/24"
						if source == "menu" {
							choice, match := "2", "1"
							if list == "black" {
								choice = "3"
							}
							if exact {
								match = "2"
							}
							c.reader = bufio.NewReader(strings.NewReader(choice + "\n1\n" + input + "\n" + match + "\n\n\n\n1\n0\n0\n"))
							if err := c.accessPolicyMenu(context.Background(), core); err != nil {
								t.Fatal(err)
							}
						} else if err := runBatchCommand(c, core, list, input, exact); err != nil {
							t.Fatal(err)
						}
						want := switchPolicy()
						want.Mode = domain.AccessModeDefault
						additions := []domain.AccessEntry{
							{Type: domain.AccessEntryDomain, Value: "new.example.com", IncludeSubdomains: !exact, Ports: domain.DefaultAccessDomainPorts},
							{Type: domain.AccessEntryIP, Value: "192.168.1.10"},
							{Type: domain.AccessEntryCIDR, Value: "192.168.1.0/24"},
						}
						if list == "white" {
							want.Whitelist = append(want.Whitelist, additions...)
						} else {
							want.Blacklist = append(want.Blacklist, additions...)
						}
						got, err := c.app.AccessPolicy(core)
						if err != nil || !reflect.DeepEqual(got, want) || r.restarts != 1 {
							t.Fatalf("batch or other list incorrect: got=%#v want=%#v restarts=%d err=%v", got, want, r.restarts, err)
						}
						if err := runBatchCommand(c, core, list, input, exact); err != nil || r.restarts != 1 {
							t.Fatalf("duplicate batch unnecessarily applied: restarts=%d err=%v", r.restarts, err)
						}
					})
				}
			}
		}
	}
}

func TestAccessBatchInvalidCancellationAndRestartFailureKeepFiles(t *testing.T) {
	for _, core := range []string{domain.CoreXray, domain.CoreSingBox} {
		for _, test := range []string{"invalid-command", "invalid-menu", "cancel-menu", "restart-failure"} {
			t.Run(core+"/"+test, func(t *testing.T) {
				c, r, assertUnchanged := prepareAccessBatch(t, core)
				input := "good.example.com,https://invalid.example.com,192.168.1.10"
				switch test {
				case "invalid-command":
					if err := runBatchCommand(c, core, "white", input, false); err == nil || !strings.Contains(err.Error(), "第 2 个目标") {
						t.Fatalf("invalid batch accepted: %v", err)
					}
				case "invalid-menu":
					c.reader = bufio.NewReader(strings.NewReader("3\n1\n" + input + "\n0\n0\n"))
					if err := c.accessPolicyMenu(context.Background(), core); err != nil {
						t.Fatal(err)
					}
				case "cancel-menu":
					c.reader = bufio.NewReader(strings.NewReader("2\n1\ngood.example.com,192.168.1.10\n1\nq\n0\n0\n"))
					if err := c.accessPolicyMenu(context.Background(), core); err != nil {
						t.Fatal(err)
					}
				case "restart-failure":
					r.failRestart = true
					if err := runBatchCommand(c, core, "black", "good.example.com 192.168.1.10", false); err == nil {
						t.Fatal("restart failure accepted")
					}
				}
				if test != "restart-failure" && r.restarts != 0 {
					t.Fatal("unconfirmed or invalid batch restarted the service")
				}
				assertUnchanged()
			})
		}
	}
}

func TestAccessBatchKeepsDisabledSwitchAndRuntimeConfig(t *testing.T) {
	for _, core := range []string{domain.CoreXray, domain.CoreSingBox} {
		t.Run(core, func(t *testing.T) {
			c, _, _ := prepareAccessBatch(t, core)
			ctx := context.Background()
			initial := switchPolicy()
			initial.Disabled = true
			initial.Mode = domain.AccessModeWhitelist
			if err := c.app.SetAccessPolicy(ctx, core, initial); err != nil {
				t.Fatal(err)
			}
			p, _ := c.app.Registry.Get(core)
			path := c.app.Layout.Resolve(p.ConfigPath())
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if err := runBatchCommand(c, core, "white", "new.example.com 192.168.1.0/24", false); err != nil {
				t.Fatal(err)
			}
			policy, err := c.app.AccessPolicy(core)
			if err != nil || !policy.Disabled || policy.EffectiveMode() != domain.AccessModeWhitelist || len(policy.Whitelist) != 3 {
				t.Fatalf("batch changed disabled switch or saved mode: %#v, err=%v", policy, err)
			}
			after, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(before, after) {
				t.Fatal("disabled bulk addition changed runtime rules")
			}
		})
	}
}
