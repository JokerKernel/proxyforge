package domain

import "time"

const (
	CoreSingBox     = "sing-box"
	CoreXray        = "xray"
	VisionFlow      = "xtls-rprx-vision"
	DefaultUserName = "one"
	FallbackPortMin = 30000
	FallbackPortMax = 65000
)

type NodeSpec struct {
	ManagedBy                 string    `json:"managed_by"`
	Core                      string    `json:"core"`
	InboundTag                string    `json:"inbound_tag"`
	Server                    string    `json:"server"`
	Port                      int       `json:"port"`
	SNI                       string    `json:"sni"`
	Target                    string    `json:"target"`
	UserName                  string    `json:"user_name"`
	SimplifiedConfig          bool      `json:"simplified_config,omitempty"`
	SingBoxFallbackGuard      bool      `json:"sing_box_fallback_guard,omitempty"`
	SingBoxFallbackPort       int       `json:"sing_box_fallback_port,omitempty"`
	SingBoxFallbackHTTPDomain bool      `json:"sing_box_fallback_http_domain,omitempty"`
	XrayFallbackGuard         bool      `json:"xray_fallback_guard,omitempty"`
	XrayFallbackPort          int       `json:"xray_fallback_port,omitempty"`
	UUID                      string    `json:"uuid"`
	PrivateKey                string    `json:"private_key"`
	PublicKey                 string    `json:"public_key"`
	ShortID                   string    `json:"short_id"`
	CoreVersion               string    `json:"core_version"`
	ConfigSHA256              string    `json:"config_sha256"`
	UpdatedAt                 time.Time `json:"updated_at"`
}

type GenerateOptions struct {
	Server                    string
	Port                      int
	SNI                       string
	Target                    string
	UserName                  string
	InboundTag                string
	StandardConfig            bool
	SimplifiedConfig          bool
	SingBoxFallbackGuard      bool
	SingBoxFallbackPort       int
	SingBoxFallbackHTTPDomain bool
	XrayFallbackGuard         bool
	XrayFallbackPort          int
	RotateCredentials         bool
	NonInteractive            bool
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
