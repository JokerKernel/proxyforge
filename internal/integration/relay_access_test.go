package integration

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"proxyforge/internal/domain"
	"proxyforge/internal/provider"
	"proxyforge/internal/provider/singbox"
	"proxyforge/internal/provider/xray"
)

func TestRealRelayAccessPoliciesAreIndependent(t *testing.T) {
	if os.Getenv("PROXYFORGE_ACCESS_CONNECT_TEST") != "1" {
		t.Skip("设置 PROXYFORGE_ACCESS_CONNECT_TEST=1 启用中转访问控制本机验收")
	}
	var hits atomic.Int64
	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { hits.Add(1); fmt.Fprint(w, "relay-policy-ok") })
	origins := []*httptest.Server{httptest.NewServer(handler), httptest.NewServer(handler)}
	for _, origin := range origins {
		defer origin.Close()
	}
	ports := []int{origins[0].Listener.Addr().(*net.TCPAddr).Port, origins[1].Listener.Addr().(*net.TCPAddr).Port}
	for _, p := range []provider.CoreProvider{xray.New(), singbox.New()} {
		t.Run(p.Name(), func(t *testing.T) {
			for _, scenario := range []string{"isolated", "relay-only", "private-blocks"} {
				t.Run(scenario, func(t *testing.T) {
					binary := testBinary(t, p)
					landing := tlsTestNode(t)
					landing.Core, landing.Port = p.Name(), availablePort(t)
					landing.AccessPolicy = domain.AccessPolicy{Mode: domain.AccessModeWhitelist, Whitelist: []domain.AccessEntry{{Type: "cidr", Value: "127.0.0.0/8"}}}
					landingLog := startTestCore(t, binary, landing.Core, accessTestServerConfig(t, p, landing, false), landing.Port)
					n := tlsTestNode(t)
					n.Core, n.Port = p.Name(), availablePort(t)
					n.AccessPolicy = domain.AccessPolicy{Disabled: scenario == "relay-only", Mode: domain.AccessModeWhitelist, Whitelist: []domain.AccessEntry{{Type: "domain", Value: "allowed.test", Ports: strconv.Itoa(ports[0])}}}
					policies := []*domain.AccessPolicy{
						{Mode: domain.AccessModeWhitelist, Whitelist: []domain.AccessEntry{{Type: "domain", Value: "exact.test", Ports: strconv.Itoa(ports[0])}, {Type: "ip", Value: "127.0.0.1", Ports: strconv.Itoa(ports[1])}}},
						{Mode: domain.AccessModeDefault, Whitelist: []domain.AccessEntry{{Type: "domain", Value: "allowed.test", Ports: strconv.Itoa(ports[1])}}, Blacklist: []domain.AccessEntry{{Type: "cidr", Value: "127.0.0.0/8", Ports: strconv.Itoa(ports[0])}, {Type: "domain", Value: "allowed.test", Ports: strconv.Itoa(ports[1])}}},
						{Disabled: true, Mode: domain.AccessModeWhitelist},
						{Mode: domain.AccessModeWhitelist},
						{Disabled: true},
					}
					for i, policy := range policies {
						peer := domain.LandingPeer{Core: p.Name(), Security: "tls", Server: "127.0.0.1", Port: landing.Port,
							SNI: landing.SNI, UUID: landing.UUID, Flow: domain.VisionFlow,
							CertificateSHA256: landing.TLSCertificate.CertificateSHA256, CertificatePublicKeySHA256: landing.TLSCertificate.CertificatePublicKeySHA256}
						if i == 4 {
							peer.Port = availablePort(t) // Must fail instead of using the node's direct whitelist.
						}
						name := fmt.Sprintf("relay-%d", i)
						n.RelayLinks = append(n.RelayLinks, domain.RelayLink{Name: name, UserName: name, UUID: fmt.Sprintf("11b50d17-8c92-4b5d-9449-245f3b70d7b%d", i), Enabled: true, Upstream: peer, AccessPolicy: policy})
					}
					// Permit loopback in the original relay routing so default/disabled
					// access modes can be tested separately from private-network blocks.
					base := n.WithoutAccessPolicies()
					config, err := p.RenderServer(base)
					if err != nil {
						t.Fatal(err)
					}
					var root map[string]any
					if err := json.Unmarshal(config, &root); err != nil {
						t.Fatal(err)
					}
					hosts := map[string][]string{"allowed.test": {"127.0.0.1"}, "exact.test": {"127.0.0.1"}, "outside.test": {"127.0.0.1"}}
					key := "route"
					if p.Name() == domain.CoreXray {
						key = "routing"
						root["dns"].(map[string]any)["hosts"] = hosts
					} else {
						root["dns"] = map[string]any{"servers": []any{map[string]any{"type": "hosts", "tag": "test-hosts", "predefined": hosts}}}
						root[key].(map[string]any)["default_domain_resolver"] = "test-hosts"
					}
					route := root[key].(map[string]any)
					var original []any
					for _, item := range route["rules"].([]any) {
						rule := item.(map[string]any)
						if scenario != "private-blocks" && (rule["outboundTag"] == "private-block" || rule["action"] == "reject") {
							continue
						}
						if rule["action"] == "resolve" {
							rule["server"] = "test-hosts"
						}
						original = append(original, rule)
					}
					route["rules"] = original
					config, _ = json.Marshal(root)
					config, err = p.(provider.AccessPolicyProvider).PatchAccessPolicy(config, base, n)
					if err != nil {
						t.Fatal(err)
					}
					serverLog := startTestCore(t, binary, n.Core, config, n.Port)
					cases := [][]struct {
						host  string
						port  int
						allow bool
					}{
						{{"allowed.test", 0, true}, {"exact.test", 0, false}}, // Ordinary user.
						{{"exact.test", 0, true}, {"allowed.test", 0, false}, {"outside.test", 1, true}, {"outside.test", 0, false}},
						{{"allowed.test", 0, false}, {"outside.test", 1, true}, {"allowed.test", 1, true}},
						{{"exact.test", 0, true}, {"allowed.test", 0, true}}, // Disabled policy still uses relay.
						{{"allowed.test", 0, false}},                         // Empty manual whitelist.
						{{"allowed.test", 0, false}},                         // Dead relay cannot fall through to direct.
					}
					if scenario == "private-blocks" {
						cases[2][1].allow = false
						cases[3][0].allow, cases[3][1].allow = false, false
					}
					for i, requests := range cases {
						if i == 0 && scenario == "relay-only" {
							continue
						}
						t.Run(fmt.Sprintf("identity-%d", i), func(t *testing.T) {
							clientNode := n.WithoutAccessPolicies()
							if i > 0 {
								clientNode.UUID, clientNode.UserName = n.RelayLinks[i-1].UUID, n.RelayLinks[i-1].UserName
							}
							client, err := p.RenderClient(clientNode)
							if err != nil {
								t.Fatal(err)
							}
							proxyPort := availablePort(t)
							clientLog := startTestCore(t, binary, n.Core, loopbackTestConfig(t, client, n.Core, true, proxyPort), proxyPort)
							proxy, _ := url.Parse(fmt.Sprintf("http://127.0.0.1:%d", proxyPort))
							transport := &http.Transport{Proxy: http.ProxyURL(proxy), DisableKeepAlives: true}
							defer transport.CloseIdleConnections()
							httpClient := &http.Client{Transport: transport, Timeout: 2 * time.Second}
							for _, request := range requests {
								before := hits.Load()
								response, err := httpClient.Get(fmt.Sprintf("http://%s:%d", request.host, ports[request.port]))
								success := false
								if err == nil {
									body, readErr := io.ReadAll(response.Body)
									response.Body.Close()
									success = readErr == nil && response.StatusCode == 200 && string(body) == "relay-policy-ok"
								}
								if success != request.allow || !request.allow && hits.Load() != before {
									serverOutput, _ := os.ReadFile(serverLog)
									clientOutput, _ := os.ReadFile(clientLog)
									landingOutput, _ := os.ReadFile(landingLog)
									t.Fatalf("request=%+v success=%v err=%v\nserver=%s\nclient=%s\nlanding=%s", request, success, err, serverOutput, clientOutput, landingOutput)
								}
							}
						})
					}
				})
			}
		})
	}
}
