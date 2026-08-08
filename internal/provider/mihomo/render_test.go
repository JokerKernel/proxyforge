package mihomo

import (
	"strings"
	"testing"

	"proxyforge/internal/domain"
)

func TestRenderClient(t *testing.T) {
	n := domain.NodeSpec{
		Core: domain.CoreSingBox, Server: "server.example.com", Port: 443,
		SNI: "www.example.com", UUID: "123e4567-e89b-42d3-a456-426614174000",
		PrivateKey: "must-not-leak", PublicKey: "public-key", ShortID: "0123456789abcdef",
	}
	b, err := RenderClient(n)
	if err != nil {
		t.Fatal(err)
	}
	config := string(b)
	for _, want := range []string{
		"mixed-port: 7890", `type: vless`, `server: "server.example.com"`,
		"port: 443", `uuid: "123e4567-e89b-42d3-a456-426614174000"`,
		"flow: xtls-rprx-vision", "packet-encoding: xudp", "client-fingerprint: chrome",
		`public-key: "public-key"`, `short-id: "0123456789abcdef"`, "- MATCH,PROXY",
	} {
		if !strings.Contains(config, want) {
			t.Errorf("config missing %q:\n%s", want, config)
		}
	}
	if strings.Contains(config, n.PrivateKey) {
		t.Fatal("client configuration leaked the REALITY private key")
	}
}

func TestRenderClientRejectsIncompleteNode(t *testing.T) {
	if _, err := RenderClient(domain.NodeSpec{Core: domain.CoreXray, Port: 443}); err == nil {
		t.Fatal("expected incomplete node error")
	}
}
