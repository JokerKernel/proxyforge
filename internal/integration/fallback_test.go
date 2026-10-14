package integration

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"proxyforge/internal/domain"
	"proxyforge/internal/provider"
	"proxyforge/internal/provider/singbox"
	"proxyforge/internal/provider/xray"
)

// Exercise the generated internal fallback listener directly so the test
// isolates Host matching from the REALITY authentication handshake.
func TestRealFallbackDomainBoundaries(t *testing.T) {
	if os.Getenv("PROXYFORGE_FALLBACK_CONNECT_TEST") != "1" {
		t.Skip("设置 PROXYFORGE_FALLBACK_CONNECT_TEST=1 启用回落域名边界验收")
	}
	var hits atomic.Int64
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		fmt.Fprint(w, "fallback-ok")
	}))
	defer origin.Close()
	for _, p := range []provider.CoreProvider{xray.New(), singbox.New()} {
		for _, exact := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/exact=%v", p.Name(), exact), func(t *testing.T) {
				binary := testBinary(t, p)
				n := realityTestNode()
				n.Core, n.Port, n.Target = p.Name(), availablePort(t), strings.TrimPrefix(origin.URL, "http://")
				fallbackPort := availablePort(t)
				for fallbackPort == n.Port {
					fallbackPort = availablePort(t)
				}
				if p.Name() == domain.CoreXray {
					n.XrayFallbackGuard, n.XrayFallbackPort = true, fallbackPort
					n.XrayFallbackHTTPDomain, n.XrayFallbackExactDomain = true, exact
				} else {
					n.SingBoxFallbackGuard, n.SingBoxFallbackPort = true, fallbackPort
					n.SingBoxFallbackHTTPDomain, n.SingBoxFallbackExactDomain = true, exact
				}
				config, err := p.RenderServer(n)
				if err != nil {
					t.Fatal(err)
				}
				logPath := startTestCore(t, binary, p.Name(), config, n.Port)
				transport := &http.Transport{DisableKeepAlives: true}
				defer transport.CloseIdleConnections()
				client := &http.Client{Transport: transport, Timeout: time.Second}
				for _, check := range []struct {
					host  string
					allow bool
				}{{"example.com", true}, {"sub.example.com", !exact}, {"evilexample.com", false}, {"example.com.evil.test", false}, {"other.test", false}} {
					t.Run(check.host, func(t *testing.T) {
						before := hits.Load()
						req, err := http.NewRequest(http.MethodGet, fmt.Sprintf("http://127.0.0.1:%d/", fallbackPort), nil)
						if err != nil {
							t.Fatal(err)
						}
						req.Host = check.host
						resp, err := client.Do(req)
						success := false
						if err == nil {
							body, readErr := io.ReadAll(resp.Body)
							resp.Body.Close()
							success = readErr == nil && resp.StatusCode == http.StatusOK && string(body) == "fallback-ok"
						}
						if success != check.allow || (!check.allow && hits.Load() != before) {
							output, _ := os.ReadFile(logPath)
							t.Fatalf("Host=%s allow=%v success=%v error=%v hits=%d: %s", check.host, check.allow, success, err, hits.Load()-before, output)
						}
					})
				}
			})
		}
	}
}
