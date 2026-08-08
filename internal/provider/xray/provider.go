package xray

import (
	"context"
	"fmt"
	"regexp"
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
func (*Provider) InstallArgs(version string, _ bool) []string {
	args := []string{"install"}
	if version != "" {
		args = append(args, "--version", version)
	}
	return args
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
	re := regexp.MustCompile(`(?m)^(Private key|PrivateKey|Private):\s*(\S+)|^(Public key|PublicKey|Password|Public):\s*(\S+)`)
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
		return pair, fmt.Errorf("无法解析 Xray REALITY 密钥输出")
	}
	return pair, nil
}

func (*Provider) RenderServer(n domain.NodeSpec) ([]byte, error) {
	v := map[string]any{
		"log": map[string]any{"loglevel": "warning"},
		"inbounds": []any{map[string]any{
			"listen": "0.0.0.0", "port": n.Port, "protocol": "vless",
			"settings": map[string]any{"clients": []any{map[string]any{"id": n.UUID, "flow": domain.VisionFlow}}, "decryption": "none"},
			"streamSettings": map[string]any{"network": "raw", "security": "reality", "realitySettings": map[string]any{
				"show": false, "target": n.Target, "xver": 0, "serverNames": []string{n.SNI}, "privateKey": n.PrivateKey, "shortIds": []string{n.ShortID},
			}},
		}},
		"outbounds": []any{map[string]any{"protocol": "freedom", "tag": "direct"}},
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
		}},
	}
	return jsonutil.Marshal(v)
}

func (*Provider) ValidateConfig(ctx context.Context, r provider.Runner, path string) error {
	if _, err := r.Run(ctx, "xray", "run", "-test", "-config", path); err != nil {
		return fmt.Errorf("Xray 配置校验失败: %w", err)
	}
	return nil
}
