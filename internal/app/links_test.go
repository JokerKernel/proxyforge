package app

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"proxyforge/internal/domain"
	"proxyforge/internal/provider/xray"
)

func TestLandingAndRelayLifecycle(t *testing.T) {
	runner := &fakeRunner{port: freePort(t)}
	a, _ := testApp(t, runner)
	base, err := a.Generate(context.Background(), domain.CoreXray, domain.GenerateOptions{
		Server: "relay.example.com", Port: runner.port, SNI: "relay.example.com", Target: "relay.example.com:443",
		StandardConfig: true, NonInteractive: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	access, err := a.AddLandingAccess(context.Background(), domain.CoreXray, "from-la")
	if err != nil {
		t.Fatal(err)
	}
	if !access.Enabled || access.UUID == "" {
		t.Fatalf("access=%#v", access)
	}
	bundleBytes, err := a.ExportLandingBundle(domain.CoreXray, "from-la", "", false)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(bundleBytes, []byte(base.PrivateKey)) || bytes.Contains(bundleBytes, []byte(`"private_key"`)) {
		t.Fatalf("private key leaked: %s", bundleBytes)
	}
	peer, err := ParseLandingBundle(bundleBytes)
	if err != nil || peer.UUID != access.UUID {
		t.Fatalf("peer=%#v err=%v", peer, err)
	}
	peer.Server = "exit.example.com"
	link, err := a.AddRelayLink(context.Background(), domain.CoreXray, "la", peer, RelayAddOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !link.Enabled || link.UUID == access.UUID {
		t.Fatalf("link=%#v", link)
	}
	client, err := a.RelayClientConfig(context.Background(), domain.CoreXray, "la", ClientFormatNative, "", false)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(client, []byte(link.UUID)) || bytes.Contains(client, []byte(peer.UUID)) {
		t.Fatalf("unexpected client config: %s", client)
	}

	configPath := a.Layout.Resolve(xray.New().ConfigPath())
	config, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	var root map[string]any
	if err := json.Unmarshal(config, &root); err != nil {
		t.Fatal(err)
	}
	if !xrayConfigHasTag(root, "proxyforge-relay-la") {
		t.Fatalf("relay outbound missing: %s", config)
	}
	if err := a.SetRelayLinkEnabled(context.Background(), domain.CoreXray, "la", false); err != nil {
		t.Fatal(err)
	}
	config, _ = os.ReadFile(configPath)
	if bytes.Contains(config, []byte("proxyforge-relay-la")) {
		t.Fatalf("disabled link remained in config: %s", config)
	}
	state, err := a.Store.Load(domain.CoreXray)
	if err != nil || len(state.RelayLinks) != 1 || state.RelayLinks[0].Enabled {
		t.Fatalf("state=%#v err=%v", state, err)
	}
}

func TestRelayMutationRollsBackConfigAndState(t *testing.T) {
	runner := &fakeRunner{port: freePort(t)}
	a, _ := testApp(t, runner)
	_, err := a.Generate(context.Background(), domain.CoreSingBox, domain.GenerateOptions{
		Server: "relay.example.com", Port: runner.port, SNI: "relay.example.com", Target: "relay.example.com:443",
		StandardConfig: true, NonInteractive: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	peer := domain.LandingPeer{Name: "exit", Core: domain.CoreXray, Server: "exit.example.com", Port: 443, SNI: "exit.example.com",
		UUID: "123e4567-e89b-42d3-a456-426614174000", PublicKey: "public", ShortID: "0123456789abcdef", Flow: domain.VisionFlow}
	if _, err := a.AddRelayLink(context.Background(), domain.CoreSingBox, "la", peer, RelayAddOptions{}); err != nil {
		t.Fatal(err)
	}
	p, _ := a.Registry.Get(domain.CoreSingBox)
	beforeConfig, _ := os.ReadFile(a.Layout.Resolve(p.ConfigPath()))
	beforeState, _ := os.ReadFile(a.Layout.StatePath(domain.CoreSingBox))
	runner.failRestart = true
	err = a.RemoveRelayLink(context.Background(), domain.CoreSingBox, "la")
	if err == nil || !strings.Contains(err.Error(), "已恢复") {
		t.Fatalf("error=%v", err)
	}
	afterConfig, _ := os.ReadFile(a.Layout.Resolve(p.ConfigPath()))
	afterState, _ := os.ReadFile(a.Layout.StatePath(domain.CoreSingBox))
	if !bytes.Equal(beforeConfig, afterConfig) || !bytes.Equal(beforeState, afterState) {
		t.Fatal("rollback did not restore config and state")
	}
}

func TestGeneratePreservesLinksUnlessExplicitlyDropped(t *testing.T) {
	runner := &fakeRunner{port: freePort(t)}
	a, _ := testApp(t, runner)
	opts := domain.GenerateOptions{Server: "relay.example.com", Port: runner.port, SNI: "relay.example.com", Target: "relay.example.com:443", StandardConfig: true, NonInteractive: true}
	if _, err := a.Generate(context.Background(), domain.CoreXray, opts); err != nil {
		t.Fatal(err)
	}
	peer := domain.LandingPeer{Name: "exit", Core: domain.CoreSingBox, Server: "exit.example.com", Port: 443, SNI: "exit.example.com",
		UUID: "123e4567-e89b-42d3-a456-426614174000", PublicKey: "public", ShortID: "0123456789abcdef", Flow: domain.VisionFlow}
	if _, err := a.AddRelayLink(context.Background(), domain.CoreXray, "la", peer, RelayAddOptions{}); err != nil {
		t.Fatal(err)
	}
	regenerated, err := a.Generate(context.Background(), domain.CoreXray, opts)
	if err != nil {
		t.Fatal(err)
	}
	if len(regenerated.RelayLinks) != 1 {
		t.Fatalf("links were not preserved: %#v", regenerated.RelayLinks)
	}
	opts.DropLinks = true
	dropped, err := a.Generate(context.Background(), domain.CoreXray, opts)
	if err != nil {
		t.Fatal(err)
	}
	if len(dropped.RelayLinks) != 0 || len(dropped.LandingAccesses) != 0 {
		t.Fatalf("links were not dropped: %#v", dropped)
	}
	p, _ := a.Registry.Get(domain.CoreXray)
	config, _ := os.ReadFile(a.Layout.Resolve(p.ConfigPath()))
	if bytes.Contains(config, []byte("proxyforge-relay-la")) {
		t.Fatalf("dropped link remained in config: %s", config)
	}
}

func TestParseLandingBundleRejectsInvalidIdentity(t *testing.T) {
	_, err := ParseLandingBundle([]byte(`{"managed_by":"other","schema_version":1,"kind":"proxyforge-landing"}`))
	if err == nil {
		t.Fatal("expected invalid bundle error")
	}
}

func TestLandingPeerAllowsOnlyRequestedTestLAN(t *testing.T) {
	peer := domain.LandingPeer{Name: "test", Core: domain.CoreXray, Server: "192.168.10.20", Port: 443, SNI: "exit.example.com",
		UUID: "123e4567-e89b-42d3-a456-426614174000", PublicKey: "public", ShortID: "0123456789abcdef", Flow: domain.VisionFlow}
	if err := validateLandingPeer(peer); err != nil {
		t.Fatalf("192.168/16 should be allowed for relay testing: %v", err)
	}
	peer.Server = "10.0.0.20"
	if err := validateLandingPeer(peer); err == nil {
		t.Fatal("10/8 should remain blocked")
	}
	peer.Server = "172.16.0.20"
	if err := validateLandingPeer(peer); err == nil {
		t.Fatal("172.16/12 should remain blocked")
	}
}

func xrayConfigHasTag(root map[string]any, tag string) bool {
	for _, raw := range root["outbounds"].([]any) {
		if outbound, ok := raw.(map[string]any); ok && outbound["tag"] == tag {
			return true
		}
	}
	return false
}
