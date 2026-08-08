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

type CoreProvider interface {
	Name() string
	Binary() string
	ServiceName() string
	ConfigPath() string
	OfficialScriptURL() string
	ScriptHosts() []string
	InstallArgs(version string, upgrade bool) []string
	PackageName() string
	UninstallArgs() []string
	Version(context.Context, Runner) (string, error)
	GenerateKeyPair(context.Context, Runner) (domain.KeyPair, error)
	RenderServer(domain.NodeSpec) ([]byte, error)
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
