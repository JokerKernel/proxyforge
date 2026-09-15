package domain

import (
	"strings"
	"time"
)

const (
	CoreSingBox            = "sing-box"
	CoreXray               = "xray"
	VisionFlow             = "xtls-rprx-vision"
	LandingSecurityReality = "reality"
	LandingSecurityTLS     = "tls"
	DefaultUserName        = "one"
	LandingTLSPortMin      = 30000
	LandingTLSPortMax      = 65000
	FallbackPortMin        = LandingTLSPortMin
	FallbackPortMax        = LandingTLSPortMax
	StateSchemaVersion     = 2
)

// LandingPeer is the portable, client-side description of a managed landing
// access. It intentionally never contains the landing server's private key.
type LandingPeer struct {
	Name      string `json:"name"`
	Core      string `json:"core"`
	Security  string `json:"security"`
	Server    string `json:"server"`
	Port      int    `json:"port"`
	SNI       string `json:"sni"`
	UUID      string `json:"uuid"`
	PublicKey string `json:"public_key,omitempty"`
	ShortID   string `json:"short_id,omitempty"`
	Flow      string `json:"flow"`
}

type LandingAccess struct {
	Name            string    `json:"name"`
	UserName        string    `json:"user_name"`
	UUID            string    `json:"uuid"`
	Security        string    `json:"security,omitempty"`
	Port            int       `json:"port,omitempty"`
	SNI             string    `json:"sni,omitempty"`
	CertificateFile string    `json:"certificate_file,omitempty"`
	KeyFile         string    `json:"key_file,omitempty"`
	Enabled         bool      `json:"enabled"`
	UpdatedAt       time.Time `json:"updated_at"`
}

func NormalizeLandingSecurity(security string) string {
	security = strings.ToLower(strings.TrimSpace(security))
	if security == "" {
		return LandingSecurityReality
	}
	return security
}

type RelayLink struct {
	Name      string      `json:"name"`
	UserName  string      `json:"user_name"`
	UUID      string      `json:"uuid"`
	Enabled   bool        `json:"enabled"`
	Upstream  LandingPeer `json:"upstream"`
	UpdatedAt time.Time   `json:"updated_at"`
}

type NodeSpec struct {
	SchemaVersion              int             `json:"schema_version,omitempty"`
	ManagedBy                  string          `json:"managed_by"`
	Core                       string          `json:"core"`
	InboundTag                 string          `json:"inbound_tag"`
	Server                     string          `json:"server"`
	Port                       int             `json:"port"`
	SNI                        string          `json:"sni"`
	Target                     string          `json:"target"`
	UserName                   string          `json:"user_name"`
	SimplifiedConfig           bool            `json:"simplified_config,omitempty"`
	SingBoxFallbackGuard       bool            `json:"sing_box_fallback_guard,omitempty"`
	SingBoxFallbackPort        int             `json:"sing_box_fallback_port,omitempty"`
	SingBoxFallbackHTTPDomain  bool            `json:"sing_box_fallback_http_domain,omitempty"`
	SingBoxFallbackExactDomain bool            `json:"sing_box_fallback_exact_domain,omitempty"`
	XrayFallbackGuard          bool            `json:"xray_fallback_guard,omitempty"`
	XrayFallbackPort           int             `json:"xray_fallback_port,omitempty"`
	XrayFallbackHTTPDomain     bool            `json:"xray_fallback_http_domain,omitempty"`
	XrayFallbackExactDomain    bool            `json:"xray_fallback_exact_domain,omitempty"`
	UUID                       string          `json:"uuid"`
	PrivateKey                 string          `json:"private_key"`
	PublicKey                  string          `json:"public_key"`
	ShortID                    string          `json:"short_id"`
	CoreVersion                string          `json:"core_version"`
	ConfigSHA256               string          `json:"config_sha256"`
	UpdatedAt                  time.Time       `json:"updated_at"`
	LandingAccesses            []LandingAccess `json:"landing_accesses,omitempty"`
	RelayLinks                 []RelayLink     `json:"relay_links,omitempty"`
}

type GenerateOptions struct {
	Server                     string
	Port                       int
	SNI                        string
	Target                     string
	UserName                   string
	InboundTag                 string
	StandardConfig             bool
	SimplifiedConfig           bool
	SingBoxFallbackGuard       bool
	SingBoxFallbackPort        int
	SingBoxFallbackHTTPDomain  bool
	SingBoxFallbackExactDomain bool
	XrayFallbackGuard          bool
	XrayFallbackPort           int
	XrayFallbackHTTPDomain     bool
	XrayFallbackExactDomain    bool
	RotateCredentials          bool
	NonInteractive             bool
	DropLinks                  bool
}

func DefaultInboundTag(core string) string {
	switch core {
	case CoreSingBox:
		return "singbox-one"
	case CoreXray:
		return "xray-one"
	default:
		return core + "-one"
	}
}

type ResetOptions struct {
	SNI    string
	Target string
	// Credentials controls whether UUID/REALITY key material/short ID are rotated.
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
