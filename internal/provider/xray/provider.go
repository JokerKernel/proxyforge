package xray

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"strings"

	"proxyforge/internal/domain"
	"proxyforge/internal/provider"
)

type Provider struct{}

type xrayConfig struct {
	Log       map[string]any  `json:"log"`
	DNS       dnsSettings     `json:"dns"`
	Inbounds  []any           `json:"inbounds"`
	Outbounds []xrayOutbound  `json:"outbounds"`
	Routing   routingSettings `json:"routing"`
}

type dnsSettings struct {
	Servers       []any  `json:"servers"`
	QueryStrategy string `json:"queryStrategy"`
}

type dokodemoInbound struct {
	Listen   string           `json:"listen"`
	Port     int              `json:"port"`
	Protocol string           `json:"protocol"`
	Settings dokodemoSettings `json:"settings"`
	Tag      string           `json:"tag"`
	Sniffing sniffingSettings `json:"sniffing"`
}

type dokodemoSettings struct {
	Address string `json:"address"`
	Port    int    `json:"port"`
	Network string `json:"network"`
}

type orderedInbound struct {
	Listen         string                 `json:"listen"`
	Port           int                    `json:"port"`
	Protocol       string                 `json:"protocol"`
	Settings       any                    `json:"settings"`
	StreamSettings *realityStreamSettings `json:"streamSettings,omitempty"`
	Tag            string                 `json:"tag,omitempty"`
	Sniffing       *sniffingSettings      `json:"sniffing,omitempty"`
}

type vlessInboundSettings struct {
	Clients    []vlessInboundUser `json:"clients"`
	Decryption string             `json:"decryption"`
}

type vlessInboundUser struct {
	ID    string `json:"id"`
	Email string `json:"email"`
	Flow  string `json:"flow"`
}

type sniffingSettings struct {
	Enabled      bool     `json:"enabled"`
	DestOverride []string `json:"destOverride"`
	RouteOnly    bool     `json:"routeOnly"`
}

type realityStreamSettings struct {
	Network         string `json:"network"`
	Security        string `json:"security"`
	RealitySettings any    `json:"realitySettings"`
}

type serverRealitySettings struct {
	Show        bool     `json:"show"`
	Target      string   `json:"target"`
	Xver        int      `json:"xver"`
	ServerNames []string `json:"serverNames"`
	PrivateKey  string   `json:"privateKey"`
	ShortIDs    []string `json:"shortIds"`
}

type clientRealitySettings struct {
	ServerName  string `json:"serverName"`
	Fingerprint string `json:"fingerprint"`
	Password    string `json:"password"`
	ShortID     string `json:"shortId"`
	SpiderX     string `json:"spiderX"`
}

type xrayOutbound struct {
	Protocol       string                 `json:"protocol"`
	Settings       any                    `json:"settings"`
	Tag            string                 `json:"tag"`
	StreamSettings *realityStreamSettings `json:"streamSettings,omitempty"`
}

type vlessOutboundSettings struct {
	VNext []vlessServer `json:"vnext"`
}

type vlessServer struct {
	Address string              `json:"address"`
	Port    int                 `json:"port"`
	Users   []vlessOutboundUser `json:"users"`
}

type vlessOutboundUser struct {
	ID         string `json:"id"`
	Encryption string `json:"encryption"`
	Flow       string `json:"flow"`
}

type routingSettings struct {
	DomainStrategy string        `json:"domainStrategy"`
	Rules          []routingRule `json:"rules"`
}

type routingRule struct {
	InboundTag  []string `json:"inboundTag,omitempty"`
	Domain      []string `json:"domain,omitempty"`
	IP          []string `json:"ip,omitempty"`
	OutboundTag string   `json:"outboundTag"`
}

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
	return []string{
		"/usr/local/etc/xray",
		"/var/log/xray",
		"/etc/systemd/system/xray.service.d/20-proxyforge-user.conf",
		"/usr/lib/sysusers.d/proxyforge-xray.conf",
	}
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
	if n.XrayFallbackGuard {
		return renderFallbackGuardServer(n)
	}
	stream := realityStreamSettings{Network: "raw", Security: "reality", RealitySettings: serverRealitySettings{
		Show: false, Target: n.Target, Xver: 0, ServerNames: []string{n.SNI}, PrivateKey: n.PrivateKey, ShortIDs: []string{n.ShortID},
	}}
	v := xrayConfig{
		Log: map[string]any{"loglevel": "warning"},
		DNS: systemDNS(),
		Inbounds: []any{orderedInbound{
			Listen: "0.0.0.0", Port: n.Port, Protocol: "vless",
			Settings:       vlessInboundSettings{Clients: []vlessInboundUser{{ID: n.UUID, Email: n.UserName, Flow: domain.VisionFlow}}, Decryption: "none"},
			StreamSettings: &stream, Tag: n.InboundTag,
		}},
		Outbounds: []xrayOutbound{directOutbound(), blockedOutbound()},
		Routing:   privateNetworkRouting(),
	}
	return marshalXray(v)
}

const fallbackGuardInboundTag = "dokodemo-in"

func renderFallbackGuardServer(n domain.NodeSpec) ([]byte, error) {
	host, rawPort, err := net.SplitHostPort(n.Target)
	if err != nil {
		return nil, fmt.Errorf("解析 REALITY target %s: %w", n.Target, err)
	}
	targetPort, err := strconv.Atoi(rawPort)
	if err != nil {
		return nil, fmt.Errorf("解析 REALITY target 端口 %s: %w", rawPort, err)
	}
	dokodemo := dokodemoInbound{
		Listen: "127.0.0.1", Tag: fallbackGuardInboundTag, Port: n.XrayFallbackPort, Protocol: "dokodemo-door",
		Settings: dokodemoSettings{Address: host, Port: targetPort, Network: "tcp"},
		Sniffing: sniffingSettings{Enabled: true, DestOverride: []string{"tls"}, RouteOnly: true},
	}
	stream := realityStreamSettings{Network: "raw", Security: "reality", RealitySettings: serverRealitySettings{
		Show: false, Target: net.JoinHostPort("127.0.0.1", strconv.Itoa(n.XrayFallbackPort)), Xver: 0,
		ServerNames: []string{n.SNI}, PrivateKey: n.PrivateKey, ShortIDs: []string{n.ShortID},
	}}
	sniffing := sniffingSettings{Enabled: true, DestOverride: []string{"http", "tls", "quic"}, RouteOnly: true}
	vless := orderedInbound{
		Listen: "0.0.0.0", Port: n.Port, Protocol: "vless",
		Settings:       vlessInboundSettings{Clients: []vlessInboundUser{{ID: n.UUID, Email: n.UserName, Flow: domain.VisionFlow}}, Decryption: "none"},
		StreamSettings: &stream, Tag: n.InboundTag, Sniffing: &sniffing,
	}
	routing := privateNetworkRouting()
	routing.Rules = append([]routingRule{
		{InboundTag: []string{fallbackGuardInboundTag}, Domain: []string{n.SNI}, OutboundTag: "direct"},
		{InboundTag: []string{fallbackGuardInboundTag}, OutboundTag: "blocked-private"},
	}, routing.Rules...)
	v := xrayConfig{
		Log:       map[string]any{"loglevel": "warning"},
		DNS:       systemDNS(),
		Inbounds:  []any{dokodemo, vless},
		Outbounds: []xrayOutbound{directOutbound(), blockedOutbound()},
		Routing:   routing,
	}
	return marshalXray(v)
}

func (*Provider) RenderClient(n domain.NodeSpec) ([]byte, error) {
	stream := realityStreamSettings{Network: "raw", Security: "reality", RealitySettings: clientRealitySettings{
		ServerName: n.SNI, Fingerprint: "chrome", Password: n.PublicKey, ShortID: n.ShortID, SpiderX: "/",
	}}
	v := xrayConfig{
		Log: map[string]any{"loglevel": "warning"},
		DNS: systemDNS(),
		Inbounds: []any{
			orderedInbound{Listen: "127.0.0.1", Port: 10808, Protocol: "socks", Settings: map[string]any{"udp": true}},
			orderedInbound{Listen: "127.0.0.1", Port: 10809, Protocol: "http", Settings: map[string]any{}},
		},
		Outbounds: []xrayOutbound{{
			Protocol: "vless",
			Settings: vlessOutboundSettings{VNext: []vlessServer{{
				Address: n.Server, Port: n.Port, Users: []vlessOutboundUser{{ID: n.UUID, Encryption: "none", Flow: domain.VisionFlow}},
			}}},
			Tag: "proxy", StreamSettings: &stream,
		}, blockedOutbound()},
		Routing: privateNetworkRouting(),
	}
	return marshalXray(v)
}

func systemDNS() dnsSettings {
	return dnsSettings{Servers: []any{"localhost"}, QueryStrategy: "UseIP"}
}

func privateNetworkRouting() routingSettings {
	return routingSettings{
		DomainStrategy: "IPOnDemand",
		Rules: []routingRule{{
			IP: xrayBlockedDestinations(), OutboundTag: "blocked-private",
		}},
	}
}

func xrayBlockedDestinations() []string {
	return append([]string{"geoip:private"}, domain.BlockedDestinationCIDRs()...)
}

func blockedOutbound() xrayOutbound {
	return xrayOutbound{Protocol: "blackhole", Settings: map[string]any{}, Tag: "blocked-private"}
}

func directOutbound() xrayOutbound {
	return xrayOutbound{Protocol: "freedom", Settings: map[string]any{"domainStrategy": "UseIP"}, Tag: "direct"}
}

func (*Provider) ValidateConfig(ctx context.Context, r provider.Runner, path string) error {
	if _, err := r.Run(ctx, "xray", "run", "-test", "-config", path); err != nil {
		return fmt.Errorf("Xray 配置校验失败: %w", err)
	}
	return nil
}
