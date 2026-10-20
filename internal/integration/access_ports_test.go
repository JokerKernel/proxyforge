package integration

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"testing"
	"time"

	"proxyforge/internal/domain"
	"proxyforge/internal/provider"
	"proxyforge/internal/provider/singbox"
	"proxyforge/internal/provider/xray"
)

func TestRealAccessPolicyPorts(t *testing.T) {
	if os.Getenv("PROXYFORGE_ACCESS_CONNECT_TEST") != "1" {
		t.Skip("设置 PROXYFORGE_ACCESS_CONNECT_TEST=1 启用端口限制本机验收")
	}
	var ports []int
	for i := 0; i < 3; i++ {
		origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "port-ok") }))
		defer origin.Close()
		ports = append(ports, origin.Listener.Addr().(*net.TCPAddr).Port)
	}
	for _, p := range []provider.CoreProvider{xray.New(), singbox.New()} {
		t.Run(p.Name(), func(t *testing.T) {
			binary := testBinary(t, p)
			for _, kind := range []string{"domain", "ip", "cidr", "blacklist"} {
				t.Run(kind, func(t *testing.T) {
					n := tlsTestNode(t)
					n.Core, n.Port = p.Name(), availablePort(t)
					host, other := "allowed.test", "exact.test"
					entryType, value := kind, host
					if kind == "ip" {
						host, value = "127.0.0.1", "127.0.0.1"
					}
					if kind == "cidr" {
						host, value = "127.0.0.1", "127.0.0.1/32"
					}
					if kind == "blacklist" {
						entryType = "domain"
					}
					first := domain.AccessEntry{Type: entryType, Value: value, Ports: strconv.Itoa(ports[0])}
					second := first
					second.Ports = strconv.Itoa(ports[1]) + "-" + strconv.Itoa(ports[1])
					n.AccessPolicy = domain.AccessPolicy{Mode: domain.AccessModeWhitelist, Whitelist: []domain.AccessEntry{
						first, {Type: "domain", Value: other, Ports: strconv.Itoa(ports[0])}, second,
					}}
					loopback := kind == "blacklist"
					if loopback {
						n.AccessPolicy = domain.AccessPolicy{Mode: domain.AccessModeDefault, Blacklist: []domain.AccessEntry{first, second}}
					}
					startTestCore(t, binary, n.Core, accessTestServerConfig(t, p, n, loopback), n.Port)
					clientNode := n
					clientNode.AccessPolicy = domain.AccessPolicy{}
					client, err := p.RenderClient(clientNode)
					if err != nil {
						t.Fatal(err)
					}
					proxyPort := availablePort(t)
					startTestCore(t, binary, n.Core, loopbackTestConfig(t, client, n.Core, true, proxyPort), proxyPort)
					proxy, _ := url.Parse(fmt.Sprintf("http://127.0.0.1:%d", proxyPort))
					transport := &http.Transport{Proxy: http.ProxyURL(proxy), DisableKeepAlives: true}
					defer transport.CloseIdleConnections()
					httpClient := &http.Client{Transport: transport, Timeout: 3 * time.Second}
					for _, destination := range []string{host, other} {
						for i, port := range ports {
							allowed := destination == host && i < 2 || destination == other && i == 0
							if kind == "ip" || kind == "cidr" {
								allowed = i < 2
							}
							if loopback {
								allowed = destination != host || i == 2
							}
							response, err := httpClient.Get(fmt.Sprintf("http://%s:%d", destination, port))
							success := false
							if err == nil {
								body, readErr := io.ReadAll(response.Body)
								response.Body.Close()
								success = readErr == nil && response.StatusCode == 200 && string(body) == "port-ok"
							}
							if success != allowed {
								t.Fatalf("%s:%d allowed=%v success=%v: %v", destination, port, allowed, success, err)
							}
						}
					}
				})
			}
		})
	}
}
