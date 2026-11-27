package app

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"

	"proxyforge/internal/domain"
	"proxyforge/internal/system"
)

func TestTLSNodeLifecycle(t *testing.T) {
	for _, core := range []string{domain.CoreXray, domain.CoreSingBox} {
		t.Run(core, func(t *testing.T) {
			runner := &fakeRunner{port: freePort(t)}
			a, _ := testApp(t, runner)
			ctx := context.Background()
			// TLS must not depend on target probing or REALITY key generation.
			a.Targets = nil
			opts := domain.GenerateOptions{Security: "tls", Server: "server.example.com", Port: runner.port, NonInteractive: true}
			first, err := a.Generate(ctx, core, opts)
			if err != nil {
				t.Fatal(err)
			}
			if !first.IsTLS() || first.TLSCertificate == nil || first.PrivateKey != "" || first.PublicKey != "" || first.ShortID != "" || first.Target != "" || nodeFallbackPort(first) != 0 || runner.keyGeneration != 0 {
				t.Fatalf("invalid TLS node: %#v", first)
			}
			assertManagedTLSCertificate(t, *first.TLSCertificate, a.Now())
			key, err := os.ReadFile(first.TLSCertificate.KeyFile)
			if err != nil {
				t.Fatal(err)
			}
			for _, format := range []string{ClientFormatNative, ClientFormatClash} {
				b, err := a.ClientConfig(ctx, core, format, "", false)
				if err != nil {
					t.Fatal(err)
				}
				if !bytes.Contains(b, []byte(first.SNI)) || bytes.Contains(b, key) || bytes.Contains(b, []byte(first.TLSCertificate.KeyFile)) || bytes.Contains(b, []byte("reality")) || bytes.Contains(b, []byte("insecure\": true")) || bytes.Contains(b, []byte("skip-cert-verify: true")) {
					t.Fatalf("invalid client %s: %s", format, b)
				}
				pin := first.TLSCertificate.CertificateSHA256
				if core == domain.CoreSingBox && format == ClientFormatNative {
					pin = first.TLSCertificate.CertificatePublicKeySHA256
				}
				if !bytes.Contains(b, []byte(pin)) {
					t.Fatalf("missing pin: %s", b)
				}
			}
			opts.Security = "" // Regeneration inherits the current protocol.
			second, err := a.Generate(ctx, core, opts)
			if err != nil {
				t.Fatal(err)
			}
			if second.UUID != first.UUID || !reflect.DeepEqual(second.TLSCertificate, first.TLSCertificate) {
				t.Fatal("regeneration rotated identity")
			}
			access, err := a.AddLandingAccessWithOptions(ctx, core, "shared", LandingAddOptions{Security: domain.LandingSecurityCurrent})
			if err != nil {
				t.Fatal(err)
			}
			if access.Security != "tls" || !access.UsesPrimaryInbound() {
				t.Fatalf("access=%#v", access)
			}
			bundle, err := a.LandingBundle(core, "shared")
			if err != nil {
				t.Fatal(err)
			}
			if bundle.Peer.Security != "tls" || bundle.Peer.Port != first.Port || bundle.Peer.CertificateSHA256 != first.TLSCertificate.CertificateSHA256 {
				t.Fatalf("bundle=%#v", bundle)
			}
			peer := bundle.Peer
			peer.Server = "exit.example.com"
			link, err := a.AddRelayLink(ctx, core, "via-exit", peer, RelayAddOptions{})
			if err != nil {
				t.Fatal(err)
			}
			b, err := a.RelayClientConfig(ctx, core, link.Name, ClientFormatNative, "", false)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Contains(b, []byte(link.UUID)) || bytes.Contains(b, []byte("reality")) {
				t.Fatalf("relay client=%s", b)
			}
			p, _ := a.Registry.Get(core)
			path := a.Layout.Resolve(p.ConfigPath())
			config, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var root map[string]any
			if err := json.Unmarshal(config, &root); err != nil {
				t.Fatal(err)
			}
			root["manual"] = "keep"
			manual, err := json.Marshal(root)
			if err != nil {
				t.Fatal(err)
			}
			if err := system.AtomicWrite(path, manual, 0600); err != nil {
				t.Fatal(err)
			}
			rotated, err := a.ResetCredentials(ctx, core, domain.ResetOptions{Credentials: true})
			if err != nil {
				t.Fatal(err)
			}
			if rotated.UUID == first.UUID || !reflect.DeepEqual(rotated.TLSCertificate, first.TLSCertificate) {
				t.Fatal("UUID reset changed certificate")
			}
			renewed, err := a.ResetCredentials(ctx, core, domain.ResetOptions{RotateCertificate: true})
			if err != nil {
				t.Fatal(err)
			}
			if renewed.UUID != rotated.UUID || renewed.TLSCertificate.CertificateSHA256 == first.TLSCertificate.CertificateSHA256 || renewed.SNI == first.SNI {
				t.Fatal("certificate reset changed UUID or retained certificate")
			}
			config, _ = os.ReadFile(path)
			if !bytes.Contains(config, []byte(`"manual": "keep"`)) {
				t.Fatalf("manual config lost: %s", config)
			}
			bundle, err = a.LandingBundle(core, "shared")
			if err != nil {
				t.Fatal(err)
			}
			if bundle.Peer.CertificateSHA256 != renewed.TLSCertificate.CertificateSHA256 || bundle.Peer.UUID != access.UUID {
				t.Fatal("shared landing pin/UUID incorrect after renewal")
			}
			// Old files remain available for configuration backups.
			if _, err := os.Stat(first.TLSCertificate.KeyFile); err != nil {
				t.Fatal(err)
			}
			if err := a.SetLandingAccessEnabled(ctx, core, "shared", false); err != nil {
				t.Fatal(err)
			}
			if err := a.RemoveLandingAccess(ctx, core, "shared"); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestTLSNodeFailuresPreserveStateAndCertificates(t *testing.T) {
	for _, core := range []string{domain.CoreXray, domain.CoreSingBox} {
		t.Run(core, func(t *testing.T) {
			runner := &fakeRunner{port: freePort(t)}
			a, _ := testApp(t, runner)
			ctx := context.Background()
			opts := domain.GenerateOptions{Security: "tls", Server: "server.example.com", Port: runner.port, NonInteractive: true}
			runner.failRestart = true
			if _, err := a.Generate(ctx, core, opts); err == nil {
				t.Fatal("expected restart failure")
			}
			entries, _ := os.ReadDir(a.Layout.TLSRoot(core))
			if len(entries) != 0 {
				t.Fatal("failed deployment left certificate")
			}
			runner.failRestart = false
			first, err := a.Generate(ctx, core, opts)
			if err != nil {
				t.Fatal(err)
			}
			p, _ := a.Registry.Get(core)
			path := a.Layout.Resolve(p.ConfigPath())
			before, _ := os.ReadFile(path)
			runner.failRestart = true
			if _, err := a.ResetCredentials(ctx, core, domain.ResetOptions{RotateCertificate: true}); err == nil {
				t.Fatal("expected renewal failure")
			}
			state, err := a.Store.Load(core)
			if err != nil {
				t.Fatal(err)
			}
			after, _ := os.ReadFile(path)
			entries, _ = os.ReadDir(a.Layout.TLSRoot(core))
			if !reflect.DeepEqual(state, first) || !bytes.Equal(before, after) || len(entries) != 1 {
				t.Fatal("failed renewal changed state, config or certificate")
			}
			opts.RotateCertificate = true
			runner.failRestart = true
			if _, err := a.Generate(ctx, core, opts); err == nil {
				t.Fatal("expected regeneration failure")
			}
			state, err = a.Store.Load(core)
			if err != nil {
				t.Fatal(err)
			}
			entries, _ = os.ReadDir(a.Layout.TLSRoot(core))
			if !reflect.DeepEqual(state, first) || len(entries) != 1 {
				t.Fatal("failed regeneration changed certificate state")
			}
			runner.failRestart = false
			state.TLSCertificate.CertificateSHA256 = strings.Repeat("0", 64)
			if err := a.Store.Save(state); err != nil {
				t.Fatal(err)
			}
			if _, err := a.Client(ctx, core, "", false); err == nil {
				t.Fatal("export accepted mismatched certificate")
			}
		})
	}
}

func TestTLSNodeProtocolSwitchAndLegacyState(t *testing.T) {
	for _, core := range []string{domain.CoreXray, domain.CoreSingBox} {
		t.Run(core, func(t *testing.T) {
			runner := &fakeRunner{port: freePort(t)}
			a, _ := testApp(t, runner)
			ctx := context.Background()
			opts := domain.GenerateOptions{Server: "server.example.com", Port: runner.port, SNI: "example.com", StandardConfig: true}
			first, err := a.Generate(ctx, core, opts)
			if err != nil {
				t.Fatal(err)
			}
			first.Security = ""
			first.SchemaVersion = 3
			if err := a.Store.Save(first); err != nil {
				t.Fatal(err)
			}
			access, err := a.AddLandingAccessWithOptions(ctx, core, "shared", LandingAddOptions{Security: domain.LandingSecurityCurrent})
			if err != nil {
				t.Fatal(err)
			}
			opts.Security, opts.SNI = "tls", ""
			tlsNode, err := a.Generate(ctx, core, opts)
			if err != nil {
				t.Fatal(err)
			}
			if tlsNode.UUID == first.UUID || tlsNode.LandingAccesses[0].Security != "tls" || tlsNode.LandingAccesses[0].UUID != access.UUID {
				t.Fatal("protocol switch did not rotate main UUID or changed shared landing UUID")
			}
			opts.Security, opts.SNI = "reality", "example.com"
			restored, err := a.Generate(ctx, core, opts)
			if err != nil {
				t.Fatal(err)
			}
			if restored.IsTLS() || restored.PrivateKey == "" || restored.TLSCertificate != nil || restored.LandingAccesses[0].Security != "reality" || restored.LandingAccesses[0].UUID != access.UUID || restored.UUID == tlsNode.UUID || restored.UUID == first.UUID {
				t.Fatalf("restored=%#v", restored)
			}
			p, _ := a.Registry.Get(core)
			config, err := os.ReadFile(a.Layout.Resolve(p.ConfigPath()))
			if err != nil || !bytes.Contains(config, []byte(restored.UUID)) || bytes.Contains(config, []byte(first.UUID)) || bytes.Contains(config, []byte(tlsNode.UUID)) {
				t.Fatalf("server retained previous protocol UUID: %s, err=%v", config, err)
			}
			client, err := a.ClientConfig(ctx, core, ClientFormatNative, "", false)
			if err != nil || !bytes.Contains(client, []byte(restored.UUID)) {
				t.Fatalf("client missing switched UUID: %s, err=%v", client, err)
			}
			sameProtocol, err := a.Generate(ctx, core, opts)
			if err != nil || sameProtocol.UUID != restored.UUID || sameProtocol.PrivateKey != restored.PrivateKey || sameProtocol.ShortID != restored.ShortID {
				t.Fatalf("same protocol regeneration rotated credentials: %#v, err=%v", sameProtocol, err)
			}
		})
	}
}

func TestProtocolSwitchFailurePreservesUUIDAndConfig(t *testing.T) {
	for _, core := range []string{domain.CoreXray, domain.CoreSingBox} {
		for _, security := range []string{domain.LandingSecurityReality, domain.LandingSecurityTLS} {
			t.Run(core+"/"+security, func(t *testing.T) {
				runner := &fakeRunner{port: freePort(t)}
				a, _ := testApp(t, runner)
				ctx := context.Background()
				opts := domain.GenerateOptions{Security: security, Server: "server.example.com", Port: runner.port, StandardConfig: true}
				if security == domain.LandingSecurityReality {
					opts.SNI = "example.com"
				}
				first, err := a.Generate(ctx, core, opts)
				if err != nil {
					t.Fatal(err)
				}
				p, _ := a.Registry.Get(core)
				path := a.Layout.Resolve(p.ConfigPath())
				beforeConfig, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				beforeState, err := os.ReadFile(a.Layout.StatePath(core))
				if err != nil {
					t.Fatal(err)
				}
				beforeCertificates, _ := os.ReadDir(a.Layout.TLSRoot(core))
				opts.Security, opts.SNI = domain.LandingSecurityTLS, ""
				if security == domain.LandingSecurityTLS {
					opts.Security, opts.SNI = domain.LandingSecurityReality, "example.com"
				}
				runner.failRestart = true
				failed, err := a.Generate(ctx, core, opts)
				if err == nil || failed.UUID == first.UUID {
					t.Fatalf("expected failure after generating a new UUID: %#v, err=%v", failed, err)
				}
				afterConfig, err := os.ReadFile(path)
				if err != nil || !bytes.Equal(beforeConfig, afterConfig) {
					t.Fatalf("failed switch changed core config: %v", err)
				}
				afterState, err := os.ReadFile(a.Layout.StatePath(core))
				if err != nil || !bytes.Equal(beforeState, afterState) {
					t.Fatalf("failed switch changed saved UUID/state: %v", err)
				}
				afterCertificates, _ := os.ReadDir(a.Layout.TLSRoot(core))
				if len(beforeCertificates) != len(afterCertificates) {
					t.Fatal("failed switch left a new certificate")
				}
			})
		}
	}
}

func TestTLSNodeRejectsRealityOptions(t *testing.T) {
	for _, opts := range []domain.GenerateOptions{
		{Security: "unknown"}, {Security: "tls", SNI: "example.com"}, {Security: "tls", Target: "example.com:443"}, {Security: "tls", XrayFallbackGuard: true}, {Security: "tls", SingBoxFallbackPort: 30001}, {Security: "reality", RotateCertificate: true},
	} {
		t.Run(strings.ReplaceAll(opts.Security+opts.SNI+opts.Target, "/", "_"), func(t *testing.T) {
			a, _ := testApp(t, &fakeRunner{port: freePort(t)})
			opts.Server, opts.Port = "server.example.com", 15443
			if _, err := a.Generate(context.Background(), domain.CoreXray, opts); err == nil {
				t.Fatal("invalid options accepted")
			}
		})
	}
}
