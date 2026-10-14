package docker

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"proxyforge/internal/docker/model"
)

var namePattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,47}$`)
var uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

func randomHex(size int) (string, error) {
	b := make([]byte, size)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
func newUUID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	b[6] = b[6]&15 | 64
	b[8] = b[8]&63 | 128
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[:4], b[4:6], b[6:8], b[8:10], b[10:]), nil
}
func (m *Manager) newCertificate(name string) (model.LandingAccess, error) {
	if !namePattern.MatchString(name) {
		return model.LandingAccess{}, fmt.Errorf("证书名称无效")
	}
	dir := filepath.Join(m.paths.TLS(), name)
	if _, err := os.Lstat(dir); !os.IsNotExist(err) {
		return model.LandingAccess{}, fmt.Errorf("证书目录已存在或不可访问")
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return model.LandingAccess{}, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return model.LandingAccess{}, err
	}
	suffix, err := randomHex(12)
	if err != nil {
		return model.LandingAccess{}, err
	}
	sni := "pf-" + suffix + ".invalid"
	now := m.now().UTC()
	tmpl := x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: sni, Organization: []string{"ProxyForge Docker"}}, DNSNames: []string{sni}, NotBefore: now.Add(-5 * time.Minute), NotAfter: now.AddDate(10, 0, 0), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &key.PublicKey, key)
	if err != nil {
		return model.LandingAccess{}, err
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return model.LandingAccess{}, err
	}
	pubDER, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		return model.LandingAccess{}, err
	}
	if err = privateDir(dir); err != nil {
		return model.LandingAccess{}, err
	}
	ok := false
	defer func() {
		if !ok {
			_ = os.RemoveAll(dir)
		}
	}()
	certFile, keyFile := filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem")
	if err = atomicWrite(certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0600); err != nil {
		return model.LandingAccess{}, err
	}
	if err = atomicWrite(keyFile, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0600); err != nil {
		return model.LandingAccess{}, err
	}
	pubHash := sha256.Sum256(pubDER)
	ok = true
	return model.LandingAccess{Name: name, Security: "tls", SNI: sni, CertificateFile: certFile, KeyFile: keyFile, CertificateSHA256: digest(der), CertificatePublicKeySHA256: base64.StdEncoding.EncodeToString(pubHash[:]), UpdatedAt: now, Enabled: true}, nil
}
func (m *Manager) validateCertificate(a model.LandingAccess) error {
	if !namePattern.MatchString(a.Name) || filepath.Clean(a.CertificateFile) != filepath.Join(m.paths.TLS(), a.Name, "cert.pem") || filepath.Clean(a.KeyFile) != filepath.Join(m.paths.TLS(), a.Name, "key.pem") {
		return fmt.Errorf("证书不属于 Docker 模块")
	}
	for _, p := range []string{a.CertificateFile, a.KeyFile} {
		if _, err := readPrivate(p); err != nil {
			return err
		}
	}
	pair, err := tls.LoadX509KeyPair(a.CertificateFile, a.KeyFile)
	if err != nil {
		return err
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		return err
	}
	if err = leaf.VerifyHostname(a.SNI); err != nil {
		return err
	}
	now := m.now()
	if now.Before(leaf.NotBefore) || now.After(leaf.NotAfter) {
		return fmt.Errorf("TLS 证书不在有效期内")
	}
	pub, err := x509.MarshalPKIXPublicKey(leaf.PublicKey)
	if err != nil {
		return err
	}
	hash := sha256.Sum256(pub)
	if a.CertificateSHA256 != digest(leaf.Raw) || a.CertificatePublicKeySHA256 != base64.StdEncoding.EncodeToString(hash[:]) {
		return fmt.Errorf("TLS 证书指纹与状态不一致")
	}
	return nil
}
func (m *Manager) renderNode(n model.Node) (model.Node, error) {
	b, err := jsonBytes(n)
	if err != nil {
		return n, err
	}
	var rendered model.Node
	if err = decodeNode(b, &rendered); err != nil {
		return n, err
	}
	mapAccess := func(a *model.LandingAccess) error {
		if err := m.validateCertificate(*a); err != nil {
			return err
		}
		a.CertificateFile = "/etc/xray/tls/" + a.Name + "/cert.pem"
		a.KeyFile = "/etc/xray/tls/" + a.Name + "/key.pem"
		return nil
	}
	if rendered.TLSCertificate != nil {
		if err = mapAccess(rendered.TLSCertificate); err != nil {
			return rendered, err
		}
	}
	for i := range rendered.LandingAccesses {
		a := &rendered.LandingAccesses[i]
		if a.CertificateFile != "" {
			if err = mapAccess(a); err != nil {
				return rendered, err
			}
		}
	}
	return rendered, nil
}
func validateNode(n model.Node) error {
	if !validServer(n.Server) || !hostname(n.SNI, true) || n.Port < 1 || n.Port > 65535 || !uuidPattern.MatchString(n.UUID) || !namePattern.MatchString(n.UserName) || !namePattern.MatchString(n.InboundTag) {
		return fmt.Errorf("节点地址、端口、SNI、UUID 或标签无效")
	}
	security := model.NormalizeLandingSecurity(n.Security)
	if security != "reality" && security != "tls" {
		return fmt.Errorf("协议必须是 reality 或 tls")
	}
	if !n.IsTLS() {
		for _, v := range []string{n.PrivateKey, n.PublicKey} {
			b, err := base64.RawURLEncoding.DecodeString(v)
			if err != nil || len(b) != 32 {
				return fmt.Errorf("REALITY 密钥无效")
			}
		}
		b, err := hex.DecodeString(n.ShortID)
		if err != nil || len(b) == 0 || len(b) > 8 {
			return fmt.Errorf("REALITY short ID 无效")
		}
	}
	if n.XrayFallbackGuard && (n.XrayFallbackPort < 1024 || n.XrayFallbackPort > 65535 || n.XrayFallbackPort == n.Port) {
		return fmt.Errorf("回落端口无效")
	}
	if strings.TrimSpace(n.Target) == "" && !n.IsTLS() {
		return fmt.Errorf("REALITY target 不能为空")
	}
	return nil
}
