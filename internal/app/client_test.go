package app

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"proxyforge/internal/domain"
	"proxyforge/internal/provider/singbox"
	"proxyforge/internal/provider/xray"
	"proxyforge/internal/system"
)

func TestClientOutputPermissionsAndForce(t *testing.T) {
	r := &fakeRunner{}
	a, _ := testApp(t, r)
	n := domain.NodeSpec{ManagedBy: "proxyforge", Core: domain.CoreSingBox, Server: "server.example.com", Port: 443, SNI: "www.example.com", UUID: "123e4567-e89b-42d3-a456-426614174000", PublicKey: "public", ShortID: "0123456789abcdef"}
	if err := a.Store.Save(n); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "client.json")
	if _, err := a.Client(context.Background(), domain.CoreSingBox, path, false); err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0600 {
		t.Fatalf("mode=%o", info.Mode().Perm())
	}
	if _, err := a.Client(context.Background(), domain.CoreSingBox, path, false); err == nil {
		t.Fatal("expected existing-file refusal")
	}
	if _, err := a.Client(context.Background(), domain.CoreSingBox, path, true); err != nil {
		t.Fatal(err)
	}
}

func TestServerConfigReadsCurrentActiveFile(t *testing.T) {
	r := &fakeRunner{}
	a, _ := testApp(t, r)
	path := a.Layout.Resolve(singbox.New().ConfigPath())
	want := []byte("{\"private_key\":\"server-secret\"}\n")
	if err := system.AtomicWrite(path, want, 0600); err != nil {
		t.Fatal(err)
	}
	got, err := a.ServerConfig(domain.CoreSingBox)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("server config=%q, want %q", got, want)
	}
	if _, err := a.ServerConfig(domain.CoreXray); err == nil || !strings.Contains(err.Error(), "尚未找到") {
		t.Fatalf("missing config error=%v", err)
	}
	gotPath, err := a.ServerConfigPath(domain.CoreSingBox)
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != path {
		t.Fatalf("server config path=%q, want %q", gotPath, path)
	}
	if _, err := a.ServerConfigPath(domain.CoreXray); err == nil || !strings.Contains(err.Error(), "尚未找到") {
		t.Fatalf("missing config path error=%v", err)
	}
}

func TestValidateServerConfigUsesNativeCoreValidator(t *testing.T) {
	r := &fakeRunner{}
	a, _ := testApp(t, r)
	for _, tt := range []struct {
		core string
		path string
		call string
	}{
		{core: domain.CoreSingBox, path: singbox.New().ConfigPath(), call: "sing-box check -c "},
		{core: domain.CoreXray, path: xray.New().ConfigPath(), call: "xray run -test -config "},
	} {
		t.Run(tt.core, func(t *testing.T) {
			path := a.Layout.Resolve(tt.path)
			if err := system.AtomicWrite(path, []byte(`{"inbounds":[]}`), 0600); err != nil {
				t.Fatal(err)
			}
			if err := a.ValidateServerConfig(context.Background(), tt.core); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(r.callLog(), tt.call+path) {
				t.Fatalf("validator call missing: %s", r.callLog())
			}
		})
	}
}

func TestServerConfigExistsIgnoresEmptyPlaceholders(t *testing.T) {
	a, _ := testApp(t, &fakeRunner{})
	path := a.Layout.Resolve(xray.New().ConfigPath())
	tests := []struct {
		name string
		data string
		want bool
	}{
		{name: "zero bytes", data: "", want: false},
		{name: "whitespace", data: " \n\t", want: false},
		{name: "empty object", data: "{\n  }\n", want: false},
		{name: "configured object", data: `{"inbounds": []}`, want: true},
		{name: "invalid but nonempty", data: "custom configuration", want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := system.AtomicWrite(path, []byte(tt.data), 0600); err != nil {
				t.Fatal(err)
			}
			got, err := a.ServerConfigExists(domain.CoreXray)
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Fatalf("ServerConfigExists() = %v, want %v for %q", got, tt.want, tt.data)
			}
		})
	}
}

func TestClashClientOutput(t *testing.T) {
	r := &fakeRunner{}
	a, _ := testApp(t, r)
	n := domain.NodeSpec{
		ManagedBy: "proxyforge", Core: domain.CoreXray, Server: "server.example.com", Port: 8443,
		SNI: "www.example.com", UUID: "123e4567-e89b-42d3-a456-426614174000",
		PrivateKey: "must-not-leak", PublicKey: "public", ShortID: "0123456789abcdef",
	}
	if err := a.Store.Save(n); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "clash.yaml")
	b, err := a.ClientConfig(context.Background(), domain.CoreXray, ClientFormatClash, path, false)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "type: vless") || !strings.Contains(string(b), `public-key: "public"`) {
		t.Fatalf("unexpected Clash output:\n%s", b)
	}
	if strings.Contains(string(b), n.PrivateKey) {
		t.Fatal("Clash output leaked private key")
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("mode=%o", info.Mode().Perm())
	}
	if strings.Contains(r.callLog(), "xray run -test") {
		t.Fatal("Xray validator was incorrectly used for Clash YAML")
	}
}

func TestClientRejectsUnknownFormat(t *testing.T) {
	r := &fakeRunner{}
	a, _ := testApp(t, r)
	if _, err := a.ClientConfig(context.Background(), domain.CoreSingBox, "legacy-clash", "", false); err == nil || !strings.Contains(err.Error(), "native 或 clash") {
		t.Fatalf("error=%v, want supported formats", err)
	}
}

func TestClientRequiresRoot(t *testing.T) {
	a := &App{RootCheck: func() error { return errors.New("root required") }}
	if _, err := a.Client(context.Background(), domain.CoreSingBox, "", false); err == nil || !strings.Contains(err.Error(), "root required") {
		t.Fatalf("error = %v, want root requirement", err)
	}
}
