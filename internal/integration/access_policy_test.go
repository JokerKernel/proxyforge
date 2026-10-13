package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"proxyforge/internal/domain"
	"proxyforge/internal/provider"
	"proxyforge/internal/provider/accesspolicy"
	"proxyforge/internal/provider/singbox"
	"proxyforge/internal/provider/xray"
)

func TestRealAccessPolicyConfigs(t *testing.T) {
	for _, p := range []provider.CoreProvider{xray.New(), singbox.New()} {
		t.Run(p.Name(), func(t *testing.T) {
			binary := testBinary(t, p)
			n := tlsTestNode(t)
			n.Core = p.Name()
			n.LandingAccesses = []domain.LandingAccess{{Name: "shared", UserName: "shared", UUID: "5d5c5537-56a4-4b1a-a3bf-9e5a1b378769", Security: "tls", Enabled: true}}
			for _, mode := range []string{domain.AccessModeDefault, domain.AccessModeWhitelist} {
				for _, empty := range []bool{true, false} {
					t.Run(fmt.Sprintf("%s-empty=%v", mode, empty), func(t *testing.T) {
						n.AccessPolicy = domain.AccessPolicy{Mode: mode}
						if !empty {
							n.AccessPolicy.Whitelist = []domain.AccessEntry{{Type: "domain", Value: "allowed.test", IncludeSubdomains: true}, {Type: "ip", Value: "192.168.1.10"}, {Type: "cidr", Value: "2001:db8::/32"}}
							n.AccessPolicy.Blacklist = []domain.AccessEntry{{Type: "domain", Value: "denied.test", IncludeSubdomains: true}, {Type: "cidr", Value: "192.168.2.0/24"}}
						}
						for name, render := range map[string]func(domain.NodeSpec) ([]byte, error){"server": p.RenderServer, "client": p.RenderClient} {
							b, err := render(n)
							if err != nil {
								t.Fatal(err)
							}
							path := filepath.Join(t.TempDir(), name+".json")
							if err := os.WriteFile(path, b, 0600); err != nil {
								t.Fatal(err)
							}
							if err := p.ValidateConfig(context.Background(), binaryRunner{names: map[string]string{p.Binary(): binary}}, path); err != nil {
								t.Fatal(err)
							}
						}
					})
				}
			}
		})
	}
}

func accessTestServerConfig(t *testing.T, p provider.CoreProvider, n domain.NodeSpec, loopback bool) []byte {
	t.Helper()
	base := n
	base.AccessPolicy = domain.AccessPolicy{}
	b, err := p.RenderServer(base)
	if err != nil {
		t.Fatal(err)
	}
	var root map[string]any
	if err := json.Unmarshal(b, &root); err != nil {
		t.Fatal(err)
	}
	hosts := map[string][]string{}
	for _, host := range []string{"allowed.test", "sub.allowed.test", "deep.sub.allowed.test", "evilallowed.test", "allowed.test.evil.test", "exact.test", "sub.exact.test", "denied.test", "sub.denied.test", "outside.test"} {
		hosts[host] = []string{"127.0.0.1"}
	}
	if p.Name() == domain.CoreXray {
		root["dns"].(map[string]any)["hosts"] = hosts
		root["log"].(map[string]any)["loglevel"] = "debug"
		if loopback {
			root["routing"].(map[string]any)["rules"] = []any{}
		}
	} else {
		root["dns"] = map[string]any{"servers": []any{map[string]any{"type": "hosts", "tag": "test-hosts", "predefined": hosts}}}
		route := root["route"].(map[string]any)
		route["default_domain_resolver"] = "test-hosts"
		for _, item := range route["rules"].([]any) {
			rule := item.(map[string]any)
			if rule["action"] == "resolve" {
				rule["server"] = "test-hosts"
			}
		}
		if loopback {
			route["rules"] = []any{}
		}
	}
	b, err = json.Marshal(root)
	if err != nil {
		t.Fatal(err)
	}
	n.AccessPolicySniffing, err = accesspolicy.SnapshotSniffing(b, n)
	if err != nil {
		t.Fatal(err)
	}
	b, err = p.(provider.AccessPolicyProvider).PatchAccessPolicy(b, base, n)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestRealAccessPolicyDestinations(t *testing.T) {
	if os.Getenv("PROXYFORGE_ACCESS_CONNECT_TEST") != "1" {
		t.Skip("设置 PROXYFORGE_ACCESS_CONNECT_TEST=1 启用访问控制本机验收")
	}
	var hits atomic.Int64
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1); fmt.Fprint(w, "access-policy-ok") })
	origin := httptest.NewServer(handler)
	defer origin.Close()
	second := httptest.NewUnstartedServer(handler)
	ln, err := net.Listen("tcp4", "127.0.0.2:0")
	if err != nil {
		t.Fatal(err)
	}
	second.Listener.Close()
	second.Listener = ln
	second.Start()
	defer second.Close()
	originPort := origin.Listener.Addr().(*net.TCPAddr).Port
	secondPort := second.Listener.Addr().(*net.TCPAddr).Port
	type request struct {
		host        string
		allow       bool
		payloadHost string
	}
	tests := []struct {
		name     string
		policy   domain.AccessPolicy
		loopback bool
		requests []request
	}{
		{"domain-and-subdomains", domain.AccessPolicy{Mode: "whitelist", Whitelist: []domain.AccessEntry{{Type: "domain", Value: "allowed.test", IncludeSubdomains: true}}}, false, []request{{"allowed.test", true, ""}, {"sub.allowed.test", true, ""}, {"deep.sub.allowed.test", true, ""}, {"evilallowed.test", false, ""}, {"allowed.test.evil.test", false, ""}, {"outside.test", false, ""}, {"127.0.0.1", false, "allowed.test"}}},
		{"exact-domain", domain.AccessPolicy{Mode: "whitelist", Whitelist: []domain.AccessEntry{{Type: "domain", Value: "exact.test"}}}, false, []request{{"exact.test", true, ""}, {"sub.exact.test", false, ""}}},
		{"ip-white-over-black-and-private", domain.AccessPolicy{Mode: "default", Whitelist: []domain.AccessEntry{{Type: "ip", Value: "127.0.0.1"}}, Blacklist: []domain.AccessEntry{{Type: "cidr", Value: "127.0.0.0/8"}}}, false, []request{{"127.0.0.1", true, ""}, {"outside.test", true, ""}, {"127.0.0.2", false, ""}}},
		{"cidr-white-over-private", domain.AccessPolicy{Mode: "whitelist", Whitelist: []domain.AccessEntry{{Type: "cidr", Value: "127.0.0.0/8"}}}, false, []request{{"127.0.0.1", true, ""}, {"127.0.0.2", true, ""}}},
		{"default-blacklist", domain.AccessPolicy{Mode: "default", Blacklist: []domain.AccessEntry{{Type: "domain", Value: "denied.test", IncludeSubdomains: true}}}, true, []request{{"outside.test", true, ""}, {"denied.test", false, ""}, {"sub.denied.test", false, ""}}},
		{"default-white-over-black-and-private", domain.AccessPolicy{Mode: "default", Whitelist: []domain.AccessEntry{{Type: "ip", Value: "127.0.0.1"}}, Blacklist: []domain.AccessEntry{{Type: "cidr", Value: "127.0.0.0/8"}}}, false, []request{{"127.0.0.1", true, ""}, {"127.0.0.2", false, ""}, {"outside.test", true, ""}}},
		{"domain-white-over-black", domain.AccessPolicy{Mode: "default", Whitelist: []domain.AccessEntry{{Type: "domain", Value: "allowed.test", IncludeSubdomains: true}}, Blacklist: []domain.AccessEntry{{Type: "domain", Value: "allowed.test", IncludeSubdomains: true}}}, false, []request{{"allowed.test", true, ""}, {"sub.allowed.test", true, ""}, {"outside.test", false, ""}}},
		{"empty-whitelist-denies-all", domain.AccessPolicy{Mode: "whitelist"}, true, []request{{"127.0.0.1", false, ""}, {"outside.test", false, ""}}},
		{"disabled-whitelist-ignores-lists", domain.AccessPolicy{Disabled: true, Mode: "whitelist", Whitelist: []domain.AccessEntry{{Type: "domain", Value: "allowed.test", IncludeSubdomains: true}}, Blacklist: []domain.AccessEntry{{Type: "domain", Value: "denied.test", IncludeSubdomains: true}}}, true, []request{{"allowed.test", true, ""}, {"outside.test", true, ""}, {"denied.test", true, ""}}},
		{"disabled-empty-whitelist-allows", domain.AccessPolicy{Disabled: true, Mode: "whitelist"}, true, []request{{"127.0.0.1", true, ""}, {"outside.test", true, ""}}},
		{"disabled-policy-still-blocks-private", domain.AccessPolicy{Disabled: true, Mode: "whitelist", Whitelist: []domain.AccessEntry{{Type: "ip", Value: "127.0.0.1"}}}, false, []request{{"127.0.0.1", false, ""}}},
		{"default-still-blocks-private", domain.AccessPolicy{}, false, []request{{"127.0.0.1", false, ""}, {"outside.test", false, ""}}},
	}
	for _, p := range []provider.CoreProvider{xray.New(), singbox.New()} {
		t.Run(p.Name(), func(t *testing.T) {
			binary := testBinary(t, p)
			for _, test := range tests {
				t.Run(test.name, func(t *testing.T) {
					n := tlsTestNode(t)
					n.Core = p.Name()
					n.Port = availablePort(t)
					n.AccessPolicy = test.policy
					serverLog := startTestCore(t, binary, n.Core, accessTestServerConfig(t, p, n, test.loopback), n.Port)
					// Client routing is unrestricted here, isolating the server's decisions.
					clientNode := n
					clientNode.AccessPolicy = domain.AccessPolicy{}
					client, err := p.RenderClient(clientNode)
					if err != nil {
						t.Fatal(err)
					}
					proxyPort := availablePort(t)
					clientLog := startTestCore(t, binary, n.Core, loopbackTestConfig(t, client, n.Core, true, proxyPort), proxyPort)
					proxy, _ := url.Parse(fmt.Sprintf("http://127.0.0.1:%d", proxyPort))
					transport := &http.Transport{Proxy: http.ProxyURL(proxy)}
					defer transport.CloseIdleConnections()
					httpClient := &http.Client{Transport: transport, Timeout: 3 * time.Second}
					for _, item := range test.requests {
						t.Run(item.host+item.payloadHost, func(t *testing.T) {
							port := originPort
							if item.host == "127.0.0.2" {
								port = secondPort
							}
							target := fmt.Sprintf("http://%s:%d", item.host, port)
							req, err := http.NewRequest("GET", target, nil)
							if err != nil {
								t.Fatal(err)
							}
							if item.payloadHost != "" {
								req.Host = item.payloadHost
							}
							before := hits.Load()
							response, err := httpClient.Do(req)
							success := false
							if err == nil {
								body, readErr := io.ReadAll(response.Body)
								response.Body.Close()
								success = readErr == nil && response.StatusCode == 200 && string(body) == "access-policy-ok"
							}
							if success != item.allow || (!item.allow && hits.Load() != before) {
								serverOutput, _ := os.ReadFile(serverLog)
								clientOutput, _ := os.ReadFile(clientLog)
								t.Fatalf("target=%s allow=%v success=%v err=%v\nserver=%s\nclient=%s", target, item.allow, success, err, serverOutput, clientOutput)
							}
						})
					}
				})
			}
		})
	}
}
