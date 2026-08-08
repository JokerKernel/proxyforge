package xray

import (
	"context"
	"encoding/json"
	"os"
	"reflect"
	"testing"

	"proxyforge/internal/domain"
)

type outputRunner struct{ output string }

func (r outputRunner) Run(context.Context, string, ...string) ([]byte, error) {
	return []byte(r.output), nil
}

func TestGoldenConfigs(t *testing.T) {
	p := New()
	n := domain.NodeSpec{InboundTag: domain.DefaultInboundTag(domain.CoreXray), Server: "203.0.113.10", Port: 443, SNI: "example.com", Target: "example.com:443", UserName: domain.DefaultUserName, UUID: "123e4567-e89b-42d3-a456-426614174000", PrivateKey: "private-key", PublicKey: "public-key", ShortID: "0123456789abcdef"}
	tests := []struct {
		name, file string
		render     func(domain.NodeSpec) ([]byte, error)
	}{{"server", "testdata/server.json", p.RenderServer}, {"client", "testdata/client.json", p.RenderClient}}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tt.render(n)
			if err != nil {
				t.Fatal(err)
			}
			want, err := os.ReadFile(tt.file)
			if err != nil {
				t.Fatal(err)
			}
			assertJSONEqual(t, want, got)
		})
	}
}

func TestRenderedConfigsExplicitlyUseSystemDNS(t *testing.T) {
	p := New()
	n := domain.NodeSpec{
		InboundTag: "xray-one", Server: "203.0.113.10", Port: 443, SNI: "example.com", Target: "example.com:443",
		UserName: "one", UUID: "123e4567-e89b-42d3-a456-426614174000", PrivateKey: "private", PublicKey: "public", ShortID: "0123456789abcdef",
	}
	for _, render := range []func(domain.NodeSpec) ([]byte, error){p.RenderServer, p.RenderClient} {
		config, err := render(n)
		if err != nil {
			t.Fatal(err)
		}
		var root map[string]any
		if err := json.Unmarshal(config, &root); err != nil {
			t.Fatal(err)
		}
		dns, ok := root["dns"].(map[string]any)
		if !ok || dns["queryStrategy"] != "UseIP" {
			t.Fatalf("dns=%#v", root["dns"])
		}
		servers, ok := dns["servers"].([]any)
		if !ok || len(servers) != 1 || servers[0] != "localhost" {
			t.Fatalf("dns servers=%#v", dns["servers"])
		}
	}
}

func TestPatchServerPreservesManualConfiguration(t *testing.T) {
	p := New()
	old := domain.NodeSpec{
		InboundTag: "managed-in", SNI: "old.example.com", Target: "old.example.com:443", UserName: "managed-user",
		UUID: "old-uuid", PrivateKey: "old-private", PublicKey: "old-public", ShortID: "old-short",
	}
	config, err := p.RenderServer(old)
	if err != nil {
		t.Fatal(err)
	}
	var root map[string]any
	if err := json.Unmarshal(config, &root); err != nil {
		t.Fatal(err)
	}
	root["manual_top_level"] = map[string]any{"keep": true}
	inbound := root["inbounds"].([]any)[0].(map[string]any)
	inbound["manual_inbound"] = "keep"
	settings := inbound["settings"].(map[string]any)
	clients := settings["clients"].([]any)
	clients = append(clients, map[string]any{"email": "other-user", "id": "other-uuid", "flow": "other-flow"})
	settings["clients"] = clients
	stream := inbound["streamSettings"].(map[string]any)
	reality := stream["realitySettings"].(map[string]any)
	reality["shortIds"] = append(reality["shortIds"].([]any), "other-short")
	reality["serverNames"] = append(reality["serverNames"].([]any), "other.example.com")
	root["outbounds"] = append(root["outbounds"].([]any), map[string]any{"protocol": "freedom", "tag": "manual-out"})
	modified, err := json.Marshal(root)
	if err != nil {
		t.Fatal(err)
	}
	next := old
	next.SNI, next.Target = "new.example.com", "origin.example.com:8443"
	next.UUID, next.PrivateKey, next.PublicKey, next.ShortID = "new-uuid", "new-private", "new-public", "new-short"
	patched, err := p.PatchServer(modified, old, next, true)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(patched, &got); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got["manual_top_level"], root["manual_top_level"]) || len(got["outbounds"].([]any)) != 3 {
		t.Fatalf("manual top-level configuration changed: %s", patched)
	}
	gotInbound := got["inbounds"].([]any)[0].(map[string]any)
	if gotInbound["manual_inbound"] != "keep" {
		t.Fatalf("manual inbound changed: %s", patched)
	}
	gotSettings := gotInbound["settings"].(map[string]any)
	gotClients := gotSettings["clients"].([]any)
	gotReality := gotInbound["streamSettings"].(map[string]any)["realitySettings"].(map[string]any)
	shortIDs := gotReality["shortIds"].([]any)
	serverNames := gotReality["serverNames"].([]any)
	if len(gotClients) != 2 || gotClients[0].(map[string]any)["id"] != "new-uuid" || gotClients[1].(map[string]any)["id"] != "other-uuid" ||
		gotReality["privateKey"] != "new-private" || shortIDs[0] != "new-short" || shortIDs[1] != "other-short" ||
		serverNames[0] != "new.example.com" || serverNames[1] != "other.example.com" || gotReality["target"] != "origin.example.com:8443" {
		t.Fatalf("managed fields were not patched correctly: %s", patched)
	}
}

func TestPatchLogLevelPreservesOtherSettings(t *testing.T) {
	p := New()
	config := []byte(`{
  "log": {"loglevel": "warning", "dnsLog": true, "maskAddress": "half"},
  "inbounds": [],
  "manual": {"keep": true}
}`)
	patched, err := p.PatchLogLevel(config, "debug")
	if err != nil {
		t.Fatal(err)
	}
	var root map[string]any
	if err := json.Unmarshal(patched, &root); err != nil {
		t.Fatal(err)
	}
	log := root["log"].(map[string]any)
	if log["loglevel"] != "debug" || log["dnsLog"] != true || log["maskAddress"] != "half" {
		t.Fatalf("patched log=%#v", log)
	}
	if root["manual"].(map[string]any)["keep"] != true {
		t.Fatalf("manual settings changed: %s", patched)
	}

	disabled, err := p.PatchLogLevel(patched, "off")
	if err != nil {
		t.Fatal(err)
	}
	var disabledRoot map[string]any
	if err := json.Unmarshal(disabled, &disabledRoot); err != nil {
		t.Fatal(err)
	}
	if got := disabledRoot["log"].(map[string]any)["loglevel"]; got != "none" {
		t.Fatalf("disabled loglevel=%v", got)
	}
	if current, err := p.CurrentLogLevel(disabled); err != nil || current != "off" {
		t.Fatalf("disabled current=%q error=%v", current, err)
	}
}

func TestGenerateKeyPairParsesSupportedNativeOutputs(t *testing.T) {
	tests := []struct {
		name, output string
	}{
		{"legacy", "Private key: private\nPublic key: public\n"},
		{"password", "PrivateKey: private\nPassword: public\nHash32: unused\n"},
		{"password with public key label", "PrivateKey: private\nPassword (PublicKey): public\nHash32: unused\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pair, err := New().GenerateKeyPair(context.Background(), outputRunner{tt.output})
			if err != nil {
				t.Fatal(err)
			}
			if pair.Private != "private" || pair.Public != "public" {
				t.Fatalf("pair=%#v", pair)
			}
		})
	}
}

func TestGenerateKeyPairDoesNotTreatHash32AsPublicKey(t *testing.T) {
	pair, err := New().GenerateKeyPair(context.Background(), outputRunner{"PrivateKey: private\nHash32: hash\n"})
	if err == nil {
		t.Fatalf("pair=%#v, expected missing public key error", pair)
	}
}

func assertJSONEqual(t *testing.T, want, got []byte) {
	t.Helper()
	var a, b any
	if err := json.Unmarshal(want, &a); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(got, &b); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(a, b) {
		t.Fatalf("JSON mismatch\nwant: %s\ngot: %s", want, got)
	}
}
