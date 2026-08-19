package app

import (
	"context"
	"fmt"
	"os"
	"strings"

	"proxyforge/internal/domain"
	"proxyforge/internal/provider"
)

func (a *App) HasFallback(core string) bool {
	current, err := a.Store.Load(core)
	if err != nil {
		return false
	}
	switch core {
	case domain.CoreXray:
		return current.XrayFallbackGuard
	case domain.CoreSingBox:
		return current.SingBoxFallbackGuard
	default:
		return false
	}
}

func (a *App) FallbackIPSettings(ctx context.Context, core string) (OutboundIPSettings, error) {
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
	ipProvider, ok := p.(provider.FallbackIPProvider)
	if !ok {
		return OutboundIPSettings{}, fmt.Errorf("%s 不支持回落 IP 设置", core)
	}
	if !a.HasFallback(core) {
		return OutboundIPSettings{}, fmt.Errorf("当前 %s 配置未启用回落防偷跑", core)
	}
	configPath := a.Layout.Resolve(p.ConfigPath())
	a.progressf("读取 %s 当前回落 IP 策略", core)
	config, err := os.ReadFile(configPath)
	if os.IsNotExist(err) {
		return OutboundIPSettings{}, fmt.Errorf("尚未找到 %s 服务端配置 %s", core, p.ConfigPath())
	}
	if err != nil {
		return OutboundIPSettings{}, fmt.Errorf("读取 %s 服务端配置: %w", core, err)
	}
	current, err := ipProvider.CurrentFallbackIPStrategy(config)
	if err != nil {
		return OutboundIPSettings{}, err
	}
	strategies := []string{
		provider.OutboundIPPreferIPv4,
		provider.OutboundIPPreferIPv6,
		provider.OutboundIPIPv4Only,
		provider.OutboundIPIPv6Only,
		provider.OutboundIPUnset,
	}
	if outbound, ok := p.(provider.OutboundIPProvider); ok {
		strategies = outbound.OutboundIPStrategies()
	}
	return OutboundIPSettings{Current: current, Strategies: strategies}, nil
}

func (a *App) SetFallbackIPStrategy(ctx context.Context, core, requested string) (OutboundIPChange, error) {
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
	ipProvider, ok := p.(provider.FallbackIPProvider)
	if !ok {
		return change, fmt.Errorf("%s 不支持回落 IP 设置", core)
	}
	if !a.HasFallback(core) {
		return change, fmt.Errorf("当前 %s 配置未启用回落防偷跑", core)
	}
	configPath := a.Layout.Resolve(p.ConfigPath())
	a.progressf("读取并修改 %s 回落 IP 策略", core)
	oldConfig, err := os.ReadFile(configPath)
	if os.IsNotExist(err) {
		return change, fmt.Errorf("尚未找到 %s 服务端配置 %s", core, p.ConfigPath())
	}
	if err != nil {
		return change, fmt.Errorf("读取 %s 服务端配置: %w", core, err)
	}
	change.Previous, err = ipProvider.CurrentFallbackIPStrategy(oldConfig)
	if err != nil {
		return change, err
	}
	patched, err := ipProvider.PatchFallbackIPStrategy(oldConfig, change.Current)
	if err != nil {
		return change, err
	}
	if change.Previous == change.Current {
		return change, nil
	}
	change.Restarted, err = a.applyServerSetting(ctx, p, core, "回落 IP", oldConfig, patched)
	if err != nil {
		return change, err
	}
	change.Changed = true
	return change, nil
}
