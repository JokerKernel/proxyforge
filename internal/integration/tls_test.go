package integration

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"proxyforge/internal/domain"
	"proxyforge/internal/provider"
	"proxyforge/internal/provider/mihomo"
	"proxyforge/internal/provider/singbox"
	"proxyforge/internal/provider/xray"
)

func tlsTestNode(t *testing.T) domain.NodeSpec {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "pf-integration.invalid"}, DNSNames: []string{"pf-integration.invalid"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	pubDER, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	certHash, pubHash := sha256.Sum256(der), sha256.Sum256(pubDER)
	dir := t.TempDir()
	certFile, keyFile := filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem")
	if err := os.WriteFile(certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0600); err != nil {
		t.Fatal(err)
	}
	return domain.NodeSpec{Security: "tls", InboundTag: "integration-in", Server: "127.0.0.1", Port: 443, SNI: template.DNSNames[0], UserName: "one", UUID: "123e4567-e89b-42d3-a456-426614174000", TLSCertificate: &domain.LandingAccess{CertificateFile: certFile, KeyFile: keyFile, SNI: template.DNSNames[0], CertificateSHA256: hex.EncodeToString(certHash[:]), CertificatePublicKeySHA256: base64.StdEncoding.EncodeToString(pubHash[:])}}
}

func testBinary(t *testing.T, p provider.CoreProvider) string {
	t.Helper()
	binary := os.Getenv(envName(p.Name()))
	if binary == "" {
		binary, _ = exec.LookPath(p.Binary())
	}
	if binary == "" {
		t.Skipf("设置 %s 启用真实 TLS 内核测试", envName(p.Name()))
	}
	return binary
}

func TestRealBinariesValidateTLSConfigs(t *testing.T) {
	for _, p := range []provider.CoreProvider{xray.New(), singbox.New()} {
		t.Run(p.Name(), func(t *testing.T) {
			binary := testBinary(t, p)
			n := tlsTestNode(t)
			n.Core = p.Name()
			n.LandingAccesses = []domain.LandingAccess{{Name: "shared", UserName: "shared", UUID: "5d5c5537-56a4-4b1a-a3bf-9e5a1b378769", Security: "tls", Enabled: true}}
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

func availablePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	return port
}

// Loopback is allowed only in the test configuration so the origin needs no network access.
func loopbackTestConfig(t *testing.T, b []byte, core string, client bool, port int) []byte {
	t.Helper()
	var root map[string]any
	if err := json.Unmarshal(b, &root); err != nil {
		t.Fatal(err)
	}
	if core == domain.CoreXray {
		root["routing"].(map[string]any)["rules"] = []any{}
		root["log"].(map[string]any)["loglevel"] = "debug"
	} else {
		root["route"].(map[string]any)["rules"] = []any{}
	}
	if client {
		if core == domain.CoreXray {
			root["inbounds"] = []any{map[string]any{"listen": "127.0.0.1", "port": port, "protocol": "http", "settings": map[string]any{}}}
		} else {
			root["inbounds"].([]any)[0].(map[string]any)["listen_port"] = port
		}
	}
	b, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func startTestCore(t *testing.T, binary, core string, config []byte, port int) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, config, 0600); err != nil {
		t.Fatal(err)
	}
	args := []string{"run", "-c", path}
	if core == domain.CoreXray {
		args = []string{"run", "-config", path}
	}
	if core == "mihomo" {
		args = []string{"-d", dir, "-f", path}
	}
	logPath := filepath.Join(dir, "process.log")
	log, err := os.Create(logPath)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(binary, args...)
	cmd.Stdout, cmd.Stderr = log, log
	if err := cmd.Start(); err != nil {
		log.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait(); _ = log.Close() })
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 100*time.Millisecond)
		if err == nil {
			conn.Close()
			// Wait for configuration initialization as well as the listener.
			if core == "mihomo" {
				output, _ := os.ReadFile(logPath)
				if !strings.Contains(string(output), "Initial configuration complete") {
					time.Sleep(30 * time.Millisecond)
					continue
				}
			}
			return logPath
		}
		time.Sleep(30 * time.Millisecond)
	}
	output, _ := os.ReadFile(logPath)
	t.Fatalf("%s 未监听端口: %s", core, output)
	return logPath
}

func TestRealTLSClientsConnectAndRejectWrongPins(t *testing.T) {
	if os.Getenv("PROXYFORGE_TLS_CONNECT_TEST") != "1" {
		t.Skip("设置 PROXYFORGE_TLS_CONNECT_TEST=1 启用本机连接验收")
	}
	origin := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "proxyforge-tls-vision-ok") }))
	origin.TLS = &tls.Config{MinVersion: tls.VersionTLS13}
	origin.StartTLS()
	defer origin.Close()
	originRoots := x509.NewCertPool()
	originRoots.AddCert(origin.Certificate())
	for _, serverProvider := range []provider.CoreProvider{xray.New(), singbox.New()} {
		t.Run(serverProvider.Name(), func(t *testing.T) {
			serverBinary := testBinary(t, serverProvider)
			n := tlsTestNode(t)
			n.Core = serverProvider.Name()
			n.Port = availablePort(t)
			server, err := serverProvider.RenderServer(n)
			if err != nil {
				t.Fatal(err)
			}
			serverLog := startTestCore(t, serverBinary, n.Core, loopbackTestConfig(t, server, n.Core, false, 0), n.Port)
			clients := []struct {
				core string
				p    provider.CoreProvider
			}{{domain.CoreXray, xray.New()}, {domain.CoreSingBox, singbox.New()}, {"mihomo", nil}}
			for _, client := range clients {
				t.Run(client.core, func(t *testing.T) {
					binary := os.Getenv("MIHOMO_BIN")
					if client.p != nil {
						binary = testBinary(t, client.p)
					} else if binary == "" {
						binary, _ = exec.LookPath("mihomo")
					}
					if binary == "" {
						t.Skip("设置 MIHOMO_BIN 启用 Mihomo 验收")
					}
					for _, wrongPin := range []bool{false, true} {
						t.Run(fmt.Sprintf("wrong-pin=%v", wrongPin), func(t *testing.T) {
							candidate := n
							cert := *n.TLSCertificate
							candidate.TLSCertificate = &cert
							if wrongPin {
								cert.CertificateSHA256 = strings.Repeat("0", 64)
								cert.CertificatePublicKeySHA256 = base64.StdEncoding.EncodeToString(make([]byte, 32))
							}
							port := availablePort(t)
							var b []byte
							var err error
							if client.p != nil {
								b, err = client.p.RenderClient(candidate)
								if err == nil {
									b = loopbackTestConfig(t, b, client.core, true, port)
								}
							} else {
								b, err = mihomo.RenderClient(candidate)
								b = []byte(strings.Replace(string(b), "mixed-port: 7890", fmt.Sprintf("mixed-port: %d", port), 1))
								b = []byte(strings.Replace(string(b), "log-level: info", "log-level: debug", 1))
							}
							if err != nil {
								t.Fatal(err)
							}
							clientLog := startTestCore(t, binary, client.core, b, port)
							proxy, _ := url.Parse(fmt.Sprintf("http://127.0.0.1:%d", port))
							transport := &http.Transport{Proxy: http.ProxyURL(proxy), TLSClientConfig: &tls.Config{RootCAs: originRoots}}
							defer transport.CloseIdleConnections()
							httpClient := &http.Client{Transport: transport, Timeout: 4 * time.Second}
							serverBefore, _ := os.ReadFile(serverLog)
							var resp *http.Response
							success, verified := false, false
							deadline := time.Now().Add(5 * time.Second)
							for time.Now().Before(deadline) {
								resp, err = httpClient.Get(origin.URL)
								success = false
								if err == nil {
									body, readErr := io.ReadAll(resp.Body)
									resp.Body.Close()
									success = readErr == nil && resp.StatusCode == 200 && string(body) == "proxyforge-tls-vision-ok"
								}
								if !wrongPin && success {
									verified = true
									break
								}
								if wrongPin {
									if success {
										break
									}
									serverAfter, _ := os.ReadFile(serverLog)
									if len(serverAfter) > len(serverBefore) && strings.Contains(string(serverAfter[len(serverBefore):]), "bad certificate") {
										verified = true
										break
									}
								}
								transport.CloseIdleConnections()
								time.Sleep(50 * time.Millisecond)
							}
							if !verified {
								serverOutput, _ := os.ReadFile(serverLog)
								clientOutput, _ := os.ReadFile(clientLog)
								t.Fatalf("wrongPin=%v success=%v error=%v response=%v\nserver=%s\nclient=%s", wrongPin, success, err, resp, serverOutput, clientOutput)
							}
						})
					}
				})
			}
		})
	}
}
