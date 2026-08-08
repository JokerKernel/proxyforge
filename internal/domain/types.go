package domain

import "time"

const (
	CoreSingBox = "sing-box"
	CoreXray    = "xray"
	VisionFlow  = "xtls-rprx-vision"
)

type NodeSpec struct {
	ManagedBy    string    `json:"managed_by"`
	Core         string    `json:"core"`
	Server       string    `json:"server"`
	Port         int       `json:"port"`
	SNI          string    `json:"sni"`
	Target       string    `json:"target"`
	UUID         string    `json:"uuid"`
	PrivateKey   string    `json:"private_key"`
	PublicKey    string    `json:"public_key"`
	ShortID      string    `json:"short_id"`
	CoreVersion  string    `json:"core_version"`
	ConfigSHA256 string    `json:"config_sha256"`
	UpdatedAt    time.Time `json:"updated_at"`
}

type GenerateOptions struct {
	Server            string
	Port              int
	SNI               string
	Target            string
	RotateCredentials bool
	TakeOver          bool
	NonInteractive    bool
}

type ResetOptions struct {
	SNI    string
	Target string
}

type KeyPair struct {
	Private string
	Public  string
}

type ServiceStatus struct {
	Active bool
	Detail string
}
