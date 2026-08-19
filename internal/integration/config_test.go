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
	"proxyforge/internal/provider/singbox"
	"proxyforge/internal/provider/xray"
)

type binaryRunner struct{ names map[string]string }

func (r binaryRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	if replacement := r.names[name]; replacement != "" {
		name = replacement
	}
	b, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	if err != nil {
		return b, fmt.Errorf("%s: %s: %w", name, b, err)
	}
	return b, nil
}

func TestRealStableBinariesValidateAllConfigs(t *testing.T) {
	n := domain.NodeSpec{InboundTag: "proxyforge-test-in", Server: "203.0.113.10", Port: 443, SNI: "example.com", Target: "example.com:443", UserName: domain.DefaultUserName, UUID: "123e4567-e89b-42d3-a456-426614174000", PrivateKey: "UuMBgl7MXTPx9inmQp2UC7Jcnwc6XYbwDNebonM-FCc", PublicKey: "jNXHt1yRo0vDuchQlIP6Z0ZvjT3KtzVI-T4E7RoLJS0", ShortID: "0123456789abcdef"}
	providers := []provider.CoreProvider{singbox.New(), xray.New()}
	for _, p := range providers {
		p := p
		t.Run(p.Name(), func(t *testing.T) {
			binary := os.Getenv(envName(p.Name()))
			if binary == "" {
				binary, _ = exec.LookPath(p.Binary())
			}
			if binary == "" {
				t.Skipf("设置 %s 或将 %s 放入 PATH 可启用真实二进制验收", envName(p.Name()), p.Binary())
			}
			runner := binaryRunner{names: map[string]string{p.Binary(): binary}}
			configs := []struct {
				name   string
				render func(domain.NodeSpec) ([]byte, error)
			}{{"server", p.RenderServer}, {"client", p.RenderClient}}
			if p.Name() == domain.CoreSingBox {
				configs = append(configs, struct {
					name   string
					render func(domain.NodeSpec) ([]byte, error)
				}{"simplified-server", func(n domain.NodeSpec) ([]byte, error) {
					n.SimplifiedConfig = true
					return p.RenderServer(n)
				}})
				configs = append(configs, struct {
					name   string
					render func(domain.NodeSpec) ([]byte, error)
				}{"fallback-guard-server", func(n domain.NodeSpec) ([]byte, error) {
					n.SingBoxFallbackGuard = true
					n.SingBoxFallbackPort = 61432
					return p.RenderServer(n)
				}})
			} else if p.Name() == domain.CoreXray {
				configs = append(configs, struct {
					name   string
					render func(domain.NodeSpec) ([]byte, error)
				}{"fallback-guard-server", func(n domain.NodeSpec) ([]byte, error) {
					n.XrayFallbackGuard = true
					n.XrayFallbackPort = 61431
					return p.RenderServer(n)
				}})
			}
			for _, item := range configs {
				item := item
				t.Run(item.name, func(t *testing.T) {
					b, err := item.render(n)
					if err != nil {
						t.Fatal(err)
					}
					path := filepath.Join(t.TempDir(), item.name+".json")
					if err := os.WriteFile(path, b, 0600); err != nil {
						t.Fatal(err)
					}
					if err := p.ValidateConfig(context.Background(), runner, path); err != nil {
						t.Fatal(err)
					}
				})
			}
		})
	}
}

func envName(core string) string {
	if core == domain.CoreSingBox {
		return "SING_BOX_BIN"
	}
	return "XRAY_BIN"
}
