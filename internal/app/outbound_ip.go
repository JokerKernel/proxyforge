package app

import (
	"context"
	"fmt"
	"os"
	"strings"

	"proxyforge/internal/provider"
)

type OutboundIPSettings struct {
	Current    string
	Strategies []string
}

type OutboundIPChange struct {
	Previous  string
	Current   string
	Restarted bool
	Changed   bool
}

func (a *App) OutboundIPSettings(ctx context.Context, core string) (OutboundIPSettings, error) {
	if err := a.RootCheck(); err != nil {
		return OutboundIPSettings{}, err
	}
	p, err := a.Registry.Get(core)
	if err != nil {
		return OutboundIPSettings{}, err
	}
	if err := a.checkCoreInstalled(ctx, p); err != nil {
		return OutboundIPSettings{}, err
	}
	ipProvider, ok := p.(provider.OutboundIPProvider)
	if !ok {
		return OutboundIPSettings{}, fmt.Errorf("%s 不支持出站 IP 设置", core)
	}
	configPath := a.Layout.Resolve(p.ConfigPath())
	a.progressf("读取 %s 当前出站 IP 策略", core)
	config, err := os.ReadFile(configPath)
	if os.IsNotExist(err) {
		return OutboundIPSettings{}, fmt.Errorf("尚未找到 %s 服务端配置 %s", core, p.ConfigPath())
	}
	if err != nil {
		return OutboundIPSettings{}, fmt.Errorf("读取 %s 服务端配置: %w", core, err)
	}
	current, err := ipProvider.CurrentOutboundIPStrategy(config)
	if err != nil {
		return OutboundIPSettings{}, err
	}
	return OutboundIPSettings{Current: current, Strategies: ipProvider.OutboundIPStrategies()}, nil
}

func (a *App) SetOutboundIPStrategy(ctx context.Context, core, requested string) (OutboundIPChange, error) {
	change := OutboundIPChange{Current: strings.ToLower(strings.TrimSpace(requested))}
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
	ipProvider, ok := p.(provider.OutboundIPProvider)
	if !ok {
		return change, fmt.Errorf("%s 不支持出站 IP 设置", core)
	}
	configPath := a.Layout.Resolve(p.ConfigPath())
	a.progressf("读取并修改 %s 出站 IP 策略", core)
	oldConfig, err := os.ReadFile(configPath)
	if os.IsNotExist(err) {
		return change, fmt.Errorf("尚未找到 %s 服务端配置 %s", core, p.ConfigPath())
	}
	if err != nil {
		return change, fmt.Errorf("读取 %s 服务端配置: %w", core, err)
	}
	change.Previous, err = ipProvider.CurrentOutboundIPStrategy(oldConfig)
	if err != nil {
		return change, err
	}
	patched, err := ipProvider.PatchOutboundIPStrategy(oldConfig, change.Current)
	if err != nil {
		return change, err
	}
	if change.Previous == change.Current {
		return change, nil
	}
	change.Restarted, err = a.applyServerSetting(ctx, p, core, "出站 IP", oldConfig, patched)
	if err != nil {
		return change, err
	}
	change.Changed = true
	return change, nil
}
