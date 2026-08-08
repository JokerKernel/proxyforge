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

func TestGenerateKeyPairParsesCurrentNativeOutput(t *testing.T) {
	pair, err := New().GenerateKeyPair(context.Background(), outputRunner{"PrivateKey: private\nPassword: public\nHash32: unused\n"})
	if err != nil {
		t.Fatal(err)
	}
	if pair.Private != "private" || pair.Public != "public" {
		t.Fatalf("pair=%#v", pair)
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
