package xray

import (
	"bytes"
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

func TestRenderedRealityStreamFieldOrder(t *testing.T) {
	p := New()
	n := domain.NodeSpec{
		InboundTag: "xray-one", Server: "203.0.113.10", Port: 443, SNI: "example.com", Target: "example.com:443",
		UserName: "one", UUID: "123e4567-e89b-42d3-a456-426614174000", PrivateKey: "private", PublicKey: "public",
		ShortID: "0123456789abcdef", XrayFallbackPort: 61431,
	}
	renders := []struct {
		name   string
		render func() ([]byte, error)
	}{
		{"server", func() ([]byte, error) { return p.RenderServer(n) }},
		{"fallback guard server", func() ([]byte, error) {
			guard := n
			guard.XrayFallbackGuard = true
			return p.RenderServer(guard)
		}},
		{"client", func() ([]byte, error) { return p.RenderClient(n) }},
	}
	for _, tt := range renders {
		t.Run(tt.name, func(t *testing.T) {
			config, err := tt.render()
			if err != nil {
				t.Fatal(err)
			}
			networkAt := bytes.Index(config, []byte(`"network": "raw"`))
			securityAt := bytes.Index(config, []byte(`"security": "reality"`))
			realityAt := bytes.Index(config, []byte(`"realitySettings"`))
			if networkAt < 0 || securityAt < networkAt || realityAt < securityAt {
				t.Fatalf("unexpected streamSettings field order: %s", config)
			}
		})
	}
}

func TestFallbackGuardServerFollowsOfficialExampleFieldOrder(t *testing.T) {
	p := New()
	config, err := p.RenderServer(domain.NodeSpec{
		InboundTag: "xray-one", Server: "203.0.113.10", Port: 443, SNI: "speed.cloudflare.com", Target: "speed.cloudflare.com:443",
		UserName: "one", UUID: "123e4567-e89b-42d3-a456-426614174000", PrivateKey: "private", ShortID: "0123456789abcdef",
		XrayFallbackGuard: true, XrayFallbackPort: 61431,
	})
	if err != nil {
		t.Fatal(err)
	}
	assertFieldsInOrder(t, config,
		`"log"`, `"dns"`, `"inbounds"`, `"outbounds"`, `"routing"`,
	)

	dokodemoStart := bytes.Index(config, []byte(`"listen": "127.0.0.1"`))
	vlessStart := bytes.Index(config, []byte(`"listen": "0.0.0.0"`))
	if dokodemoStart < 0 || vlessStart <= dokodemoStart {
		t.Fatalf("cannot locate ordered inbounds: %s", config)
	}
	dokodemo := config[dokodemoStart:vlessStart]
	assertFieldsInOrder(t, dokodemo,
		`"listen"`, `"port": 61431`, `"protocol": "dokodemo-door"`, `"settings"`, `"tag": "dokodemo-in"`, `"sniffing"`,
	)
	assertFieldsInOrder(t, dokodemo,
		`"address": "speed.cloudflare.com"`, `"port": 443`, `"network": "tcp"`,
	)
	assertFieldsInOrder(t, dokodemo,
		`"enabled": true`, `"destOverride"`, `"routeOnly": true`,
	)

	vless := config[vlessStart:]
	assertFieldsInOrder(t, vless,
		`"listen"`, `"port": 443`, `"protocol": "vless"`, `"settings"`, `"streamSettings"`, `"tag": "xray-one"`, `"sniffing"`,
	)
	assertFieldsInOrder(t, vless,
		`"show": false`, `"target": "127.0.0.1:61431"`, `"xver": 0`, `"serverNames"`, `"privateKey"`, `"shortIds"`,
	)
}

func TestStandardServerFollowsOfficialFieldOrder(t *testing.T) {
	p := New()
	config, err := p.RenderServer(domain.NodeSpec{
		InboundTag: "xray-one", Server: "203.0.113.10", Port: 443, SNI: "example.com", Target: "example.com:443",
		UserName: "one", UUID: "123e4567-e89b-42d3-a456-426614174000", PrivateKey: "private", ShortID: "0123456789abcdef",
	})
	if err != nil {
		t.Fatal(err)
	}
	assertFieldsInOrder(t, config, `"log"`, `"dns"`, `"inbounds"`, `"outbounds"`, `"routing"`)

	inbound := xrayConfigSection(t, config, `"listen": "0.0.0.0"`, `"outbounds"`)
	assertFieldsInOrder(t, inbound,
		`"listen"`, `"port": 443`, `"protocol": "vless"`, `"settings"`, `"streamSettings"`, `"tag": "xray-one"`,
	)
	assertFieldsInOrder(t, inbound, `"clients"`, `"id"`, `"email"`, `"flow"`, `"decryption"`)
	assertFieldsInOrder(t, inbound, `"network": "raw"`, `"security": "reality"`, `"realitySettings"`)
	assertFieldsInOrder(t, inbound,
		`"show": false`, `"target": "example.com:443"`, `"xver": 0`, `"serverNames"`, `"privateKey"`, `"shortIds"`,
	)

	direct := xrayConfigSection(t, config, `"protocol": "freedom"`, `"protocol": "blackhole"`)
	assertFieldsInOrder(t, direct, `"protocol"`, `"settings"`, `"tag": "direct"`, `"streamSettings"`)
	assertFieldsInOrder(t, direct, `"domainStrategy": "AsIs"`, `"finalRules"`, `"action": "allow"`)
	assertFieldsInOrder(t, direct, `"sockopt"`, `"domainStrategy": "UseIP"`, `"happyEyeballs"`, `"tryDelayMs"`)
	routing := xrayConfigSection(t, config, `"routing"`, "")
	assertFieldsInOrder(t, routing, `"domainStrategy"`, `"rules"`, `"ip"`, `"outboundTag"`)
	if bytes.Contains(routing, []byte(`"type"`)) {
		t.Fatalf("routing rule unexpectedly contains type: %s", routing)
	}
}

func TestClientFollowsOfficialFieldOrder(t *testing.T) {
	p := New()
	config, err := p.RenderClient(domain.NodeSpec{
		Server: "203.0.113.10", Port: 443, SNI: "example.com", UUID: "123e4567-e89b-42d3-a456-426614174000",
		PublicKey: "public", ShortID: "0123456789abcdef",
	})
	if err != nil {
		t.Fatal(err)
	}
	assertFieldsInOrder(t, config, `"log"`, `"dns"`, `"inbounds"`, `"outbounds"`, `"routing"`)
	assertFieldsInOrder(t, config,
		`"listen": "127.0.0.1"`, `"port": 10808`, `"protocol": "socks"`, `"settings"`,
		`"port": 10809`, `"protocol": "http"`, `"settings"`,
	)

	proxy := xrayConfigSection(t, config, `"protocol": "vless"`, `"protocol": "blackhole"`)
	assertFieldsInOrder(t, proxy, `"protocol"`, `"settings"`, `"tag": "proxy"`, `"streamSettings"`)
	assertFieldsInOrder(t, proxy, `"vnext"`, `"address"`, `"port": 443`, `"users"`, `"id"`, `"encryption"`, `"flow"`)
	assertFieldsInOrder(t, proxy, `"network": "raw"`, `"security": "reality"`, `"realitySettings"`)
	assertFieldsInOrder(t, proxy, `"serverName"`, `"fingerprint"`, `"password"`, `"shortId"`, `"spiderX"`)
}

func xrayConfigSection(t *testing.T, config []byte, start, end string) []byte {
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

func assertFieldsInOrder(t *testing.T, config []byte, fields ...string) {
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
		InboundTag: "xray-one", Server: "203.0.113.10", Port: 443, SNI: "speed.cloudflare.com",
		Target: "speed.cloudflare.com:443", UserName: "one", UUID: "old-uuid", PrivateKey: "old-private",
		PublicKey: "old-public", ShortID: "old-short", XrayFallbackGuard: true, XrayFallbackPort: 61431,
	}
	config, err := p.RenderServer(old)
	if err != nil {
		t.Fatal(err)
	}
	assertFallbackGuardConfig(t, config, "speed.cloudflare.com", 443, "127.0.0.1:61431", "old-uuid", "old-private", "old-short")

	next := old
	next.SNI = "www.example.com"
	next.Target = "origin.example.com:8443"
	next.UUID, next.PrivateKey, next.PublicKey, next.ShortID = "new-uuid", "new-private", "new-public", "new-short"
	patched, err := p.PatchServer(config, old, next, true)
	if err != nil {
		t.Fatal(err)
	}
	assertFallbackGuardConfig(t, patched, "origin.example.com", 8443, "127.0.0.1:61431", "new-uuid", "new-private", "new-short")
	assertFieldsInOrder(t, patched, `"log"`, `"dns"`, `"inbounds"`, `"outbounds"`, `"routing"`)
	dokodemoStart := bytes.Index(patched, []byte(`"listen": "127.0.0.1"`))
	vlessStart := bytes.Index(patched, []byte(`"listen": "0.0.0.0"`))
	if dokodemoStart < 0 || vlessStart <= dokodemoStart {
		t.Fatalf("cannot locate patched ordered inbounds: %s", patched)
	}
	assertFieldsInOrder(t, patched[dokodemoStart:vlessStart],
		`"listen"`, `"port": 61431`, `"protocol": "dokodemo-door"`, `"settings"`, `"tag": "dokodemo-in"`, `"sniffing"`,
	)
	var root map[string]any
	if err := json.Unmarshal(patched, &root); err != nil {
		t.Fatal(err)
	}
	rules := root["routing"].(map[string]any)["rules"].([]any)
	domains := rules[0].(map[string]any)["domain"].([]any)
	if len(domains) != 1 || domains[0] != "www.example.com" {
		t.Fatalf("fallback allow domains=%#v", domains)
	}
}

func assertFallbackGuardConfig(t *testing.T, config []byte, targetHost string, targetPort int, realityTarget, uuid, privateKey, shortID string) {
	t.Helper()
	var root map[string]any
	if err := json.Unmarshal(config, &root); err != nil {
		t.Fatal(err)
	}
	inbounds := root["inbounds"].([]any)
	if len(inbounds) != 2 {
		t.Fatalf("inbounds=%#v", inbounds)
	}
	dokodemo := inbounds[0].(map[string]any)
	settings := dokodemo["settings"].(map[string]any)
	sniffing := dokodemo["sniffing"].(map[string]any)
	if dokodemo["tag"] != fallbackGuardInboundTag || dokodemo["listen"] != "127.0.0.1" || dokodemo["protocol"] != "dokodemo-door" ||
		settings["address"] != targetHost || settings["port"] != float64(targetPort) || settings["network"] != "tcp" ||
		sniffing["routeOnly"] != true {
		t.Fatalf("dokodemo inbound=%#v", dokodemo)
	}
	vless := inbounds[1].(map[string]any)
	client := vless["settings"].(map[string]any)["clients"].([]any)[0].(map[string]any)
	reality := vless["streamSettings"].(map[string]any)["realitySettings"].(map[string]any)
	shortIDs := reality["shortIds"].([]any)
	if client["id"] != uuid || client["flow"] != domain.VisionFlow || reality["target"] != realityTarget ||
		reality["privateKey"] != privateKey || len(shortIDs) != 1 || shortIDs[0] != shortID || vless["sniffing"].(map[string]any)["routeOnly"] != true {
		t.Fatalf("vless inbound=%#v", vless)
	}
	rules := root["routing"].(map[string]any)["rules"].([]any)
	if len(rules) != 3 || rules[0].(map[string]any)["outboundTag"] != fallbackDirectOutboundTag || rules[1].(map[string]any)["outboundTag"] != "blocked-private" {
		t.Fatalf("routing rules=%#v", rules)
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

func TestPatchDNSProfilePreservesOtherDNSSettings(t *testing.T) {
	p := New()
	config, err := p.RenderServer(domain.NodeSpec{})
	if err != nil {
		t.Fatal(err)
	}
	if current, err := p.CurrentDNSProfile(config); err != nil || current != "system" {
		t.Fatalf("current=%q error=%v", current, err)
	}
	if servers, err := p.CurrentDNSServers(config); err != nil || len(servers) != 1 || servers[0] != "localhost" {
		t.Fatalf("system servers=%v error=%v", servers, err)
	}
	var root map[string]any
	if err := json.Unmarshal(config, &root); err != nil {
		t.Fatal(err)
	}
	dns := root["dns"].(map[string]any)
	dns["disableCache"] = true
	modified, err := json.Marshal(root)
	if err != nil {
		t.Fatal(err)
	}
	patched, err := p.PatchDNSProfile(modified, "public-google")
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(patched, &got); err != nil {
		t.Fatal(err)
	}
	gotDNS := got["dns"].(map[string]any)
	servers := gotDNS["servers"].([]any)
	if len(servers) != 2 || servers[0] != "8.8.8.8" || servers[1] != "1.1.1.1" || gotDNS["queryStrategy"] != "UseIP" || gotDNS["disableCache"] != true {
		t.Fatalf("dns=%#v", gotDNS)
	}
	if current, err := p.CurrentDNSProfile(patched); err != nil || current != "public-google" {
		t.Fatalf("patched current=%q error=%v", current, err)
	}
	doh, err := p.PatchDNSProfile(patched, "doh-cloudflare")
	if err != nil {
		t.Fatal(err)
	}
	var dohRoot map[string]any
	if err := json.Unmarshal(doh, &dohRoot); err != nil {
		t.Fatal(err)
	}
	dohServers := dohRoot["dns"].(map[string]any)["servers"].([]any)
	if len(dohServers) != 2 || dohServers[0] != "https+local://1.1.1.1/dns-query" || dohServers[1] != "https+local://8.8.8.8/dns-query" {
		t.Fatalf("doh servers=%#v", dohServers)
	}
	if current, err := p.CurrentDNSProfile(doh); err != nil || current != "doh-cloudflare" {
		t.Fatalf("doh current=%q error=%v", current, err)
	}
	if servers, err := p.CurrentDNSServers(doh); err != nil || len(servers) != 2 || servers[0] != "1.1.1.1" || servers[1] != "8.8.8.8" {
		t.Fatalf("doh display servers=%v error=%v", servers, err)
	}
	gotDNS["queryStrategy"] = "UseIPv4"
	misconfigured, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	if current, err := p.CurrentDNSProfile(misconfigured); err != nil || current != "custom" {
		t.Fatalf("misconfigured current=%q error=%v", current, err)
	}
	if current, err := p.CurrentDNSProfile([]byte(`{"inbounds":[]}`)); err != nil || current != "implicit-system" {
		t.Fatalf("implicit current=%q error=%v", current, err)
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
