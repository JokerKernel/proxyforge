package model

import (
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"strings"
	"time"
)

const (
	CoreXray               = "xray"
	VisionFlow             = "xtls-rprx-vision"
	LandingSecurityCurrent = "current"
	LandingSecurityReality = "reality"
	LandingSecurityTLS     = "tls"
	DefaultUserName        = "one"
	LandingTLSPortMin      = 30000
	LandingTLSPortMax      = 65000
	FallbackPortMin        = LandingTLSPortMin
	FallbackPortMax        = LandingTLSPortMax
	StateSchemaVersion     = 1
)

// LandingPeer is the portable, client-side description of a managed landing
// access. It intentionally never contains the landing server's private key.
type LandingPeer struct {
	Name                       string `json:"name"`
	Core                       string `json:"core"`
	Security                   string `json:"security"`
	Server                     string `json:"server"`
	Port                       int    `json:"port"`
	SNI                        string `json:"sni"`
	UUID                       string `json:"uuid"`
	PublicKey                  string `json:"public_key,omitempty"`
	ShortID                    string `json:"short_id,omitempty"`
	CertificateSHA256          string `json:"certificate_sha256,omitempty"`
	CertificatePublicKeySHA256 string `json:"certificate_public_key_sha256,omitempty"`
	Flow                       string `json:"flow"`
}

type LandingAccess struct {
	Name                       string    `json:"name"`
	UserName                   string    `json:"user_name"`
	UUID                       string    `json:"uuid"`
	Security                   string    `json:"security,omitempty"`
	Port                       int       `json:"port,omitempty"`
	SNI                        string    `json:"sni,omitempty"`
	CertificateFile            string    `json:"certificate_file,omitempty"`
	KeyFile                    string    `json:"key_file,omitempty"`
	CertificateSHA256          string    `json:"certificate_sha256,omitempty"`
	CertificatePublicKeySHA256 string    `json:"certificate_public_key_sha256,omitempty"`
	Enabled                    bool      `json:"enabled"`
	UpdatedAt                  time.Time `json:"updated_at"`
}

func NormalizeLandingSecurity(security string) string {
	security = strings.ToLower(strings.TrimSpace(security))
	if security == "" {
		return LandingSecurityReality
	}
	return security
}

func ValidCertificateSHA256(value string) bool {
	if len(value) != 64 {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == 32
}

func ValidCertificatePublicKeySHA256(value string) bool {
	decoded, err := base64.StdEncoding.DecodeString(value)
	return err == nil && len(decoded) == 32 && base64.StdEncoding.EncodeToString(decoded) == value
}

func RelayOutboundTag(name string) string { return name + "-out" }

func LandingTLSInboundTag(name string) string { return name + "-in" }

type RelayLink struct {
	Name      string      `json:"name"`
	UserName  string      `json:"user_name"`
	UUID      string      `json:"uuid"`
	Enabled   bool        `json:"enabled"`
	Upstream  LandingPeer `json:"upstream"`
	UpdatedAt time.Time   `json:"updated_at"`
}

type Node struct {
	AccessPolicySniffing    map[string]json.RawMessage `json:"access_policy_sniffing,omitempty"`
	AccessPolicy            AccessPolicy               `json:"access_policy"`
	Security                string                     `json:"security,omitempty"`
	TLSCertificate          *LandingAccess             `json:"tls_certificate,omitempty"`
	SchemaVersion           int                        `json:"schema_version,omitempty"`
	ManagedBy               string                     `json:"managed_by"`
	Core                    string                     `json:"core"`
	InboundTag              string                     `json:"inbound_tag"`
	Server                  string                     `json:"server"`
	Port                    int                        `json:"port"`
	SNI                     string                     `json:"sni"`
	Target                  string                     `json:"target"`
	UserName                string                     `json:"user_name"`
	SimplifiedConfig        bool                       `json:"simplified_config,omitempty"`
	XrayFallbackGuard       bool                       `json:"xray_fallback_guard,omitempty"`
	XrayFallbackPort        int                        `json:"xray_fallback_port,omitempty"`
	XrayFallbackHTTPDomain  bool                       `json:"xray_fallback_http_domain,omitempty"`
	XrayFallbackExactDomain bool                       `json:"xray_fallback_exact_domain,omitempty"`
	UUID                    string                     `json:"uuid"`
	PrivateKey              string                     `json:"private_key"`
	PublicKey               string                     `json:"public_key"`
	ShortID                 string                     `json:"short_id"`
	CoreVersion             string                     `json:"core_version"`
	ConfigSHA256            string                     `json:"config_sha256"`
	UpdatedAt               time.Time                  `json:"updated_at"`
	LandingAccesses         []LandingAccess            `json:"landing_accesses,omitempty"`
	RelayLinks              []RelayLink                `json:"relay_links,omitempty"`
}

type GenerateOptions struct {
	Security                string
	RotateCertificate       bool
	Server                  string
	Port                    int
	SNI                     string
	Target                  string
	UserName                string
	InboundTag              string
	StandardConfig          bool
	SimplifiedConfig        bool
	XrayFallbackGuard       bool
	XrayFallbackPort        int
	XrayFallbackHTTPDomain  bool
	XrayFallbackExactDomain bool
	RotateCredentials       bool
	NonInteractive          bool
	DropLinks               bool
}

func DefaultInboundTag(core string) string { return "docker-one" }

type ResetOptions struct {
	RotateCertificate bool
	SNI               string
	Target            string
	// Credentials rotates UUID and, for REALITY, its key material and short ID.
	// Zero value preserves the historical credential-reset behavior.
	Credentials bool
}

type KeyPair struct {
	Private string
	Public  string
}

type ServiceStatus struct {
	Active bool
	Detail string
}

// Empty security in historical states means REALITY.
func (n Node) IsTLS() bool { return NormalizeLandingSecurity(n.Security) == LandingSecurityTLS }

// A zero-port access shares the primary inbound, including historical REALITY accesses.
func (a LandingAccess) UsesPrimaryInbound() bool {
	return NormalizeLandingSecurity(a.Security) == LandingSecurityReality || a.Port == 0
}

// TLSAccesses includes the main certificate for shared file permission handling.
func (n Node) TLSAccesses() []LandingAccess {
	accesses := append([]LandingAccess(nil), n.LandingAccesses...)
	if n.TLSCertificate != nil {
		accesses = append(accesses, *n.TLSCertificate)
	}
	return accesses
}
