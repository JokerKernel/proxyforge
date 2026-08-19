package app

import (
	"context"
	"fmt"
	"os"
	"strings"

	"proxyforge/internal/provider"
)

type DNSSettings struct {
	Current  string
	Profiles []string
}

type DNSChange struct {
	Previous  string
	Current   string
	Restarted bool
	Changed   bool
}

func (a *App) DNSSettings(ctx context.Context, core string) (DNSSettings, error) {
	if err := a.RootCheck(); err != nil {
		return DNSSettings{}, err
	}
	p, err := a.Registry.Get(core)
	if err != nil {
		return DNSSettings{}, err
	}
	if err := a.checkCoreInstalled(ctx, p); err != nil {
		return DNSSettings{}, err
	}
	dnsProvider, ok := p.(provider.DNSProfileProvider)
	if !ok {
		return DNSSettings{}, fmt.Errorf("%s 不支持 DNS 设置", core)
	}
	configPath := a.Layout.Resolve(p.ConfigPath())
	a.progressf("读取 %s 当前 DNS 设置", core)
	config, err := os.ReadFile(configPath)
	if os.IsNotExist(err) {
		return DNSSettings{}, fmt.Errorf("尚未找到 %s 服务端配置 %s", core, p.ConfigPath())
	}
	if err != nil {
		return DNSSettings{}, fmt.Errorf("读取 %s 服务端配置: %w", core, err)
	}
	current, err := dnsProvider.CurrentDNSProfile(config)
	if err != nil {
		return DNSSettings{}, err
	}
	return DNSSettings{Current: current, Profiles: dnsProvider.DNSProfiles()}, nil
}

func (a *App) SetDNSProfile(ctx context.Context, core, requested string) (DNSChange, error) {
	change := DNSChange{Current: strings.ToLower(strings.TrimSpace(requested))}
	if err := a.RootCheck(); err != nil {
		return change, err
	}
	p, err := a.Registry.Get(core)
	if err != nil {
		return change, err
	}
	if err := a.checkCoreInstalled(ctx, p); err != nil {
		return change, err
	}
	dnsProvider, ok := p.(provider.DNSProfileProvider)
	if !ok {
		return change, fmt.Errorf("%s 不支持 DNS 设置", core)
	}
	configPath := a.Layout.Resolve(p.ConfigPath())
	a.progressf("读取并修改 %s DNS 设置", core)
	oldConfig, err := os.ReadFile(configPath)
	if os.IsNotExist(err) {
		return change, fmt.Errorf("尚未找到 %s 服务端配置 %s", core, p.ConfigPath())
	}
	if err != nil {
		return change, fmt.Errorf("读取 %s 服务端配置: %w", core, err)
	}
	change.Previous, err = dnsProvider.CurrentDNSProfile(oldConfig)
	if err != nil {
		return change, err
	}
	patched, err := dnsProvider.PatchDNSProfile(oldConfig, change.Current)
	if err != nil {
		return change, err
	}
	if change.Previous == change.Current {
		return change, nil
	}
	change.Restarted, err = a.applyServerSetting(ctx, p, core, "DNS", oldConfig, patched)
	if err != nil {
		return change, err
	}
	change.Changed = true
	return change, nil
}
