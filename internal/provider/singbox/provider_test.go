package singbox

import (
	"context"
	"encoding/json"
	"os"
	"reflect"
	"slices"
	"testing"

	"proxyforge/internal/domain"
)

type outputRunner struct{ output string }

func (r outputRunner) Run(context.Context, string, ...string) ([]byte, error) {
	return []byte(r.output), nil
}

func TestGoldenConfigs(t *testing.T) {
	p := New()
	n := domain.NodeSpec{InboundTag: domain.DefaultInboundTag(domain.CoreSingBox), Server: "203.0.113.10", Port: 443, SNI: "example.com", Target: "example.com:443", UserName: domain.DefaultUserName, UUID: "123e4567-e89b-42d3-a456-426614174000", PrivateKey: "private-key", PublicKey: "public-key", ShortID: "0123456789abcdef"}
	tests := []struct {
		name, file string
		render     func(domain.NodeSpec) ([]byte, error)
	}{
		{"server", "testdata/server.json", p.RenderServer},
		{"simplified server", "testdata/server_simplified.json", func(n domain.NodeSpec) ([]byte, error) {
			n.SimplifiedConfig = true
			return p.RenderServer(n)
		}},
		{"client", "testdata/client.json", p.RenderClient},
	}
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
	users := inbound["users"].([]any)
	users = append(users, map[string]any{"name": "other-user", "uuid": "other-uuid", "flow": "other-flow"})
	inbound["users"] = users
	tls := inbound["tls"].(map[string]any)
	reality := tls["reality"].(map[string]any)
	reality["short_id"] = append(reality["short_id"].([]any), "other-short")
	root["outbounds"] = append(root["outbounds"].([]any), map[string]any{"type": "direct", "tag": "manual-out"})
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
	if !reflect.DeepEqual(got["manual_top_level"], root["manual_top_level"]) || len(got["outbounds"].([]any)) != 2 {
		t.Fatalf("manual top-level configuration changed: %s", patched)
	}
	gotInbound := got["inbounds"].([]any)[0].(map[string]any)
	if gotInbound["manual_inbound"] != "keep" || len(gotInbound["users"].([]any)) != 2 {
		t.Fatalf("manual inbound/users changed: %s", patched)
	}
	managedUser := gotInbound["users"].([]any)[0].(map[string]any)
	otherUser := gotInbound["users"].([]any)[1].(map[string]any)
	gotTLS := gotInbound["tls"].(map[string]any)
	gotReality := gotTLS["reality"].(map[string]any)
	shortIDs := gotReality["short_id"].([]any)
	handshake := gotReality["handshake"].(map[string]any)
	if managedUser["uuid"] != "new-uuid" || otherUser["uuid"] != "other-uuid" || gotReality["private_key"] != "new-private" ||
		shortIDs[0] != "new-short" || shortIDs[1] != "other-short" || gotTLS["server_name"] != "new.example.com" ||
		handshake["server"] != "origin.example.com" || handshake["server_port"] != float64(8443) {
		t.Fatalf("managed fields were not patched correctly: %s", patched)
	}
}

func TestPatchLogLevelPreservesOtherSettings(t *testing.T) {
	p := New()
	config := []byte(`{
  "log": {"level": "info", "timestamp": true, "output": "custom.log"},
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
	if log["level"] != "debug" || log["disabled"] != false || log["timestamp"] != true || log["output"] != "custom.log" {
		t.Fatalf("patched log=%#v", log)
	}
	if root["manual"].(map[string]any)["keep"] != true {
		t.Fatalf("manual settings changed: %s", patched)
	}
	if current, err := p.CurrentLogLevel(patched); err != nil || current != "debug" {
		t.Fatalf("current=%q error=%v", current, err)
	}

	disabled, err := p.PatchLogLevel(patched, "off")
	if err != nil {
		t.Fatal(err)
	}
	if current, err := p.CurrentLogLevel(disabled); err != nil || current != "off" {
		t.Fatalf("disabled current=%q error=%v", current, err)
	}
}

func TestPatchDNSProfileUpdatesResolverReferences(t *testing.T) {
	p := New()
	config, err := p.RenderServer(domain.NodeSpec{
		InboundTag: "singbox-one", Port: 443, SNI: "example.com", Target: "example.com:443", UserName: "one",
		UUID: "123e4567-e89b-42d3-a456-426614174000", PrivateKey: "private", ShortID: "0123456789abcdef",
	})
	if err != nil {
		t.Fatal(err)
	}
	if current, err := p.CurrentDNSProfile(config); err != nil || current != "system" {
		t.Fatalf("current=%q error=%v", current, err)
	}
	var source map[string]any
	if err := json.Unmarshal(config, &source); err != nil {
		t.Fatal(err)
	}
	sourceRoute := source["route"].(map[string]any)
	sourceRoute["rules"] = append(sourceRoute["rules"].([]any), map[string]any{"action": "resolve", "server": "local", "domain_suffix": []any{"example.net"}})
	config, err = json.Marshal(source)
	if err != nil {
		t.Fatal(err)
	}
	patched, err := p.PatchDNSProfile(config, "cloudflare")
	if err != nil {
		t.Fatal(err)
	}
	var root map[string]any
	if err := json.Unmarshal(patched, &root); err != nil {
		t.Fatal(err)
	}
	dns := root["dns"].(map[string]any)
	server := dns["servers"].([]any)[0].(map[string]any)
	route := root["route"].(map[string]any)
	resolve := route["rules"].([]any)[0].(map[string]any)
	if server["type"] != "udp" || server["server"] != "1.1.1.1" || server["server_port"] != float64(53) ||
		dns["final"] != "cloudflare" || route["default_domain_resolver"] != "cloudflare" || resolve["server"] != "cloudflare" {
		t.Fatalf("dns=%#v route=%#v", dns, route)
	}
	if current, err := p.CurrentDNSProfile(patched); err != nil || current != "cloudflare" {
		t.Fatalf("patched current=%q error=%v", current, err)
	}
	for _, rawRule := range route["rules"].([]any) {
		rule := rawRule.(map[string]any)
		if rule["action"] == "resolve" && rule["server"] != "cloudflare" {
			t.Fatalf("resolve rule still references old DNS: %#v", rule)
		}
	}

	simplified, err := p.RenderServer(domain.NodeSpec{SimplifiedConfig: true})
	if err != nil {
		t.Fatal(err)
	}
	if current, err := p.CurrentDNSProfile(simplified); err != nil || current != "none" {
		t.Fatalf("simplified current=%q error=%v", current, err)
	}
	withGoogle, err := p.PatchDNSProfile(simplified, "google")
	if err != nil {
		t.Fatal(err)
	}
	var googleRoot map[string]any
	if err := json.Unmarshal(withGoogle, &googleRoot); err != nil {
		t.Fatal(err)
	}
	googleRoute := googleRoot["route"].(map[string]any)
	if action := googleRoute["rules"].([]any)[0].(map[string]any)["action"]; action != "resolve" {
		t.Fatalf("first route action=%v", action)
	}
}

func TestGenerateKeyPairParsesNativeOutput(t *testing.T) {
	pair, err := New().GenerateKeyPair(context.Background(), outputRunner{"PrivateKey: private\nPublicKey: public\n"})
	if err != nil {
		t.Fatal(err)
	}
	if pair.Private != "private" || pair.Public != "public" {
		t.Fatalf("pair=%#v", pair)
	}
}

func TestScriptHostsIncludeOfficialRedirect(t *testing.T) {
	hosts := New().ScriptHosts()
	if !slices.Contains(hosts, "sing-box.sagernet.org") {
		t.Fatalf("official redirect host missing from whitelist: %v", hosts)
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
