package cli

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"proxyforge/internal/app"
	"proxyforge/internal/domain"
	"proxyforge/internal/provider"
	"proxyforge/internal/provider/singbox"
	"proxyforge/internal/provider/xray"
	"proxyforge/internal/system"
)

type accessRunner struct{}

func (accessRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	if name == "systemctl" && len(args) > 0 {
		if args[0] == "show" {
			if strings.Contains(strings.Join(args, " "), "LoadState") {
				return []byte("loaded\n"), nil
			}
			return []byte("root\n"), nil
		}
		if args[0] == "is-active" {
			return []byte("active\n"), nil
		}
	}
	return nil, nil
}

func accessCLI(t *testing.T, core string) (*commandSet, *bytes.Buffer) {
	t.Helper()
	layout := system.Layout{Root: t.TempDir()}
	var out bytes.Buffer
	a := app.New(provider.NewRegistry(xray.New(), singbox.New()), accessRunner{}, layout, io.Discard)
	a.RootCheck = func() error { return nil }
	a.LookPath = func(name string) (string, error) { return "/usr/bin/" + name, nil }
	a.Listening = func(context.Context, int, time.Duration) error { return nil }
	n := domain.NodeSpec{Core: core, ManagedBy: "proxyforge", SchemaVersion: domain.StateSchemaVersion, InboundTag: "main-in", Server: "server.example.com", Port: 15443, SNI: "example.com", Target: "example.com:443", UserName: "one", UUID: "123e4567-e89b-42d3-a456-426614174000", PrivateKey: "private", PublicKey: "public", ShortID: "0123456789abcdef"}
	p, _ := a.Registry.Get(core)
	b, err := p.RenderServer(n)
	if err != nil {
		t.Fatal(err)
	}
	if err := system.AtomicWrite(layout.Resolve(p.ConfigPath()), b, 0600); err != nil {
		t.Fatal(err)
	}
	n.ConfigSHA256 = system.SHA256(b)
	if err := a.Store.Save(n); err != nil {
		t.Fatal(err)
	}
	return &commandSet{app: a, in: strings.NewReader(""), reader: bufio.NewReader(strings.NewReader("")), out: &out, yes: true}, &out
}

func TestAccessCommandsAndList(t *testing.T) {
	for _, core := range []string{domain.CoreXray, domain.CoreSingBox} {
		t.Run(core, func(t *testing.T) {
			c, out := accessCLI(t, core)
			for _, args := range [][]string{{"add", core, "white", "Example.COM"}, {"add", core, "white", "example.com", "--exact"}, {"add", core, "black", "192.168.1.0/24"}, {"mode", core, "whitelist"}, {"list", core}, {"remove", core, "white", "example.com", "--exact"}} {
				cmd := c.accessCommand()
				cmd.SetArgs(args)
				if err := cmd.Execute(); err != nil {
					t.Fatal(err)
				}
			}
			policy, err := c.app.AccessPolicy(core)
			if err != nil || policy.EffectiveMode() != "whitelist" || len(policy.Whitelist) != 1 || !policy.Whitelist[0].IncludeSubdomains || len(policy.Blacklist) != 1 {
				t.Fatalf("policy=%#v err=%v", policy, err)
			}
			if !strings.Contains(out.String(), "包括子域名") || !strings.Contains(out.String(), "仅允许白名单访问") {
				t.Fatal(out.String())
			}
			p, _ := c.app.Registry.Get(core)
			before, _ := os.ReadFile(c.app.Layout.Resolve(p.ConfigPath()))
			c.yes = false
			cmd := c.accessCommand()
			cmd.SetArgs([]string{"mode", core, "blacklist"})
			if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "--yes") {
				t.Fatalf("expected noninteractive confirmation requirement: %v", err)
			}
			after, _ := os.ReadFile(c.app.Layout.Resolve(p.ConfigPath()))
			if !bytes.Equal(before, after) {
				t.Fatal("unconfirmed policy change applied")
			}
		})
	}
}

func TestAccessMenuShowsModesAndLists(t *testing.T) {
	c, out := accessCLI(t, domain.CoreXray)
	c.reader = bufio.NewReader(strings.NewReader("0\n"))
	if err := c.accessPolicyMenu(context.Background(), domain.CoreXray); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"默认模式", "添加白名单", "添加黑名单", "白名单（0 条）", "黑名单（0 条）"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("missing %q in %s", want, out.String())
		}
	}
}

func TestAccessMenuPreviewsEmptyWhitelistAndCancels(t *testing.T) {
	c, out := accessCLI(t, domain.CoreXray)
	c.reader = bufio.NewReader(strings.NewReader("1\n2\nq\n0\n"))
	if err := c.accessPolicyMenu(context.Background(), domain.CoreXray); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "默认动作：拒绝；白名单为空时全部拒绝") {
		t.Fatal("empty whitelist effect not shown before confirmation")
	}
	policy, err := c.app.AccessPolicy(domain.CoreXray)
	if err != nil || policy.EffectiveMode() != domain.AccessModeDefault {
		t.Fatal("cancelled mode change was applied")
	}
}
