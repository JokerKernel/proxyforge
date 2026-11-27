package mihomo

import (
	"fmt"
	"strings"

	"proxyforge/internal/domain"
)

// RenderClient renders a complete Mihomo/Clash Meta configuration. Legacy
// Dreamacro Clash does not support VLESS REALITY and cannot use this output.
func RenderClient(n domain.NodeSpec) ([]byte, error) {
	fields := map[string]string{
		"core": n.Core, "server": n.Server, "uuid": n.UUID,
		"sni": n.SNI, "public key": n.PublicKey, "short id": n.ShortID,
	}
	if n.IsTLS() {
		delete(fields, "public key")
		delete(fields, "short id")
	}
	for field, value := range fields {
		if strings.TrimSpace(value) == "" {
			return nil, fmt.Errorf("生成 Clash 配置: %s 为空", field)
		}
	}
	if n.Port < 1 || n.Port > 65535 {
		return nil, fmt.Errorf("生成 Clash 配置: 端口 %d 无效", n.Port)
	}

	securityConfig := fmt.Sprintf("    reality-opts:\n      public-key: %q\n      short-id: %q\n      support-x25519mlkem768: true\n", n.PublicKey, n.ShortID)
	if n.IsTLS() {
		if n.TLSCertificate == nil || !domain.ValidCertificateSHA256(n.TLSCertificate.CertificateSHA256) {
			return nil, fmt.Errorf("TLS 节点缺少合法证书指纹")
		}
		securityConfig = fmt.Sprintf("    skip-cert-verify: false\n    fingerprint: %q\n", n.TLSCertificate.CertificateSHA256)
	}
	name := "proxyforge-" + n.Core
	config := fmt.Sprintf(`mixed-port: 7890
allow-lan: false
mode: rule
log-level: info
ipv6: true

proxies:
  - name: %q
    type: vless
    server: %q
    port: %d
    uuid: %q
    network: tcp
    udp: true
    tls: true
    servername: %q
    flow: xtls-rprx-vision
    packet-encoding: xudp
    client-fingerprint: chrome
%s    encryption: ""

proxy-groups:
  - name: PROXY
    type: select
    proxies:
      - %q

rules:
  - MATCH,PROXY
`, name, n.Server, n.Port, n.UUID, n.SNI, securityConfig, name)
	return []byte(config), nil
}
