package cli

import (
	"bufio"
	"bytes"
	"context"
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
	c := &commandSet{app: a, reader: bufio.NewReader(strings.NewReader("2\n")), out: &out}
	pause, err := c.clientMenu(context.Background(), domain.CoreSingBox)
	if err != nil {
		t.Fatal(err)
	}
	if !pause || !strings.Contains(out.String(), "中转节点客户端配置") || !strings.Contains(out.String(), "Clash YAML") || !strings.Contains(out.String(), "type: vless") {
		t.Fatalf("pause=%v output=%q", pause, out.String())
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
	c := &commandSet{app: a, reader: bufio.NewReader(strings.NewReader("3\n1\n2\n")), out: &out}
	pause, err := c.clientMenu(context.Background(), domain.CoreSingBox)
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
	if !pause {
		t.Fatal("relay client output should pause before returning to the core menu")
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
		reader: bufio.NewReader(strings.NewReader("3\n")),
		out:    &out,
	}
	pause, err := c.clientMenu(context.Background(), domain.CoreXray)
	if err != nil {
		t.Fatal(err)
	}
	if !pause || !strings.Contains(out.String(), "尚未配置中转线路") {
		t.Fatalf("pause=%v output=%q", pause, out.String())
	}
}
