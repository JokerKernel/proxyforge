package app

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
	"os/user"
	"path/filepath"
	"strconv"
	"time"

	"proxyforge/internal/domain"
)

const (
	managedTLSCertificateName = "cert.pem"
	managedTLSKeyName         = "key.pem"
)

func (a *App) generateManagedTLSCertificate(core, name, serviceUser string) (domain.LandingAccess, error) {
	var access domain.LandingAccess
	dir := a.Layout.TLSAccessDir(core, name)
	if _, err := os.Stat(dir); err == nil {
		return access, fmt.Errorf("受管 TLS 证书目录已存在: %s", dir)
	} else if !os.IsNotExist(err) {
		return access, fmt.Errorf("检查受管 TLS 证书目录: %w", err)
	}

	randomName, err := randomHex(12)
	if err != nil {
		return access, fmt.Errorf("生成 TLS 证书域名: %w", err)
	}
	sni := "pf-" + randomName + ".invalid"
	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return access, fmt.Errorf("生成 TLS ECDSA 私钥: %w", err)
	}
	serialLimit := new(big.Int).Lsh(big.NewInt(1), 128)
	serial, err := rand.Int(rand.Reader, serialLimit)
	if err != nil {
		return access, fmt.Errorf("生成 TLS 证书序列号: %w", err)
	}
	if serial.Sign() == 0 {
		serial.SetInt64(1)
	}
	now := time.Now()
	if a.Now != nil {
		now = a.Now()
	}
	now = now.UTC()
	template := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: sni, Organization: []string{"ProxyForge"}},
		DNSNames:     []string{sni},
		NotBefore:    now.Add(-5 * time.Minute),
		NotAfter:     now.AddDate(10, 0, 0),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	certificateDER, err := x509.CreateCertificate(rand.Reader, template, template, &privateKey.PublicKey, privateKey)
	if err != nil {
		return access, fmt.Errorf("签发 TLS 自签证书: %w", err)
	}
	keyDER, err := x509.MarshalECPrivateKey(privateKey)
	if err != nil {
		return access, fmt.Errorf("编码 TLS 私钥: %w", err)
	}
	publicKeyDER, err := x509.MarshalPKIXPublicKey(&privateKey.PublicKey)
	if err != nil {
		return access, fmt.Errorf("编码 TLS 公钥: %w", err)
	}

	certFile := filepath.Join(dir, managedTLSCertificateName)
	keyFile := filepath.Join(dir, managedTLSKeyName)
	if err := a.createManagedTLSDirectory(core, dir, serviceUser); err != nil {
		_ = os.RemoveAll(dir)
		return access, err
	}
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.RemoveAll(dir)
		}
	}()
	fileMode := os.FileMode(0640)
	if serviceUser == "" || serviceUser == "root" {
		fileMode = 0600
	}
	if err := writeNewFile(certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certificateDER}), fileMode); err != nil {
		return access, fmt.Errorf("写入 TLS 证书: %w", err)
	}
	if err := writeNewFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), fileMode); err != nil {
		return access, fmt.Errorf("写入 TLS 私钥: %w", err)
	}
	if err := secureManagedTLSFiles(dir, certFile, keyFile, serviceUser); err != nil {
		return access, err
	}

	certificateHash := sha256.Sum256(certificateDER)
	publicKeyHash := sha256.Sum256(publicKeyDER)
	access.SNI = sni
	access.CertificateFile = certFile
	access.KeyFile = keyFile
	access.CertificateSHA256 = hex.EncodeToString(certificateHash[:])
	access.CertificatePublicKeySHA256 = base64.StdEncoding.EncodeToString(publicKeyHash[:])
	cleanup = false
	return access, nil
}

func randomHex(byteCount int) (string, error) {
	b := make([]byte, byteCount)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func (a *App) createManagedTLSDirectory(core, dir, serviceUser string) error {
	base := filepath.Dir(a.Layout.TLSRoot(core))
	dataRoot := filepath.Dir(base)
	if err := os.MkdirAll(base, 0755); err != nil {
		return fmt.Errorf("创建 TLS 数据目录: %w", err)
	}
	if err := os.Chmod(dataRoot, 0755); err != nil {
		return fmt.Errorf("设置 ProxyForge 数据目录权限: %w", err)
	}
	if err := os.Chmod(base, 0755); err != nil {
		return fmt.Errorf("设置 TLS 数据目录权限: %w", err)
	}
	dirMode := os.FileMode(0750)
	if serviceUser == "" || serviceUser == "root" {
		dirMode = 0700
	}
	if err := os.MkdirAll(dir, dirMode); err != nil {
		return fmt.Errorf("创建受管 TLS 证书目录: %w", err)
	}
	if err := os.Chmod(a.Layout.TLSRoot(core), dirMode); err != nil {
		return fmt.Errorf("设置内核 TLS 目录权限: %w", err)
	}
	if err := secureManagedTLSFiles(a.Layout.TLSRoot(core), "", "", serviceUser); err != nil {
		return err
	}
	return secureManagedTLSFiles(dir, "", "", serviceUser)
}

func secureManagedTLSFiles(dir, certFile, keyFile, serviceUser string) error {
	dirMode, fileMode := os.FileMode(0750), os.FileMode(0640)
	gid := 0
	if serviceUser == "" || serviceUser == "root" {
		dirMode, fileMode = 0700, 0600
	} else {
		u, err := user.Lookup(serviceUser)
		if err != nil {
			return fmt.Errorf("查找服务用户 %s: %w", serviceUser, err)
		}
		gid, err = strconv.Atoi(u.Gid)
		if err != nil {
			return fmt.Errorf("解析服务用户 %s 的组 ID: %w", serviceUser, err)
		}
	}
	changeOwner := serviceUser != "" && serviceUser != "root"
	for _, path := range []string{dir} {
		if path == "" {
			continue
		}
		if changeOwner {
			if err := os.Chown(path, 0, gid); err != nil {
				return fmt.Errorf("设置 TLS 目录所有者: %w", err)
			}
		}
		if err := os.Chmod(path, dirMode); err != nil {
			return fmt.Errorf("设置 TLS 目录权限: %w", err)
		}
	}
	for _, path := range []string{certFile, keyFile} {
		if path == "" {
			continue
		}
		if changeOwner {
			if err := os.Chown(path, 0, gid); err != nil {
				return fmt.Errorf("设置 TLS 文件所有者: %w", err)
			}
		}
		if err := os.Chmod(path, fileMode); err != nil {
			return fmt.Errorf("设置 TLS 文件权限: %w", err)
		}
	}
	return nil
}

func writeNewFile(path string, data []byte, mode os.FileMode) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	if _, err = f.Write(data); err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	return err
}

func (a *App) removeManagedTLSAccess(core, name string) error {
	if core != domain.CoreXray && core != domain.CoreSingBox {
		return fmt.Errorf("拒绝清理未知内核的 TLS 目录: %q", core)
	}
	if err := validateLinkName(name); err != nil {
		return fmt.Errorf("拒绝清理不安全的 TLS 接入目录名 %q: %w", name, err)
	}
	dir := a.Layout.TLSAccessDir(core, name)
	if err := removeCleanupPath(dir); err != nil {
		return err
	}
	for _, parent := range []string{a.Layout.TLSRoot(core), filepath.Dir(a.Layout.TLSRoot(core))} {
		if err := removeEmptyCleanupDirectory(parent); err != nil {
			return err
		}
	}
	return nil
}

func (a *App) isManagedTLSAccess(core string, access domain.LandingAccess) bool {
	if (core != domain.CoreXray && core != domain.CoreSingBox) || validateLinkName(access.Name) != nil {
		return false
	}
	dir := a.Layout.TLSAccessDir(core, access.Name)
	return domain.NormalizeLandingSecurity(access.Security) == domain.LandingSecurityTLS &&
		filepath.Clean(access.CertificateFile) == filepath.Join(dir, managedTLSCertificateName) &&
		filepath.Clean(access.KeyFile) == filepath.Join(dir, managedTLSKeyName)
}

func validateManagedTLSCertificate(access domain.LandingAccess, now time.Time) error {
	pair, err := tls.LoadX509KeyPair(access.CertificateFile, access.KeyFile)
	if err != nil {
		return fmt.Errorf("读取受管 TLS 证书或私钥: %w", err)
	}
	if len(pair.Certificate) == 0 {
		return fmt.Errorf("受管 TLS 证书链为空")
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		return fmt.Errorf("解析受管 TLS 证书: %w", err)
	}
	if err := leaf.VerifyHostname(access.SNI); err != nil {
		return fmt.Errorf("受管 TLS 证书不匹配 SNI %s: %w", access.SNI, err)
	}
	if now.Before(leaf.NotBefore) || now.After(leaf.NotAfter) {
		return fmt.Errorf("受管 TLS 证书当前不在有效期内（%s 至 %s）", leaf.NotBefore.Format(time.RFC3339), leaf.NotAfter.Format(time.RFC3339))
	}
	publicKeyDER, err := x509.MarshalPKIXPublicKey(leaf.PublicKey)
	if err != nil {
		return fmt.Errorf("编码受管 TLS 公钥: %w", err)
	}
	certificateHash := sha256.Sum256(leaf.Raw)
	publicKeyHash := sha256.Sum256(publicKeyDER)
	if access.CertificateSHA256 != hex.EncodeToString(certificateHash[:]) ||
		access.CertificatePublicKeySHA256 != base64.StdEncoding.EncodeToString(publicKeyHash[:]) {
		return fmt.Errorf("受管 TLS 证书与状态中的固定指纹不一致；请删除并重新创建该接入")
	}
	return nil
}
