package integration

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"proxyforge/internal/domain"
	"proxyforge/internal/provider"
	"proxyforge/internal/provider/mihomo"
	"proxyforge/internal/provider/singbox"
	"proxyforge/internal/provider/xray"
)

func realityTestNode() domain.NodeSpec {
	return domain.NodeSpec{InboundTag: "proxyforge-test-in", Server: "203.0.113.10", Port: 443,
		SNI: "example.com", Target: "example.com:443", UserName: domain.DefaultUserName,
		UUID:       "123e4567-e89b-42d3-a456-426614174000",
		PrivateKey: "UuMBgl7MXTPx9inmQp2UC7Jcnwc6XYbwDNebonM-FCc",
		PublicKey:  "jNXHt1yRo0vDuchQlIP6Z0ZvjT3KtzVI-T4E7RoLJS0", ShortID: "0123456789abcdef"}
}

// Validate combinations with the real parsers, including settings applied while
// access control, independent TLS landings and both relay protocols are present.
func TestRealConfigurationSettingsMatrix(t *testing.T) {
	for _, p := range []provider.CoreProvider{xray.New(), singbox.New()} {
		t.Run(p.Name(), func(t *testing.T) {
			binary := testBinary(t, p)
			runner := binaryRunner{names: map[string]string{p.Binary(): binary}}
			dns := p.(provider.DNSProfileProvider)
			ip := p.(provider.OutboundIPProvider)
			fallback := p.(provider.FallbackIPProvider)
			log := p.(provider.LogLevelProvider)
			tls := tlsTestNode(t)
			modes := []string{"standard", "guard", "tls"}
			if p.Name() == domain.CoreSingBox {
				modes = append(modes, "simplified")
			}
			validate := func(t *testing.T, b []byte) {
				t.Helper()
				path := filepath.Join(t.TempDir(), "config.json")
				if err := os.WriteFile(path, b, 0600); err != nil {
					t.Fatal(err)
				}
				if err := p.ValidateConfig(context.Background(), runner, path); err != nil {
					t.Fatal(err)
				}
			}
			for _, mode := range modes {
				t.Run(mode, func(t *testing.T) {
					n := realityTestNode()
					if mode == "tls" {
						n = tls
					}
					n.Core = p.Name()
					n.SimplifiedConfig = mode == "simplified"
					if mode == "guard" {
						if p.Name() == domain.CoreXray {
							n.XrayFallbackGuard, n.XrayFallbackPort, n.XrayFallbackHTTPDomain = true, 31443, true
						} else {
							n.SingBoxFallbackGuard, n.SingBoxFallbackPort, n.SingBoxFallbackHTTPDomain = true, 31443, true
						}
					}
					access := *tls.TLSCertificate
					access.Name, access.UserName, access.UUID = "independent", "independent", "5d5c5537-56a4-4b1a-a3bf-9e5a1b378769"
					access.Security, access.Port, access.Enabled = "tls", 32443, true
					n.LandingAccesses = []domain.LandingAccess{access}
					for i, security := range []string{"reality", "tls"} {
						peer := domain.LandingPeer{Core: p.Name(), Security: security, Server: "198.51.100.20", Port: 443,
							SNI: "example.com", UUID: "26831be5-da6c-4d2d-b983-9280601670ae", Flow: domain.VisionFlow,
							PublicKey: realityTestNode().PublicKey, ShortID: realityTestNode().ShortID,
							CertificateSHA256: access.CertificateSHA256, CertificatePublicKeySHA256: access.CertificatePublicKeySHA256}
						name := "relay-" + security
						n.RelayLinks = append(n.RelayLinks, domain.RelayLink{Name: name, UserName: name,
							UUID: fmt.Sprintf("11b50d17-8c92-4b5d-9449-245f3b70d7b%d", i), Enabled: true, Upstream: peer,
							AccessPolicy: &domain.AccessPolicy{Mode: domain.AccessModeWhitelist, Whitelist: []domain.AccessEntry{{Type: "domain", Value: name + ".test", Ports: "80,443,8000-8100"}}}})
					}
					n.AccessPolicy = domain.AccessPolicy{Mode: domain.AccessModeDefault,
						Whitelist: []domain.AccessEntry{{Type: "domain", Value: "allowed.test", IncludeSubdomains: true}, {Type: "cidr", Value: "192.168.1.0/24"}},
						Blacklist: []domain.AccessEntry{{Type: "domain", Value: "denied.test"}}}
					base, err := p.RenderServer(n)
					if err != nil {
						t.Fatal(err)
					}
					for _, profile := range dns.DNSProfiles() {
						for _, strategy := range ip.OutboundIPStrategies() {
							t.Run(profile+"/"+strategy, func(t *testing.T) {
								b, err := dns.PatchDNSProfile(base, profile)
								if err != nil {
									t.Fatal(err)
								}
								b, err = ip.PatchOutboundIPStrategy(b, strategy)
								if err == nil && mode == "guard" {
									b, err = fallback.PatchFallbackIPStrategy(b, strategy)
								}
								if err == nil {
									// The application reapplies access control after settings changes.
									b, err = p.(provider.AccessPolicyProvider).PatchAccessPolicy(b, n, n)
								}
								if err != nil {
									t.Fatal(err)
								}
								validate(t, b)
								if got, err := ip.CurrentOutboundIPStrategy(b); err != nil || got != strategy {
									t.Fatalf("IP strategy=%s, error=%v, want %s", got, err, strategy)
								}
								if mode == "guard" {
									if got, err := fallback.CurrentFallbackIPStrategy(b); err != nil || got != strategy {
										t.Fatalf("fallback strategy=%s, error=%v, want %s", got, err, strategy)
									}
								}
							})
						}
					}
					for _, level := range log.LogLevels() {
						t.Run("log/"+level, func(t *testing.T) {
							b, err := log.PatchLogLevel(base, level)
							if err != nil {
								t.Fatal(err)
							}
							validate(t, b)
						})
					}
					if mode == "guard" || mode == "standard" {
						next := n
						next.UUID, next.SNI, next.Target = "123e4567-e89b-42d3-a456-426614174001", "new.example.com", "new.example.com:8443"
						b, err := p.PatchServer(base, n, next, true)
						if err != nil {
							t.Fatal(err)
						}
						validate(t, b)
					}
				})
			}
		})
	}
}

func TestRealMihomoClientConfigs(t *testing.T) {
	binary := os.Getenv("MIHOMO_BIN")
	if binary == "" {
		binary, _ = exec.LookPath("mihomo")
	}
	if binary == "" {
		t.Skip("设置 MIHOMO_BIN 启用 Mihomo 配置验收")
	}
	for _, n := range []domain.NodeSpec{realityTestNode(), tlsTestNode(t)} {
		for _, core := range []string{domain.CoreXray, domain.CoreSingBox} {
			n.Core = core
			t.Run(core+"/"+domain.NormalizeLandingSecurity(n.Security), func(t *testing.T) {
				b, err := mihomo.RenderClient(n)
				if err != nil {
					t.Fatal(err)
				}
				dir := t.TempDir()
				path := filepath.Join(dir, "config.yaml")
				if err := os.WriteFile(path, b, 0600); err != nil {
					t.Fatal(err)
				}
				if output, err := exec.Command(binary, "-t", "-d", dir, "-f", path).CombinedOutput(); err != nil {
					t.Fatalf("Mihomo validation: %v: %s", err, output)
				}
			})
		}
	}
}
