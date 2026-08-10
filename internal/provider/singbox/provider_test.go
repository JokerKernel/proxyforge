package singbox

import (
	"bytes"
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

func TestRenderedConfigsFollowOfficialFieldOrder(t *testing.T) {
	p := New()
	base := domain.NodeSpec{
		InboundTag: "singbox-one", Server: "203.0.113.10", Port: 443, SNI: "example.com", Target: "example.com:443",
		UserName: "one", UUID: "123e4567-e89b-42d3-a456-426614174000", PrivateKey: "private-key",
		PublicKey: "public-key", ShortID: "0123456789abcdef",
	}

	t.Run("standard server", func(t *testing.T) {
		config, err := p.RenderServer(base)
		if err != nil {
			t.Fatal(err)
		}
		assertSingBoxFieldsInOrder(t, config, `"log"`, `"dns"`, `"inbounds"`, `"outbounds"`, `"route"`)
		assertSingBoxFieldsInOrder(t, singBoxConfigSection(t, config, `"log"`, `"dns"`), `"level"`, `"timestamp"`)
		assertSingBoxVLESSInboundOrder(t, singBoxConfigSection(t, config, `"type": "vless"`, `"outbounds"`))
		assertSingBoxFieldsInOrder(t, singBoxConfigSection(t, config, `"outbounds"`, `"route"`), `"type": "direct"`, `"tag": "direct"`)
		route := singBoxConfigSection(t, config, `"route"`, "")
		assertSingBoxFieldsInOrder(t, route,
			`"rules"`, `"action": "resolve"`, `"server": "local"`, `"ip_is_private"`, `"ip_cidr"`, `"action": "reject"`,
			`"final": "direct"`, `"default_domain_resolver": "local"`,
		)
	})

	t.Run("simplified server", func(t *testing.T) {
		node := base
		node.SimplifiedConfig = true
		config, err := p.RenderServer(node)
		if err != nil {
			t.Fatal(err)
		}
		assertSingBoxFieldsInOrder(t, config, `"log"`, `"inbounds"`, `"outbounds"`, `"route"`)
		if bytes.Contains(config, []byte(`"dns"`)) {
			t.Fatalf("simplified config unexpectedly contains dns: %s", config)
		}
		assertSingBoxVLESSInboundOrder(t, singBoxConfigSection(t, config, `"type": "vless"`, `"outbounds"`))
		assertSingBoxFieldsInOrder(t, singBoxConfigSection(t, config, `"route"`, ""),
			`"rules"`, `"ip_is_private"`, `"action": "reject"`, `"final": "direct"`,
		)
	})

	t.Run("fallback guard server", func(t *testing.T) {
		node := base
		node.SingBoxFallbackGuard = true
		node.SingBoxFallbackPort = domain.DefaultSingBoxFallbackPort
		config, err := p.RenderServer(node)
		if err != nil {
			t.Fatal(err)
		}
		assertSingBoxFieldsInOrder(t, config, `"log"`, `"dns"`, `"inbounds"`, `"outbounds"`, `"route"`)
		fallback := singBoxConfigSection(t, config, `"type": "direct"`, `"type": "vless"`)
		assertSingBoxFieldsInOrder(t, fallback,
			`"type"`, `"tag": "singbox-fallback-in"`, `"listen": "127.0.0.1"`, `"listen_port": 61432`,
			`"network": "tcp"`, `"override_address": "example.com"`, `"override_port": 443`,
		)
		assertSingBoxVLESSInboundOrder(t, singBoxConfigSection(t, config, `"type": "vless"`, `"outbounds"`))
		route := singBoxConfigSection(t, config, `"route"`, "")
		assertSingBoxFieldsInOrder(t, route,
			`"inbound"`, `"action": "sniff"`,
			`"inbound"`, `"protocol"`, `"domain"`, `"action": "route"`, `"outbound": "direct"`,
			`"inbound"`, `"action": "reject"`,
		)
	})

	t.Run("client", func(t *testing.T) {
		config, err := p.RenderClient(base)
		if err != nil {
			t.Fatal(err)
		}
		assertSingBoxFieldsInOrder(t, config, `"log"`, `"inbounds"`, `"outbounds"`, `"route"`)
		mixed := singBoxConfigSection(t, config, `"type": "mixed"`, `"outbounds"`)
		assertSingBoxFieldsInOrder(t, mixed, `"type"`, `"tag"`, `"listen"`, `"listen_port"`)
		proxy := singBoxConfigSection(t, config, `"type": "vless"`, `"route"`)
		assertSingBoxFieldsInOrder(t, proxy,
			`"type"`, `"tag": "proxy"`, `"server"`, `"server_port"`, `"uuid"`, `"flow"`, `"tls"`,
		)
		assertSingBoxFieldsInOrder(t, proxy, `"enabled": true`, `"server_name"`, `"utls"`, `"reality"`)
		assertSingBoxFieldsInOrder(t, proxy, `"utls"`, `"enabled": true`, `"fingerprint"`)
		assertSingBoxFieldsInOrder(t, proxy, `"reality"`, `"enabled": true`, `"public_key"`, `"short_id"`)
	})
}

func assertSingBoxVLESSInboundOrder(t *testing.T, inbound []byte) {
	t.Helper()
	assertSingBoxFieldsInOrder(t, inbound, `"type"`, `"tag"`, `"listen"`, `"listen_port"`, `"users"`, `"tls"`)
	assertSingBoxFieldsInOrder(t, inbound, `"users"`, `"name"`, `"uuid"`, `"flow"`)
	assertSingBoxFieldsInOrder(t, inbound, `"tls"`, `"enabled"`, `"server_name"`, `"reality"`)
	assertSingBoxFieldsInOrder(t, inbound,
		`"reality"`, `"enabled"`, `"handshake"`, `"server"`, `"server_port"`, `"private_key"`, `"short_id"`,
	)
}

func singBoxConfigSection(t *testing.T, config []byte, start, end string) []byte {
	t.Helper()
	startAt := bytes.Index(config, []byte(start))
	if startAt < 0 {
		t.Fatalf("section start %s is missing: %s", start, config)
	}
	if end == "" {
		return config[startAt:]
	}
	endAt := bytes.Index(config[startAt+len(start):], []byte(end))
	if endAt < 0 {
		t.Fatalf("section end %s is missing: %s", end, config)
	}
	return config[startAt : startAt+len(start)+endAt]
}

func assertSingBoxFieldsInOrder(t *testing.T, config []byte, fields ...string) {
	t.Helper()
	remaining := config
	for _, field := range fields {
		at := bytes.Index(remaining, []byte(field))
		if at < 0 {
			t.Fatalf("field %s is missing or out of order: %s", field, config)
		}
		remaining = remaining[at+len(field):]
	}
}

func TestRenderFallbackGuardServerAndPatchEndpoint(t *testing.T) {
	p := New()
	old := domain.NodeSpec{
		InboundTag: "singbox-one", Server: "203.0.113.10", Port: 443, SNI: "speed.cloudflare.com",
		Target: "speed.cloudflare.com:443", UserName: "one", UUID: "old-uuid", PrivateKey: "old-private",
		PublicKey: "old-public", ShortID: "old-short", SingBoxFallbackGuard: true, SingBoxFallbackPort: domain.DefaultSingBoxFallbackPort,
	}
	config, err := p.RenderServer(old)
	if err != nil {
		t.Fatal(err)
	}
	assertFallbackGuardConfig(t, config, "speed.cloudflare.com", 443, "speed.cloudflare.com", false, "old-uuid", "old-private", "old-short")

	next := old
	next.SNI = "www.example.com"
	next.Target = "origin.example.com:8443"
	next.UUID, next.PrivateKey, next.PublicKey, next.ShortID = "new-uuid", "new-private", "new-public", "new-short"
	patched, err := p.PatchServer(config, old, next, true)
	if err != nil {
		t.Fatal(err)
	}
	assertFallbackGuardConfig(t, patched, "origin.example.com", 8443, "www.example.com", false, "new-uuid", "new-private", "new-short")
	assertSingBoxFieldsInOrder(t, patched, `"log"`, `"dns"`, `"inbounds"`, `"outbounds"`, `"route"`)
	fallback := singBoxConfigSection(t, patched, `"type": "direct"`, `"type": "vless"`)
	assertSingBoxFieldsInOrder(t, fallback,
		`"type"`, `"tag": "singbox-fallback-in"`, `"listen"`, `"listen_port"`, `"network"`, `"override_address"`, `"override_port"`,
	)
}

func TestFallbackGuardHTTPDomainSwitchAndPatch(t *testing.T) {
	p := New()
	old := domain.NodeSpec{
		InboundTag: "singbox-one", Server: "203.0.113.10", Port: 443, SNI: "old.example.com",
		Target: "old.example.com:443", UserName: "one", UUID: "old-uuid", PrivateKey: "old-private",
		PublicKey: "old-public", ShortID: "old-short", SingBoxFallbackGuard: true, SingBoxFallbackPort: domain.DefaultSingBoxFallbackPort,
		SingBoxFallbackHTTPDomain: true,
	}
	config, err := p.RenderServer(old)
	if err != nil {
		t.Fatal(err)
	}
	assertFallbackGuardConfig(t, config, "old.example.com", 443, "old.example.com", true, "old-uuid", "old-private", "old-short")

	next := old
	next.SNI = "new.example.com"
	next.Target = "new.example.com:443"
	patched, err := p.PatchServer(config, old, next, true)
	if err != nil {
		t.Fatal(err)
	}
	assertFallbackGuardConfig(t, patched, "new.example.com", 443, "new.example.com", true, "old-uuid", "old-private", "old-short")
}

func assertFallbackGuardConfig(t *testing.T, config []byte, targetHost string, targetPort int, allowedDomain string, restrictHTTP bool, uuid, privateKey, shortID string) {
	t.Helper()
	var root map[string]any
	if err := json.Unmarshal(config, &root); err != nil {
		t.Fatal(err)
	}
	inbounds := root["inbounds"].([]any)
	if len(inbounds) != 2 {
		t.Fatalf("inbounds=%#v", inbounds)
	}
	fallback := inbounds[0].(map[string]any)
	if fallback["type"] != "direct" || fallback["tag"] != fallbackGuardInboundTag || fallback["listen"] != "127.0.0.1" ||
		fallback["listen_port"] != float64(domain.DefaultSingBoxFallbackPort) || fallback["network"] != "tcp" || fallback["override_address"] != targetHost || fallback["override_port"] != float64(targetPort) {
		t.Fatalf("fallback inbound=%#v", fallback)
	}
	vless := inbounds[1].(map[string]any)
	user := vless["users"].([]any)[0].(map[string]any)
	tls := vless["tls"].(map[string]any)
	reality := tls["reality"].(map[string]any)
	handshake := reality["handshake"].(map[string]any)
	shortIDs := reality["short_id"].([]any)
	if user["uuid"] != uuid || user["flow"] != domain.VisionFlow || tls["server_name"] != allowedDomain ||
		handshake["server"] != "127.0.0.1" || handshake["server_port"] != float64(domain.DefaultSingBoxFallbackPort) || reality["private_key"] != privateKey ||
		len(shortIDs) != 1 || shortIDs[0] != shortID {
		t.Fatalf("vless inbound=%#v", vless)
	}
	rules := root["route"].(map[string]any)["rules"].([]any)
	if len(rules) != 7 || rules[0].(map[string]any)["action"] != "sniff" || rules[3].(map[string]any)["action"] != "reject" {
		t.Fatalf("route rules=%#v", rules)
	}
	allow := rules[1].(map[string]any)
	domains := allow["domain"].([]any)
	protocols := allow["protocol"].([]any)
	if allow["action"] != "route" || allow["outbound"] != "direct" || len(protocols) != 1 || protocols[0] != "tls" || len(domains) != 1 || domains[0] != allowedDomain {
		t.Fatalf("fallback allow rule=%#v", allow)
	}
	httpAllow := rules[2].(map[string]any)
	httpProtocols := httpAllow["protocol"].([]any)
	if httpAllow["action"] != "route" || httpAllow["outbound"] != "direct" || len(httpProtocols) != 1 || httpProtocols[0] != "http" {
		t.Fatalf("fallback HTTP allow rule=%#v", httpAllow)
	}
	httpDomains, hasHTTPDomains := httpAllow["domain"].([]any)
	if restrictHTTP && (!hasHTTPDomains || len(httpDomains) != 1 || httpDomains[0] != allowedDomain) {
		t.Fatalf("fallback HTTP domains=%#v", httpAllow["domain"])
	}
	if !restrictHTTP && hasHTTPDomains {
		t.Fatalf("fallback HTTP rule should not restrict domains: %#v", httpAllow)
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
	assertSingBoxFieldsInOrder(t, patched, `"log"`, `"inbounds"`)
	assertSingBoxFieldsInOrder(t, singBoxConfigSection(t, patched, `"log"`, `"inbounds"`),
		`"disabled"`, `"level"`, `"output"`, `"timestamp"`,
	)
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
	patched, err := p.PatchDNSProfile(config, "public-cloudflare")
	if err != nil {
		t.Fatal(err)
	}
	var root map[string]any
	if err := json.Unmarshal(patched, &root); err != nil {
		t.Fatal(err)
	}
	dns := root["dns"].(map[string]any)
	servers := dns["servers"].([]any)
	if len(servers) != 1 {
		t.Fatalf("dns servers=%#v", servers)
	}
	cloudflare := servers[0].(map[string]any)
	route := root["route"].(map[string]any)
	resolve := route["rules"].([]any)[0].(map[string]any)
	if cloudflare["type"] != "udp" || cloudflare["server"] != "1.1.1.1" || cloudflare["server_port"] != float64(53) ||
		dns["final"] != "cloudflare" || route["default_domain_resolver"] != "cloudflare" || resolve["server"] != "cloudflare" {
		t.Fatalf("dns=%#v route=%#v", dns, route)
	}
	if current, err := p.CurrentDNSProfile(patched); err != nil || current != "public-cloudflare" {
		t.Fatalf("patched current=%q error=%v", current, err)
	}
	assertSingBoxFieldsInOrder(t, patched, `"log"`, `"dns"`, `"inbounds"`, `"outbounds"`, `"route"`)
	assertSingBoxFieldsInOrder(t, singBoxConfigSection(t, patched, `"dns"`, `"inbounds"`),
		`"servers"`, `"type": "udp"`, `"tag": "cloudflare"`, `"server": "1.1.1.1"`, `"server_port": 53`, `"final": "cloudflare"`,
	)
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
	withPublicDNS, err := p.PatchDNSProfile(simplified, "public-google")
	if err != nil {
		t.Fatal(err)
	}
	var googleRoot map[string]any
	if err := json.Unmarshal(withPublicDNS, &googleRoot); err != nil {
		t.Fatal(err)
	}
	googleDNS := googleRoot["dns"].(map[string]any)
	googleServers := googleDNS["servers"].([]any)
	googleRoute := googleRoot["route"].(map[string]any)
	googleResolve := googleRoute["rules"].([]any)[0].(map[string]any)
	if len(googleServers) != 1 || googleServers[0].(map[string]any)["server"] != "8.8.8.8" || googleDNS["final"] != "google" ||
		googleRoute["default_domain_resolver"] != "google" || googleResolve["action"] != "resolve" || googleResolve["server"] != "google" {
		t.Fatalf("google dns=%#v route=%#v", googleDNS, googleRoute)
	}
	if current, err := p.CurrentDNSProfile(withPublicDNS); err != nil || current != "public-google" {
		t.Fatalf("google current=%q error=%v", current, err)
	}

	withDoH, err := p.PatchDNSProfile(simplified, "doh-google")
	if err != nil {
		t.Fatal(err)
	}
	var dohRoot map[string]any
	if err := json.Unmarshal(withDoH, &dohRoot); err != nil {
		t.Fatal(err)
	}
	dohDNS := dohRoot["dns"].(map[string]any)
	dohServers := dohDNS["servers"].([]any)
	dohRoute := dohRoot["route"].(map[string]any)
	if len(dohServers) != 2 || dohServers[0].(map[string]any)["type"] != "local" ||
		dohServers[1].(map[string]any)["server"] != "dns.google" || dohServers[1].(map[string]any)["type"] != "https" ||
		dohDNS["final"] != "google-doh" ||
		dohRoute["default_domain_resolver"] != "google-doh" || dohRoute["rules"].([]any)[0].(map[string]any)["server"] != "google-doh" {
		t.Fatalf("doh dns=%#v route=%#v", dohDNS, dohRoute)
	}
	googleDoH := dohServers[1].(map[string]any)
	googleTLS := googleDoH["tls"].(map[string]any)
	if googleDoH["domain_resolver"] != "bootstrap" || googleTLS["enabled"] != true || googleTLS["server_name"] != "dns.google" {
		t.Fatalf("google doh=%#v", googleDoH)
	}
	if current, err := p.CurrentDNSProfile(withDoH); err != nil || current != "doh-google" {
		t.Fatalf("doh current=%q error=%v", current, err)
	}
	assertSingBoxFieldsInOrder(t, withDoH, `"log"`, `"dns"`, `"inbounds"`, `"outbounds"`, `"route"`)
	dohServer := singBoxConfigSection(t, withDoH, `"type": "https"`, `"inbounds"`)
	assertSingBoxFieldsInOrder(t, dohServer,
		`"type"`, `"tag": "google-doh"`, `"server": "dns.google"`, `"server_port": 443`, `"path": "/dns-query"`,
		`"tls"`, `"enabled": true`, `"server_name": "dns.google"`, `"domain_resolver": "bootstrap"`,
	)
	backToSystem, err := p.PatchDNSProfile(withDoH, "system")
	if err != nil {
		t.Fatal(err)
	}
	var systemRoot map[string]any
	if err := json.Unmarshal(backToSystem, &systemRoot); err != nil {
		t.Fatal(err)
	}
	if servers := systemRoot["dns"].(map[string]any)["servers"].([]any); len(servers) != 1 || servers[0].(map[string]any)["tag"] != "local" {
		t.Fatalf("system servers=%#v", servers)
	}
	if current, err := p.CurrentDNSProfile(backToSystem); err != nil || current != "system" {
		t.Fatalf("system current=%q error=%v", current, err)
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
