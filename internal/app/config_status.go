package app

import (
	"context"
	"os"
	"strings"

	"proxyforge/internal/provider"
	"proxyforge/internal/system"
)

// ModifyConfigStatus is a quiet snapshot for the interactive modify-config card.
// It does not require root, an installed core, or emit progress.
type ModifyConfigStatus struct {
	SNI         string
	DNS         string
	DNSServers  []string
	OutboundIP  string
	FallbackIP  string
	ServiceUser string
	HasFallback bool
	HasConfig   bool
}

var lookupSystemResolvers = system.ResolverAddresses

func (a *App) ModifyConfigStatus(ctx context.Context, core string) ModifyConfigStatus {
	var status ModifyConfigStatus
	if a == nil {
		return status
	}
	if current, err := a.Store.Load(core); err == nil {
		status.SNI = strings.TrimSpace(current.SNI)
		status.HasFallback = a.HasFallback(core)
	}
	if a.Registry == nil {
		return status
	}
	p, err := a.Registry.Get(core)
	if err != nil {
		return status
	}
	if a.Services.Runner != nil {
		if user, err := a.Services.UserState(ctx, p.ServiceName()); err == nil {
			status.ServiceUser = user
		}
	}
	config, err := os.ReadFile(a.Layout.Resolve(p.ConfigPath()))
	if err != nil {
		return status
	}
	status.HasConfig = true
	if dnsProvider, ok := p.(provider.DNSProfileProvider); ok {
		if current, err := dnsProvider.CurrentDNSProfile(config); err == nil {
			status.DNS = current
		}
		if servers, err := dnsProvider.CurrentDNSServers(config); err == nil {
			status.DNSServers = expandDNSServers(servers)
		}
	}
	if ipProvider, ok := p.(provider.OutboundIPProvider); ok {
		if current, err := ipProvider.CurrentOutboundIPStrategy(config); err == nil {
			status.OutboundIP = current
		}
	}
	if status.HasFallback {
		if ipProvider, ok := p.(provider.FallbackIPProvider); ok {
			if current, err := ipProvider.CurrentFallbackIPStrategy(config); err == nil {
				status.FallbackIP = current
			}
		}
	}
	return status
}

func expandDNSServers(servers []string) []string {
	var addrs []string
	seen := make(map[string]struct{})
	add := func(addr string) {
		addr = strings.TrimSpace(addr)
		if addr == "" {
			return
		}
		if _, exists := seen[addr]; exists {
			return
		}
		seen[addr] = struct{}{}
		addrs = append(addrs, addr)
	}
	usedSystem := false
	for _, server := range servers {
		if server == "localhost" || server == "local" {
			if usedSystem {
				continue
			}
			usedSystem = true
			if resolved := lookupSystemResolvers(); len(resolved) > 0 {
				for _, addr := range resolved {
					add(addr)
				}
			}
			continue
		}
		add(server)
	}
	return addrs
}
