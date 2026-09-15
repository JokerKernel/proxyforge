package app

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"proxyforge/internal/domain"
	"proxyforge/internal/provider/xray"
)

func TestLandingAndRelayLifecycle(t *testing.T) {
	runner := &fakeRunner{port: freePort(t)}
	a, _ := testApp(t, runner)
	base, err := a.Generate(context.Background(), domain.CoreXray, domain.GenerateOptions{
		Server: "relay.example.com", Port: runner.port, SNI: "relay.example.com", Target: "relay.example.com:443",
		StandardConfig: true, NonInteractive: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	access, err := a.AddLandingAccess(context.Background(), domain.CoreXray, "from-la")
	if err != nil {
		t.Fatal(err)
	}
	if !access.Enabled || access.UUID == "" || access.UserName != "from-la" {
		t.Fatalf("access=%#v", access)
	}
	bundleBytes, err := a.ExportLandingBundle(domain.CoreXray, "from-la", "", false)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(bundleBytes, []byte(base.PrivateKey)) || bytes.Contains(bundleBytes, []byte(`"private_key"`)) {
		t.Fatalf("private key leaked: %s", bundleBytes)
	}
	peer, err := ParseLandingBundle(bundleBytes)
	if err != nil || peer.UUID != access.UUID {
		t.Fatalf("peer=%#v err=%v", peer, err)
	}
	peer.Server = "exit.example.com"
	link, err := a.AddRelayLink(context.Background(), domain.CoreXray, "la", peer, RelayAddOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !link.Enabled || link.UUID == access.UUID || link.UserName != "la" {
		t.Fatalf("link=%#v", link)
	}
	client, err := a.RelayClientConfig(context.Background(), domain.CoreXray, "la", ClientFormatNative, "", false)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(client, []byte(link.UUID)) || bytes.Contains(client, []byte(peer.UUID)) {
		t.Fatalf("unexpected client config: %s", client)
	}

	configPath := a.Layout.Resolve(xray.New().ConfigPath())
	config, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	var root map[string]any
	if err := json.Unmarshal(config, &root); err != nil {
		t.Fatal(err)
	}
	if !xrayConfigHasTag(root, "la-out") {
		t.Fatalf("relay outbound missing: %s", config)
	}
	if err := a.SetRelayLinkEnabled(context.Background(), domain.CoreXray, "la", false); err != nil {
		t.Fatal(err)
	}
	config, _ = os.ReadFile(configPath)
	if xrayConfigContainsTag(config, "la-out") {
		t.Fatalf("disabled link remained in config: %s", config)
	}
	state, err := a.Store.Load(domain.CoreXray)
	if err != nil || len(state.RelayLinks) != 1 || state.RelayLinks[0].Enabled {
		t.Fatalf("state=%#v err=%v", state, err)
	}
}

func TestTLSLandingLifecycleAndPortableBundle(t *testing.T) {
	runner := &fakeRunner{port: freePort(t)}
	a, _ := testApp(t, runner)
	if _, err := a.Generate(context.Background(), domain.CoreXray, domain.GenerateOptions{
		Server: "relay.example.com", Port: runner.port, SNI: "relay.example.com", Target: "relay.example.com:443",
		StandardConfig: true, NonInteractive: true,
	}); err != nil {
		t.Fatal(err)
	}
	access, err := a.AddLandingAccessWithOptions(context.Background(), domain.CoreXray, "tls-exit", LandingAddOptions{
		Security: domain.LandingSecurityTLS,
	})
	if err != nil {
		t.Fatal(err)
	}
	if access.Name != "tls-exit" || access.UserName != "tls-exit" || access.Security != domain.LandingSecurityTLS || access.Port < domain.LandingTLSPortMin || access.Port > domain.LandingTLSPortMax || !regexp.MustCompile(`^pf-[0-9a-f]{24}\.invalid$`).MatchString(access.SNI) {
		t.Fatalf("access=%#v", access)
	}
	certFile, keyFile := access.CertificateFile, access.KeyFile
	if certFile != filepath.Join(a.Layout.TLSAccessDir(domain.CoreXray, access.Name), "cert.pem") || keyFile != filepath.Join(a.Layout.TLSAccessDir(domain.CoreXray, access.Name), "key.pem") {
		t.Fatalf("unexpected managed paths: cert=%s key=%s", certFile, keyFile)
	}
	assertManagedTLSCertificate(t, access, a.Now())
	if info, err := os.Stat(filepath.Dir(certFile)); err != nil || info.Mode().Perm() != 0700 {
		t.Fatalf("unexpected managed TLS directory mode: info=%v err=%v", info, err)
	}
	b, err := a.ExportLandingBundle(domain.CoreXray, access.Name, "", false)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(b, []byte(certFile)) || bytes.Contains(b, []byte(keyFile)) || bytes.Contains(b, []byte("certificate_file")) || bytes.Contains(b, []byte(`"key_file"`)) || bytes.Contains(b, []byte(`"private_key"`)) {
		t.Fatalf("landing-local TLS details leaked: %s", b)
	}
	peer, err := ParseLandingBundle(b)
	if err != nil {
		t.Fatal(err)
	}
	if peer.Security != domain.LandingSecurityTLS || peer.Port != access.Port || peer.SNI != access.SNI || peer.PublicKey != "" || peer.ShortID != "" || peer.CertificateSHA256 != access.CertificateSHA256 || peer.CertificatePublicKeySHA256 != access.CertificatePublicKeySHA256 {
		t.Fatalf("peer=%#v", peer)
	}
	p, _ := a.Registry.Get(domain.CoreXray)
	configPath := a.Layout.Resolve(p.ConfigPath())
	config, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(config, []byte(`"tag": "tls-exit-in"`)) || !bytes.Contains(config, []byte(`"security": "tls"`)) {
		t.Fatalf("TLS landing inbound missing: %s", config)
	}
	if err := a.SetLandingAccessEnabled(context.Background(), domain.CoreXray, access.Name, false); err != nil {
		t.Fatal(err)
	}
	config, _ = os.ReadFile(configPath)
	if bytes.Contains(config, []byte(`"tag": "tls-exit-in"`)) {
		t.Fatalf("disabled TLS landing remained in config: %s", config)
	}
	if _, err := os.Stat(certFile); err != nil {
		t.Fatalf("disabling removed certificate: %v", err)
	}
	if err := a.SetLandingAccessEnabled(context.Background(), domain.CoreXray, access.Name, true); err != nil {
		t.Fatal(err)
	}
	rotated, err := a.RotateLandingAccess(context.Background(), domain.CoreXray, access.Name)
	if err != nil {
		t.Fatal(err)
	}
	if rotated.UUID == access.UUID || rotated.CertificateSHA256 != access.CertificateSHA256 || rotated.CertificatePublicKeySHA256 != access.CertificatePublicKeySHA256 || rotated.CertificateFile != certFile {
		t.Fatalf("UUID rotation changed TLS certificate: before=%#v after=%#v", access, rotated)
	}
	if err := a.RemoveLandingAccess(context.Background(), domain.CoreXray, access.Name); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Dir(certFile)); !os.IsNotExist(err) {
		t.Fatalf("managed certificate directory remained after removal: %v", err)
	}
}

func TestTLSLandingCreationFailureRemovesGeneratedCertificate(t *testing.T) {
	runner := &fakeRunner{port: freePort(t)}
	a, _ := testApp(t, runner)
	if _, err := a.Generate(context.Background(), domain.CoreXray, domain.GenerateOptions{
		Server: "relay.example.com", Port: runner.port, SNI: "relay.example.com", Target: "relay.example.com:443",
		StandardConfig: true, NonInteractive: true,
	}); err != nil {
		t.Fatal(err)
	}
	runner.failRestart = true
	_, err := a.AddLandingAccessWithOptions(context.Background(), domain.CoreXray, "rollback-tls", LandingAddOptions{Security: domain.LandingSecurityTLS})
	if err == nil || !strings.Contains(err.Error(), "已恢复") {
		t.Fatalf("error=%v", err)
	}
	if _, statErr := os.Stat(a.Layout.TLSAccessDir(domain.CoreXray, "rollback-tls")); !os.IsNotExist(statErr) {
		t.Fatalf("generated certificate was not rolled back: %v", statErr)
	}
	state, loadErr := a.Store.Load(domain.CoreXray)
	if loadErr != nil || len(state.LandingAccesses) != 0 {
		t.Fatalf("state retained failed TLS access: %#v err=%v", state.LandingAccesses, loadErr)
	}
}

func TestTLSLandingExportRejectsFingerprintThatDoesNotMatchCertificate(t *testing.T) {
	runner := &fakeRunner{port: freePort(t)}
	a, _ := testApp(t, runner)
	if _, err := a.Generate(context.Background(), domain.CoreXray, domain.GenerateOptions{
		Server: "relay.example.com", Port: runner.port, SNI: "relay.example.com", Target: "relay.example.com:443",
		StandardConfig: true, NonInteractive: true,
	}); err != nil {
		t.Fatal(err)
	}
	access, err := a.AddLandingAccessWithOptions(context.Background(), domain.CoreXray, "tampered-tls", LandingAddOptions{Security: domain.LandingSecurityTLS})
	if err != nil {
		t.Fatal(err)
	}
	state, err := a.Store.Load(domain.CoreXray)
	if err != nil {
		t.Fatal(err)
	}
	state.LandingAccesses[0].CertificateSHA256 = strings.Repeat("0", 64)
	if state.LandingAccesses[0].CertificateSHA256 == access.CertificateSHA256 {
		state.LandingAccesses[0].CertificateSHA256 = strings.Repeat("1", 64)
	}
	if err := a.Store.Save(state); err != nil {
		t.Fatal(err)
	}
	if _, err := a.ExportLandingBundle(domain.CoreXray, access.Name, "", false); err == nil || !strings.Contains(err.Error(), "固定指纹不一致") {
		t.Fatalf("tampered fingerprint error=%v", err)
	}
}

func TestRelayMutationRollsBackConfigAndState(t *testing.T) {
	runner := &fakeRunner{port: freePort(t)}
	a, _ := testApp(t, runner)
	_, err := a.Generate(context.Background(), domain.CoreSingBox, domain.GenerateOptions{
		Server: "relay.example.com", Port: runner.port, SNI: "relay.example.com", Target: "relay.example.com:443",
		StandardConfig: true, NonInteractive: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	peer := domain.LandingPeer{Name: "exit", Core: domain.CoreXray, Server: "exit.example.com", Port: 443, SNI: "exit.example.com",
		UUID: "123e4567-e89b-42d3-a456-426614174000", PublicKey: "public", ShortID: "0123456789abcdef", Flow: domain.VisionFlow}
	if _, err := a.AddRelayLink(context.Background(), domain.CoreSingBox, "la", peer, RelayAddOptions{}); err != nil {
		t.Fatal(err)
	}
	p, _ := a.Registry.Get(domain.CoreSingBox)
	beforeConfig, _ := os.ReadFile(a.Layout.Resolve(p.ConfigPath()))
	beforeState, _ := os.ReadFile(a.Layout.StatePath(domain.CoreSingBox))
	runner.failRestart = true
	err = a.RemoveRelayLink(context.Background(), domain.CoreSingBox, "la")
	if err == nil || !strings.Contains(err.Error(), "已恢复") {
		t.Fatalf("error=%v", err)
	}
	afterConfig, _ := os.ReadFile(a.Layout.Resolve(p.ConfigPath()))
	afterState, _ := os.ReadFile(a.Layout.StatePath(domain.CoreSingBox))
	if !bytes.Equal(beforeConfig, afterConfig) || !bytes.Equal(beforeState, afterState) {
		t.Fatal("rollback did not restore config and state")
	}
}

func TestGeneratePreservesLinksUnlessExplicitlyDropped(t *testing.T) {
	runner := &fakeRunner{port: freePort(t)}
	a, _ := testApp(t, runner)
	opts := domain.GenerateOptions{Server: "relay.example.com", Port: runner.port, SNI: "relay.example.com", Target: "relay.example.com:443", StandardConfig: true, NonInteractive: true}
	if _, err := a.Generate(context.Background(), domain.CoreXray, opts); err != nil {
		t.Fatal(err)
	}
	peer := domain.LandingPeer{Name: "exit", Core: domain.CoreSingBox, Server: "exit.example.com", Port: 443, SNI: "exit.example.com",
		UUID: "123e4567-e89b-42d3-a456-426614174000", PublicKey: "public", ShortID: "0123456789abcdef", Flow: domain.VisionFlow}
	if _, err := a.AddRelayLink(context.Background(), domain.CoreXray, "la", peer, RelayAddOptions{}); err != nil {
		t.Fatal(err)
	}
	tlsAccess, err := a.AddLandingAccessWithOptions(context.Background(), domain.CoreXray, "drop-tls", LandingAddOptions{Security: domain.LandingSecurityTLS})
	if err != nil {
		t.Fatal(err)
	}
	regenerated, err := a.Generate(context.Background(), domain.CoreXray, opts)
	if err != nil {
		t.Fatal(err)
	}
	if len(regenerated.RelayLinks) != 1 || len(regenerated.LandingAccesses) != 1 || regenerated.LandingAccesses[0].CertificateSHA256 != tlsAccess.CertificateSHA256 {
		t.Fatalf("links were not preserved: %#v %#v", regenerated.RelayLinks, regenerated.LandingAccesses)
	}
	opts.DropLinks = true
	dropped, err := a.Generate(context.Background(), domain.CoreXray, opts)
	if err != nil {
		t.Fatal(err)
	}
	if len(dropped.RelayLinks) != 0 || len(dropped.LandingAccesses) != 0 {
		t.Fatalf("links were not dropped: %#v", dropped)
	}
	p, _ := a.Registry.Get(domain.CoreXray)
	config, _ := os.ReadFile(a.Layout.Resolve(p.ConfigPath()))
	if xrayConfigContainsTag(config, "la-out") {
		t.Fatalf("dropped link remained in config: %s", config)
	}
	if _, err := os.Stat(filepath.Dir(tlsAccess.CertificateFile)); !os.IsNotExist(err) {
		t.Fatalf("dropped TLS certificate directory remains: %v", err)
	}
}

func TestParseLandingBundleRejectsInvalidIdentity(t *testing.T) {
	_, err := ParseLandingBundle([]byte(`{"managed_by":"other","schema_version":1,"kind":"proxyforge-landing"}`))
	if err == nil {
		t.Fatal("expected invalid bundle error")
	}
}

func TestParseLandingBundleRejectsTLSWithoutValidFingerprints(t *testing.T) {
	base := `{"managed_by":"proxyforge","schema_version":2,"kind":"proxyforge-landing","peer":{"name":"old-tls","core":"xray","security":"tls","server":"exit.example.com","port":443,"sni":"old.invalid","uuid":"123e4567-e89b-42d3-a456-426614174000","flow":"xtls-rprx-vision"}}`
	if _, err := ParseLandingBundle([]byte(base)); err == nil || !strings.Contains(err.Error(), "删除并重新创建") {
		t.Fatalf("missing fingerprints error=%v", err)
	}
	malformed := strings.Replace(base, `"flow"`, `"certificate_sha256":"xyz","certificate_public_key_sha256":"bad","flow"`, 1)
	if _, err := ParseLandingBundle([]byte(malformed)); err == nil || !strings.Contains(err.Error(), "合法证书指纹") {
		t.Fatalf("malformed fingerprints error=%v", err)
	}
}

func TestLandingPeerAllowsOnlyRequestedTestLAN(t *testing.T) {
	peer := domain.LandingPeer{Name: "test", Core: domain.CoreXray, Server: "192.168.10.20", Port: 443, SNI: "exit.example.com",
		UUID: "123e4567-e89b-42d3-a456-426614174000", PublicKey: "public", ShortID: "0123456789abcdef", Flow: domain.VisionFlow}
	if err := validateLandingPeer(peer); err != nil {
		t.Fatalf("192.168/16 should be allowed for relay testing: %v", err)
	}
	peer.Server = "10.0.0.20"
	if err := validateLandingPeer(peer); err == nil {
		t.Fatal("10/8 should remain blocked")
	}
	peer.Server = "172.16.0.20"
	if err := validateLandingPeer(peer); err == nil {
		t.Fatal("172.16/12 should remain blocked")
	}
}

func TestLinkNameAvailabilityRejectsDuplicatesAndReservedNames(t *testing.T) {
	n := domain.NodeSpec{
		UserName: "one", InboundTag: "xray-one",
		LandingAccesses: []domain.LandingAccess{{Name: "landing-a", UserName: "landing-a"}},
		RelayLinks:      []domain.RelayLink{{Name: "relay-a", UserName: "relay-a"}},
	}
	for _, name := range []string{"one", "XRAY-ONE", "LANDING-A", "relay-a", "direct", "blocked-private", "singbox-fallback-in"} {
		if err := validateAvailableLinkName(n, name); err == nil {
			t.Fatalf("name %q should be rejected", name)
		}
	}
	if err := validateAvailableLinkName(n, "user-choice"); err != nil {
		t.Fatalf("available user name rejected: %v", err)
	}
}

func xrayConfigHasTag(root map[string]any, tag string) bool {
	for _, raw := range root["outbounds"].([]any) {
		if outbound, ok := raw.(map[string]any); ok && outbound["tag"] == tag {
			return true
		}
	}
	return false
}

func xrayConfigContainsTag(config []byte, tag string) bool {
	var root map[string]any
	return json.Unmarshal(config, &root) == nil && xrayConfigHasTag(root, tag)
}

func assertManagedTLSCertificate(t *testing.T, access domain.LandingAccess, now time.Time) {
	t.Helper()
	certPEM, err := os.ReadFile(access.CertificateFile)
	if err != nil {
		t.Fatal(err)
	}
	certBlock, _ := pem.Decode(certPEM)
	if certBlock == nil {
		t.Fatal("certificate PEM missing")
	}
	cert, err := x509.ParseCertificate(certBlock.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	publicKey, ok := cert.PublicKey.(*ecdsa.PublicKey)
	if !ok || publicKey.Curve != elliptic.P256() || cert.VerifyHostname(access.SNI) != nil {
		t.Fatalf("unexpected certificate public key or SAN: %#v", cert)
	}
	if cert.NotAfter.Before(now.AddDate(9, 11, 0)) || cert.NotAfter.After(now.AddDate(10, 0, 1)) {
		t.Fatalf("unexpected validity: %s - %s", cert.NotBefore, cert.NotAfter)
	}
	keyPEM, err := os.ReadFile(access.KeyFile)
	if err != nil {
		t.Fatal(err)
	}
	keyBlock, _ := pem.Decode(keyPEM)
	privateKey, err := x509.ParseECPrivateKey(keyBlock.Bytes)
	if err != nil || !privateKey.PublicKey.Equal(publicKey) {
		t.Fatalf("certificate/key mismatch: %v", err)
	}
	if err := cert.CheckSignature(cert.SignatureAlgorithm, cert.RawTBSCertificate, cert.Signature); err != nil {
		t.Fatalf("certificate is not self-signed: %v", err)
	}
	certificateHash := sha256.Sum256(cert.Raw)
	publicKeyDER, err := x509.MarshalPKIXPublicKey(cert.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	publicKeyHash := sha256.Sum256(publicKeyDER)
	if access.CertificateSHA256 != hex.EncodeToString(certificateHash[:]) || access.CertificatePublicKeySHA256 != base64.StdEncoding.EncodeToString(publicKeyHash[:]) {
		t.Fatal("stored TLS fingerprints do not match certificate")
	}
	for _, path := range []string{access.CertificateFile, access.KeyFile} {
		info, statErr := os.Stat(path)
		if statErr != nil {
			t.Fatalf("stat %s: %v", path, statErr)
		}
		if info.Mode().Perm() != 0600 {
			t.Fatalf("unexpected mode for %s: %v", path, info.Mode().Perm())
		}
	}
}
