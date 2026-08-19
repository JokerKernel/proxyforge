package provider

import (
	"context"
	"fmt"
	"io"
	"sort"
	"sync"

	"proxyforge/internal/domain"
)

type Runner interface {
	Run(ctx context.Context, name string, args ...string) ([]byte, error)
}

type StreamingRunner interface {
	RunStreaming(ctx context.Context, stdout, stderr io.Writer, name string, args ...string) error
}

// ScriptProxyProvider is implemented by providers whose management script
// requires an explicit proxy argument instead of honoring proxy environment
// variables directly.
type ScriptProxyProvider interface {
	ScriptProxyArgs(proxyURL string) []string
}

// LogLevelProvider updates only the top-level logging settings while
// preserving every unrelated field in an existing server configuration.
type LogLevelProvider interface {
	LogLevels() []string
	CurrentLogLevel([]byte) (string, error)
	PatchLogLevel([]byte, string) ([]byte, error)
}

const (
	DNSProfileSystem           = "system"
	DNSProfilePublicCloudflare = "public-cloudflare"
	DNSProfilePublicGoogle     = "public-google"
	DNSProfileDoHCloudflare    = "doh-cloudflare"
	DNSProfileDoHGoogle        = "doh-google"
	DNSProfileCloudflare       = "cloudflare"
	DNSProfileGoogle           = "google"
)

type DNSProfileProvider interface {
	DNSProfiles() []string
	CurrentDNSProfile([]byte) (string, error)
	CurrentDNSServers([]byte) ([]string, error)
	PatchDNSProfile([]byte, string) ([]byte, error)
}

const (
	OutboundIPPreferIPv4 = "prefer-ipv4"
	OutboundIPPreferIPv6 = "prefer-ipv6"
	OutboundIPIPv4Only   = "ipv4-only"
	OutboundIPIPv6Only   = "ipv6-only"
	OutboundIPUnset      = "unset"
)

type OutboundIPProvider interface {
	OutboundIPStrategies() []string
	CurrentOutboundIPStrategy([]byte) (string, error)
	PatchOutboundIPStrategy([]byte, string) ([]byte, error)
}

type FallbackIPProvider interface {
	CurrentFallbackIPStrategy([]byte) (string, error)
	PatchFallbackIPStrategy([]byte, string) ([]byte, error)
}

type CoreProvider interface {
	Name() string
	Binary() string
	ServiceName() string
	ConfigPath() string
	OfficialScriptURL() string
	ScriptHosts() []string
	InstallArgs(version string) []string
	PackageName() string
	UninstallArgs() []string
	CleanupPaths() []string
	Version(context.Context, Runner) (string, error)
	GenerateKeyPair(context.Context, Runner) (domain.KeyPair, error)
	RenderServer(domain.NodeSpec) ([]byte, error)
	PatchServer([]byte, domain.NodeSpec, domain.NodeSpec, bool) ([]byte, error)
	RenderClient(domain.NodeSpec) ([]byte, error)
	ValidateConfig(context.Context, Runner, string) error
}

type Registry struct {
	mu sync.RWMutex
	m  map[string]CoreProvider
}

func NewRegistry(providers ...CoreProvider) *Registry {
	r := &Registry{m: make(map[string]CoreProvider)}
	for _, p := range providers {
		r.m[p.Name()] = p
	}
	return r
}

func (r *Registry) Get(name string) (CoreProvider, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	p, ok := r.m[name]
	if !ok {
		return nil, fmt.Errorf("不支持的内核 %q（可选: sing-box, xray）", name)
	}
	return p, nil
}

func (r *Registry) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	names := make([]string, 0, len(r.m))
	for name := range r.m {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
