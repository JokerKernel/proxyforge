package xray

import (
	"context"
	"fmt"
	"strings"

	"proxyforge/internal/domain"
	"proxyforge/internal/provider"
	"proxyforge/internal/provider/jsonutil"
)

type Provider struct{}

func New() *Provider                  { return &Provider{} }
func (*Provider) Name() string        { return domain.CoreXray }
func (*Provider) Binary() string      { return "xray" }
func (*Provider) ServiceName() string { return "xray.service" }
func (*Provider) ConfigPath() string  { return "/usr/local/etc/xray/config.json" }
func (*Provider) OfficialScriptURL() string {
	return "https://github.com/XTLS/Xray-install/raw/main/install-release.sh"
}
func (*Provider) ScriptHosts() []string {
	return []string{"github.com", "raw.githubusercontent.com", "objects.githubusercontent.com"}
}
func (*Provider) InstallArgs(version string) []string {
	args := []string{"install"}
	if version != "" {
		args = append(args, "--version", version)
	}
	return args
}
func (*Provider) ScriptProxyArgs(proxyURL string) []string {
	if proxyURL == "" {
		return nil
	}
	return []string{"--proxy", proxyURL}
}
func (*Provider) PackageName() string     { return "" }
func (*Provider) UninstallArgs() []string { return []string{"remove"} }
func (*Provider) CleanupPaths() []string {
	return []string{"/usr/local/etc/xray", "/var/log/xray"}
}

func (*Provider) Version(ctx context.Context, r provider.Runner) (string, error) {
	b, err := r.Run(ctx, "xray", "version")
	if err != nil {
		return "", fmt.Errorf("读取 Xray 版本: %w", err)
	}
	line := strings.Split(strings.TrimSpace(string(b)), "\n")[0]
	if line == "" {
		return "", fmt.Errorf("Xray 返回了空版本")
	}
	return line, nil
}

func (*Provider) GenerateKeyPair(ctx context.Context, r provider.Runner) (domain.KeyPair, error) {
	b, err := r.Run(ctx, "xray", "x25519")
	if err != nil {
		return domain.KeyPair{}, fmt.Errorf("生成 Xray REALITY 密钥: %w", err)
	}
	var pair domain.KeyPair
	for _, line := range strings.Split(string(b), "\n") {
		parts := strings.SplitN(line, ":", 2)
		if len(parts) != 2 {
			continue
		}
		values := strings.Fields(parts[1])
		if len(values) == 0 {
			continue
		}
		label := strings.ToLower(strings.TrimSpace(parts[0]))
		label = strings.NewReplacer(" ", "", "_", "", "-", "").Replace(label)
		switch label {
		case "private", "privatekey":
			pair.Private = values[0]
		case "public", "publickey", "password", "password(publickey)":
			pair.Public = values[0]
		}
	}
	if pair.Private == "" || pair.Public == "" {
		return pair, fmt.Errorf("无法解析 Xray REALITY 密钥输出（缺少 PrivateKey 或 Password/PublicKey 字段）")
	}
	return pair, nil
}

func (*Provider) RenderServer(n domain.NodeSpec) ([]byte, error) {
	v := map[string]any{
		"log": map[string]any{"loglevel": "warning"},
		"inbounds": []any{map[string]any{
			"listen": "0.0.0.0", "port": n.Port, "protocol": "vless", "tag": n.InboundTag,
			"settings": map[string]any{"clients": []any{map[string]any{"email": n.UserName, "id": n.UUID, "flow": domain.VisionFlow}}, "decryption": "none"},
			"streamSettings": map[string]any{"network": "raw", "security": "reality", "realitySettings": map[string]any{
				"show": false, "target": n.Target, "xver": 0, "serverNames": []string{n.SNI}, "privateKey": n.PrivateKey, "shortIds": []string{n.ShortID},
			}},
		}},
		"outbounds": []any{directOutbound(), blockedOutbound()},
		"routing":   privateNetworkRouting(),
	}
	return jsonutil.Marshal(v)
}

func (*Provider) RenderClient(n domain.NodeSpec) ([]byte, error) {
	v := map[string]any{
		"log": map[string]any{"loglevel": "warning"},
		"inbounds": []any{
			map[string]any{"listen": "127.0.0.1", "port": 10808, "protocol": "socks", "settings": map[string]any{"udp": true}},
			map[string]any{"listen": "127.0.0.1", "port": 10809, "protocol": "http", "settings": map[string]any{}},
		},
		"outbounds": []any{map[string]any{
			"protocol": "vless", "tag": "proxy", "settings": map[string]any{"vnext": []any{map[string]any{
				"address": n.Server, "port": n.Port, "users": []any{map[string]any{"id": n.UUID, "encryption": "none", "flow": domain.VisionFlow}},
			}}},
			"streamSettings": map[string]any{"network": "raw", "security": "reality", "realitySettings": map[string]any{
				"fingerprint": "chrome", "serverName": n.SNI, "password": n.PublicKey, "shortId": n.ShortID, "spiderX": "/",
			}},
		}, blockedOutbound()},
		"routing": privateNetworkRouting(),
	}
	return jsonutil.Marshal(v)
}

func privateNetworkRouting() map[string]any {
	return map[string]any{
		"domainStrategy": "IPOnDemand",
		"rules": []any{map[string]any{
			"type":        "field",
			"ip":          xrayBlockedDestinations(),
			"outboundTag": "blocked-private",
		}},
	}
}

func xrayBlockedDestinations() []string {
	return append([]string{"geoip:private"}, domain.BlockedDestinationCIDRs()...)
}

func blockedOutbound() map[string]any {
	return map[string]any{"protocol": "blackhole", "tag": "blocked-private", "settings": map[string]any{}}
}

func directOutbound() map[string]any {
	return map[string]any{
		"protocol": "freedom",
		"tag":      "direct",
		"settings": map[string]any{"domainStrategy": "UseIP"},
	}
}

func (*Provider) ValidateConfig(ctx context.Context, r provider.Runner, path string) error {
	if _, err := r.Run(ctx, "xray", "run", "-test", "-config", path); err != nil {
		return fmt.Errorf("Xray 配置校验失败: %w", err)
	}
	return nil
}
