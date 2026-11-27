package cli

import (
	"bufio"
	"bytes"
	"context"
	"os"
	"strings"
	"testing"

	"proxyforge/internal/app"
	"proxyforge/internal/domain"
	"proxyforge/internal/provider"
	"proxyforge/internal/provider/singbox"
	"proxyforge/internal/provider/xray"
	"proxyforge/internal/system"
)

func TestClientCommandOffersClashFormat(t *testing.T) {
	c := &commandSet{}
	flag := c.clientCommand().Flags().Lookup("format")
	if flag == nil || flag.DefValue != app.ClientFormatNative {
		t.Fatalf("format flag=%v, want default %q", flag, app.ClientFormatNative)
	}
}

func TestClientMenuOutputsClashYAML(t *testing.T) {
	layout := system.Layout{Root: t.TempDir()}
	store := system.StateStore{Layout: layout}
	if err := store.Save(domain.NodeSpec{
		ManagedBy: "proxyforge", Core: domain.CoreSingBox, Server: "server.example.com", Port: 443,
		SNI: "www.example.com", UUID: "123e4567-e89b-42d3-a456-426614174000",
		PublicKey: "public", ShortID: "0123456789abcdef",
	}); err != nil {
		t.Fatal(err)
	}
	a := &app.App{
		Registry: provider.NewRegistry(singbox.New(), xray.New()), Store: store,
		RootCheck: func() error { return nil },
	}
	var out bytes.Buffer
	c := &commandSet{app: a, reader: bufio.NewReader(strings.NewReader("1\n0\n")), out: &out}
	err := c.clientMenu(context.Background(), domain.CoreSingBox)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "中转节点客户端配置") || !strings.Contains(out.String(), "Clash YAML") || !strings.Contains(out.String(), "type: vless") {
		t.Fatalf("output=%q", out.String())
	}
}

func TestClientMenuDisplaysRelayClientConfig(t *testing.T) {
	layout := system.Layout{Root: t.TempDir()}
	store := system.StateStore{Layout: layout}
	if err := store.Save(domain.NodeSpec{
		ManagedBy: "proxyforge", Core: domain.CoreSingBox, InboundTag: "singbox-one",
		Server: "relay.example.com", Port: 443, SNI: "www.example.com",
		UUID: "123e4567-e89b-42d3-a456-426614174000", PublicKey: "public", ShortID: "0123456789abcdef",
		RelayLinks: []domain.RelayLink{{
			Name: "us-exit", UserName: "us-exit",
			UUID: "223e4567-e89b-42d3-a456-426614174000", Enabled: true,
		}},
	}); err != nil {
		t.Fatal(err)
	}
	a := &app.App{
		Registry: provider.NewRegistry(singbox.New(), xray.New()), Store: store,
		RootCheck: func() error { return nil },
	}
	var out bytes.Buffer
	c := &commandSet{app: a, reader: bufio.NewReader(strings.NewReader("3\n1\n1\n0\n0\n0\n")), out: &out}
	err := c.clientMenu(context.Background(), domain.CoreSingBox)
	if err != nil {
		t.Fatal(err)
	}
	got := out.String()
	for _, want := range []string{
		"中转节点客户端配置", "us-exit", "[已启用]",
		"中转节点 us-exit 的 Clash YAML 配置", "type: vless",
		`uuid: "223e4567-e89b-42d3-a456-426614174000"`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("relay client output missing %q: %q", want, got)
		}
	}
	if strings.Contains(got, "proxyforge-relay-us-exit") {
		t.Fatalf("relay client menu leaked legacy user prefix: %q", got)
	}
}

func TestClientMenuReportsWhenNoRelayLinksExist(t *testing.T) {
	layout := system.Layout{Root: t.TempDir()}
	store := system.StateStore{Layout: layout}
	if err := store.Save(domain.NodeSpec{ManagedBy: "proxyforge", Core: domain.CoreXray}); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	c := &commandSet{
		app:    &app.App{Store: store},
		reader: bufio.NewReader(strings.NewReader("3\n0\n")),
		out:    &out,
	}
	err := c.clientMenu(context.Background(), domain.CoreXray)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "尚未配置中转线路") {
		t.Fatalf("output=%q", out.String())
	}
}

func TestClientMenuReturnsOneLevelAndKeepsOutputInItsOptions(t *testing.T) {
	t.Setenv("TERM", "xterm")
	input, err := os.Open("/dev/null")
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	for _, core := range []string{domain.CoreXray, domain.CoreSingBox} {
		for _, back := range []string{"0", "q", "Q"} {
			t.Run(core+"/"+back, func(t *testing.T) {
				store := system.StateStore{Layout: system.Layout{Root: t.TempDir()}}
				mainUUID := "123e4567-e89b-42d3-a456-426614174000"
				firstUUID := "223e4567-e89b-42d3-a456-426614174000"
				secondUUID := "323e4567-e89b-42d3-a456-426614174000"
				if err := store.Save(domain.NodeSpec{
					ManagedBy: "proxyforge", Core: core, InboundTag: "main-in",
					Server: "relay.example.com", Port: 443, SNI: "www.example.com",
					UUID: mainUUID, PublicKey: "public", ShortID: "0123456789abcdef",
					RelayLinks: []domain.RelayLink{
						{Name: "first", UserName: "first", UUID: firstUUID, Enabled: true},
						{Name: "second", UserName: "second", UUID: secondUUID, Enabled: true},
					},
				}); err != nil {
					t.Fatal(err)
				}
				// Back out of the first format page, select another line, view its
				// YAML and return through both parent menus to view ordinary JSON.
				sequence := "3\n1\n" + back + "\n2\n1\n\n" + back + "\n" + back + "\n2\n\n" + back + "\nparent-choice\n"
				var out menuTerminalBuffer
				c := &commandSet{
					app: &app.App{Store: store, Registry: provider.NewRegistry(singbox.New(), xray.New()), Runner: accessRunner{}, RootCheck: func() error { return nil }},
					in:  input, out: &out, errOut: &out,
					reader: bufio.NewReader(menuByteReader{strings.NewReader(sequence)}),
				}
				if !c.interactiveUI() {
					t.Fatal("test must exercise interactive pauses")
				}
				if err := c.clientMenu(context.Background(), core); err != nil {
					t.Fatal(err)
				}
				remaining, err := c.reader.ReadString('\n')
				if err != nil || remaining != "parent-choice\n" {
					t.Fatalf("return consumed input intended for a parent: %q, %v", remaining, err)
				}
				got := out.String()
				if !strings.Contains(got, "中转节点 second 的 Clash YAML 配置") || !strings.Contains(got, secondUUID) || !strings.Contains(got, mainUUID) || strings.Contains(got, firstUUID) {
					t.Fatalf("return skipped options or exported the wrong user: %s", got)
				}
				if strings.Count(got, "按 Enter 返回菜单") != 2 || strings.Contains(got, "操作失败") {
					t.Fatalf("back unexpectedly paused or export failed: %s", got)
				}
			})
		}
	}
}
