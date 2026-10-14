package xray

import (
	"encoding/json"
	"fmt"
	"net"
	"strconv"

	"proxyforge/internal/docker/model"
	"proxyforge/internal/docker/xray/accesspolicy"
)

type Codec struct{}

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

type tunnelInbound struct {
	Listen   string           `json:"listen"`
	Port     int              `json:"port"`
	Protocol string           `json:"protocol"`
	Settings tunnelSettings   `json:"settings"`
	Tag      string           `json:"tag"`
	Sniffing sniffingSettings `json:"sniffing"`
}

type tunnelSettings struct {
	Address string `json:"address"`
	Port    int    `json:"port"`
	Network string `json:"network"`
}

type orderedInbound struct {
	Listen         string            `json:"listen"`
	Port           int               `json:"port"`
	Protocol       string            `json:"protocol"`
	Settings       any               `json:"settings"`
	StreamSettings *streamSettings   `json:"streamSettings,omitempty"`
	Tag            string            `json:"tag,omitempty"`
	Sniffing       *sniffingSettings `json:"sniffing,omitempty"`
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

type streamSettings struct {
	Network         string `json:"network"`
	Security        string `json:"security"`
	RealitySettings any    `json:"realitySettings,omitempty"`
	TLSSettings     any    `json:"tlsSettings,omitempty"`
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
	Protocol       string `json:"protocol"`
	Settings       any    `json:"settings"`
	Tag            string `json:"tag"`
	StreamSettings any    `json:"streamSettings,omitempty"`
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
	Protocol    []string `json:"protocol,omitempty"`
	Domain      []string `json:"domain,omitempty"`
	IP          []string `json:"ip,omitempty"`
	OutboundTag string   `json:"outboundTag"`
}

func New() *Codec { return &Codec{} }

func renderServer(n model.Node) ([]byte, error) {
	if n.IsTLS() {
		return renderTLSServer(n)
	}
	if n.XrayFallbackGuard {
		b, err := renderFallbackGuardServer(n)
		if err != nil || (len(n.RelayLinks) == 0 && len(n.LandingAccesses) == 0) {
			return b, err
		}
		return (&Codec{}).PatchLinks(b, model.Node{}, n)
	}
	stream := streamSettings{Network: "raw", Security: "reality", RealitySettings: serverRealitySettings{
		Show: false, Target: n.Target, Xver: 0, ServerNames: []string{n.SNI}, PrivateKey: n.PrivateKey, ShortIDs: []string{n.ShortID},
	}}
	v := xrayConfig{
		Log: map[string]any{"loglevel": "warning"},
		DNS: systemDNS(),
		Inbounds: []any{orderedInbound{
			Listen: "0.0.0.0", Port: n.Port, Protocol: "vless",
			Settings:       vlessInboundSettings{Clients: []vlessInboundUser{{ID: n.UUID, Email: n.UserName, Flow: model.VisionFlow}}, Decryption: "none"},
			StreamSettings: &stream, Tag: n.InboundTag,
		}},
		Outbounds: []xrayOutbound{directOutbound(), blockedOutbound()},
		Routing:   privateNetworkRouting(),
	}
	b, err := marshalXray(v)
	if err != nil || (len(n.RelayLinks) == 0 && len(n.LandingAccesses) == 0) {
		return b, err
	}
	return (&Codec{}).PatchLinks(b, model.Node{}, n)
}

const (
	fallbackGuardInboundTag   = "fallback-in"
	fallbackDirectOutboundTag = "fallback-out"
)

func renderFallbackGuardServer(n model.Node) ([]byte, error) {
	host, rawPort, err := net.SplitHostPort(n.Target)
	if err != nil {
		return nil, fmt.Errorf("解析 REALITY target %s: %w", n.Target, err)
	}
	targetPort, err := strconv.Atoi(rawPort)
	if err != nil {
		return nil, fmt.Errorf("解析 REALITY target 端口 %s: %w", rawPort, err)
	}
	destOverride := []string{"tls"}
	if n.XrayFallbackHTTPDomain {
		destOverride = []string{"http", "tls"}
	}
	tunnel := tunnelInbound{
		Listen: "127.0.0.1", Tag: fallbackGuardInboundTag, Port: n.XrayFallbackPort, Protocol: "tunnel",
		Settings: tunnelSettings{Address: host, Port: targetPort, Network: "tcp"},
		Sniffing: sniffingSettings{Enabled: true, DestOverride: destOverride, RouteOnly: true},
	}
	stream := streamSettings{Network: "raw", Security: "reality", RealitySettings: serverRealitySettings{
		Show: false, Target: net.JoinHostPort("127.0.0.1", strconv.Itoa(n.XrayFallbackPort)), Xver: 0,
		ServerNames: []string{n.SNI}, PrivateKey: n.PrivateKey, ShortIDs: []string{n.ShortID},
	}}
	sniffing := sniffingSettings{Enabled: true, DestOverride: []string{"http", "tls", "quic"}, RouteOnly: true}
	vless := orderedInbound{
		Listen: "0.0.0.0", Port: n.Port, Protocol: "vless",
		Settings:       vlessInboundSettings{Clients: []vlessInboundUser{{ID: n.UUID, Email: n.UserName, Flow: model.VisionFlow}}, Decryption: "none"},
		StreamSettings: &stream, Tag: n.InboundTag, Sniffing: &sniffing,
	}
	routing := privateNetworkRouting()
	fallbackDomain := n.SNI
	if n.XrayFallbackExactDomain {
		fallbackDomain = xrayFullDomain(n.SNI)
	}
	fallbackRules := []routingRule{
		// Without HTTP sniffing, routing uses the configured tunnel
		// destination and does not restrict the plaintext HTTP Host header.
		{InboundTag: []string{fallbackGuardInboundTag}, Domain: []string{fallbackDomain}, OutboundTag: fallbackDirectOutboundTag},
	}
	if n.XrayFallbackHTTPDomain {
		fallbackRules = []routingRule{
			{InboundTag: []string{fallbackGuardInboundTag}, Protocol: []string{"tls"}, Domain: []string{fallbackDomain}, OutboundTag: fallbackDirectOutboundTag},
			{InboundTag: []string{fallbackGuardInboundTag}, Protocol: []string{"http"}, Domain: []string{fallbackDomain}, OutboundTag: fallbackDirectOutboundTag},
		}
	}
	fallbackRules = append(fallbackRules, routingRule{InboundTag: []string{fallbackGuardInboundTag}, OutboundTag: "private-block"})
	routing.Rules = append(fallbackRules, routing.Rules...)
	v := xrayConfig{
		Log:       map[string]any{"loglevel": "warning"},
		DNS:       systemDNS(),
		Inbounds:  []any{tunnel, vless},
		Outbounds: []xrayOutbound{directOutbound(), fallbackDirectOutbound(), blockedOutbound()},
		Routing:   routing,
	}
	return marshalXray(v)
}
func xrayFullDomain(name string) string {
	return "full:" + name
}

func (*Codec) RenderClient(n model.Node) ([]byte, error) {
	n.Core = model.CoreXray
	stream := streamSettings{Network: "raw", Security: "reality", RealitySettings: clientRealitySettings{
		ServerName: n.SNI, Fingerprint: "chrome", Password: n.PublicKey, ShortID: n.ShortID, SpiderX: "/",
	}}
	if n.IsTLS() {
		if n.TLSCertificate == nil || !model.ValidCertificateSHA256(n.TLSCertificate.CertificateSHA256) {
			return nil, fmt.Errorf("TLS 节点缺少合法证书指纹")
		}
		stream.Security, stream.RealitySettings = "tls", nil
		stream.TLSSettings = map[string]any{"serverName": n.SNI, "fingerprint": "chrome", "minVersion": "1.3", "allowInsecure": false, "pinnedPeerCertSha256": n.TLSCertificate.CertificateSHA256}
	}
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
				Address: n.Server, Port: n.Port, Users: []vlessOutboundUser{{ID: n.UUID, Encryption: "none", Flow: model.VisionFlow}},
			}}},
			Tag: "proxy", StreamSettings: &stream,
		}, blockedOutbound()},
		Routing: privateNetworkRouting(),
	}
	if len(n.AccessPolicy.Whitelist) > 0 {
		b, err := json.Marshal(v)
		if err != nil {
			return nil, err
		}
		var root map[string]any
		if err := json.Unmarshal(b, &root); err != nil {
			return nil, err
		}
		accesspolicy.AllowClient(root, n)
		return marshalXray(root)
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
			IP: xrayBlockedDestinations(), OutboundTag: "private-block",
		}},
	}
}

func xrayBlockedDestinations() []string {
	return append([]string{"geoip:private"}, model.BlockedDestinationCIDRs()...)
}

func blockedOutbound() xrayOutbound {
	return xrayOutbound{Protocol: "blackhole", Settings: map[string]any{}, Tag: "private-block"}
}

func directOutbound() xrayOutbound {
	return xrayOutbound{
		Protocol:       "freedom",
		Settings:       freedomDirectSettings(),
		Tag:            "direct",
		StreamSettings: freedomHappyEyeballsStream(),
	}
}

func fallbackDirectOutbound() xrayOutbound {
	return xrayOutbound{
		Protocol:       "freedom",
		Settings:       map[string]any{"domainStrategy": "AsIs"},
		Tag:            fallbackDirectOutboundTag,
		StreamSettings: freedomHappyEyeballsStream(),
	}
}

func renderTLSServer(n model.Node) ([]byte, error) {
	if n.TLSCertificate == nil {
		return nil, fmt.Errorf("TLS 节点缺少证书")
	}
	stream := streamSettings{Network: "raw", Security: "tls", TLSSettings: map[string]any{
		"minVersion": "1.3", "certificates": []any{map[string]any{"certificateFile": n.TLSCertificate.CertificateFile, "keyFile": n.TLSCertificate.KeyFile}},
	}}
	v := xrayConfig{Log: map[string]any{"loglevel": "warning"}, DNS: systemDNS(),
		Inbounds: []any{orderedInbound{Listen: "0.0.0.0", Port: n.Port, Protocol: "vless", Tag: n.InboundTag,
			Settings: vlessInboundSettings{Clients: []vlessInboundUser{{ID: n.UUID, Email: n.UserName, Flow: model.VisionFlow}}, Decryption: "none"}, StreamSettings: &stream}},
		Outbounds: []xrayOutbound{directOutbound(), blockedOutbound()}, Routing: privateNetworkRouting()}
	b, err := marshalXray(v)
	if err != nil || (len(n.RelayLinks) == 0 && len(n.LandingAccesses) == 0) {
		return b, err
	}
	return (&Codec{}).PatchLinks(b, model.Node{}, n)
}
