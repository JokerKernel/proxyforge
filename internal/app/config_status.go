package app

import (
	"os"
	"strings"

	"proxyforge/internal/provider"
)

// ModifyConfigStatus is a quiet snapshot for the interactive modify-config card.
// It does not require root, an installed core, or emit progress.
type ModifyConfigStatus struct {
	SNI         string
	DNS         string
	OutboundIP  string
	FallbackIP  string
	HasFallback bool
	HasConfig   bool
}

func (a *App) ModifyConfigStatus(core string) ModifyConfigStatus {
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
	config, err := os.ReadFile(a.Layout.Resolve(p.ConfigPath()))
	if err != nil {
		return status
	}
	status.HasConfig = true
	if dnsProvider, ok := p.(provider.DNSProfileProvider); ok {
		if current, err := dnsProvider.CurrentDNSProfile(config); err == nil {
			status.DNS = current
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
