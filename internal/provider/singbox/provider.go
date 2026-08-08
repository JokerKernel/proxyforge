package singbox

import (
	"context"
	"fmt"
	"net"
	"regexp"
	"strings"

	"proxyforge/internal/domain"
	"proxyforge/internal/provider"
	"proxyforge/internal/provider/jsonutil"
)

type Provider struct{}

func New() *Provider                        { return &Provider{} }
func (*Provider) Name() string              { return domain.CoreSingBox }
func (*Provider) Binary() string            { return "sing-box" }
func (*Provider) ServiceName() string       { return "sing-box.service" }
func (*Provider) ConfigPath() string        { return "/etc/sing-box/config.json" }
func (*Provider) OfficialScriptURL() string { return "https://sing-box.app/install.sh" }
func (*Provider) ScriptHosts() []string {
	return []string{"sing-box.app", "sing-box.sagernet.org", "raw.githubusercontent.com", "github.com"}
}
func (*Provider) InstallArgs(version string) []string {
	if version == "" {
		return nil
	}
	return []string{"--version", version}
}
func (*Provider) PackageName() string     { return "sing-box" }
func (*Provider) UninstallArgs() []string { return nil }
func (*Provider) CleanupPaths() []string  { return []string{"/etc/sing-box", "/var/lib/sing-box"} }

func (*Provider) Version(ctx context.Context, r provider.Runner) (string, error) {
	b, err := r.Run(ctx, "sing-box", "version")
	if err != nil {
		return "", fmt.Errorf("读取 sing-box 版本: %w", err)
	}
	line := strings.Split(strings.TrimSpace(string(b)), "\n")[0]
	if line == "" {
		return "", fmt.Errorf("sing-box 返回了空版本")
	}
	return line, nil
}

func (*Provider) GenerateKeyPair(ctx context.Context, r provider.Runner) (domain.KeyPair, error) {
	b, err := r.Run(ctx, "sing-box", "generate", "reality-keypair")
	if err != nil {
		return domain.KeyPair{}, fmt.Errorf("生成 sing-box REALITY 密钥: %w", err)
	}
	re := regexp.MustCompile(`(?m)^(PrivateKey|Private key|Private):\s*(\S+)|^(PublicKey|Public key|Public):\s*(\S+)`)
	var pair domain.KeyPair
	for _, m := range re.FindAllStringSubmatch(string(b), -1) {
		label, value := m[1], m[2]
		if label == "" {
			label, value = m[3], m[4]
		}
		if strings.HasPrefix(strings.ToLower(label), "private") {
			pair.Private = value
		} else {
			pair.Public = value
		}
	}
	if pair.Private == "" || pair.Public == "" {
		return pair, fmt.Errorf("无法解析 sing-box REALITY 密钥输出")
	}
	return pair, nil
}

func (*Provider) RenderServer(n domain.NodeSpec) ([]byte, error) {
	v := map[string]any{
		"log": map[string]any{"level": "info", "timestamp": true},
		"dns": map[string]any{"servers": []any{map[string]any{"type": "local", "tag": "local"}}},
		"inbounds": []any{map[string]any{
			"type": "vless", "tag": "vless-reality", "listen": "::", "listen_port": n.Port,
			"users": []any{map[string]any{"uuid": n.UUID, "flow": domain.VisionFlow}},
			"tls": map[string]any{"enabled": true, "server_name": n.SNI, "reality": map[string]any{
				"enabled": true, "handshake": map[string]any{"server": targetHost(n.Target), "server_port": targetPort(n.Target)},
				"private_key": n.PrivateKey, "short_id": []string{n.ShortID},
			}},
		}},
		"outbounds": []any{map[string]any{"type": "direct", "tag": "direct"}},
		"route":     privateNetworkRoute(true, "direct"),
	}
	return jsonutil.Marshal(v)
}

func (*Provider) RenderClient(n domain.NodeSpec) ([]byte, error) {
	v := map[string]any{
		"log":      map[string]any{"level": "info", "timestamp": true},
		"inbounds": []any{map[string]any{"type": "mixed", "tag": "mixed-in", "listen": "127.0.0.1", "listen_port": 2080}},
		"outbounds": []any{map[string]any{
			"type": "vless", "tag": "proxy", "server": n.Server, "server_port": n.Port, "uuid": n.UUID, "flow": domain.VisionFlow,
			"tls": map[string]any{"enabled": true, "server_name": n.SNI, "utls": map[string]any{"enabled": true, "fingerprint": "chrome"},
				"reality": map[string]any{"enabled": true, "public_key": n.PublicKey, "short_id": n.ShortID}},
		}},
		"route": privateNetworkRoute(false, "proxy"),
	}
	return jsonutil.Marshal(v)
}

func privateNetworkRoute(resolveDomains bool, final string) map[string]any {
	rules := make([]any, 0, 2)
	if resolveDomains {
		rules = append(rules, map[string]any{"action": "resolve", "server": "local"})
	}
	rules = append(rules, map[string]any{
		"ip_is_private": true,
		"ip_cidr":       domain.BlockedDestinationCIDRs(),
		"action":        "reject",
	})
	route := map[string]any{"rules": rules, "final": final}
	if resolveDomains {
		route["default_domain_resolver"] = "local"
	}
	return route
}

func (*Provider) ValidateConfig(ctx context.Context, r provider.Runner, path string) error {
	if _, err := r.Run(ctx, "sing-box", "check", "-c", path); err != nil {
		return fmt.Errorf("sing-box 配置校验失败: %w", err)
	}
	return nil
}

func targetPort(target string) int {
	_, raw, err := net.SplitHostPort(target)
	if err == nil {
		var p int
		if _, err := fmt.Sscanf(raw, "%d", &p); err == nil {
			return p
		}
	}
	return 443
}

func targetHost(target string) string {
	host, _, err := net.SplitHostPort(target)
	if err == nil {
		return host
	}
	return target
}
