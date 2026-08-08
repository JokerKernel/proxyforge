package mihomo

import (
	"fmt"
	"strings"

	"proxyforge/internal/domain"
)

// RenderClient renders a complete Mihomo/Clash Meta configuration. Legacy
// Dreamacro Clash does not support VLESS REALITY and cannot use this output.
func RenderClient(n domain.NodeSpec) ([]byte, error) {
	for field, value := range map[string]string{
		"core": n.Core, "server": n.Server, "uuid": n.UUID,
		"sni": n.SNI, "public key": n.PublicKey, "short id": n.ShortID,
	} {
		if strings.TrimSpace(value) == "" {
			return nil, fmt.Errorf("生成 Clash 配置: %s 为空", field)
		}
	}
	if n.Port < 1 || n.Port > 65535 {
		return nil, fmt.Errorf("生成 Clash 配置: 端口 %d 无效", n.Port)
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
    reality-opts:
      public-key: %q
      short-id: %q
    encryption: ""

proxy-groups:
  - name: PROXY
    type: select
    proxies:
      - %q

rules:
  - MATCH,PROXY
`, name, n.Server, n.Port, n.UUID, n.SNI, n.PublicKey, n.ShortID, name)
	return []byte(config), nil
}
