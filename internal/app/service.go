package app

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"proxyforge/internal/provider"
)

type LogLevelSettings struct {
	Current string
	Levels  []string
}

type LogLevelChange struct {
	Previous  string
	Current   string
	Restarted bool
	Changed   bool
}

func (a *App) Service(ctx context.Context, core, action string) ([]byte, error) {
	a.progressf("执行 %s 服务操作：%s", core, action)
	if err := a.RootCheck(); err != nil {
		return nil, err
	}
	p, err := a.Registry.Get(core)
	if err != nil {
		return nil, err
	}
	return a.Services.Action(ctx, p.ServiceName(), action)
}

func (a *App) FollowServiceLogs(ctx context.Context, core string, output io.Writer) error {
	a.progressf("实时查看 %s 服务日志", core)
	if err := a.RootCheck(); err != nil {
		return err
	}
	p, err := a.Registry.Get(core)
	if err != nil {
		return err
	}
	return a.Services.FollowLogs(ctx, p.ServiceName(), output)
}

func (a *App) LogLevelSettings(ctx context.Context, core string) (LogLevelSettings, error) {
	if err := a.RootCheck(); err != nil {
		return LogLevelSettings{}, err
	}
	p, err := a.Registry.Get(core)
	if err != nil {
		return LogLevelSettings{}, err
	}
	if err := a.checkCoreInstalled(ctx, p); err != nil {
		return LogLevelSettings{}, err
	}
	logProvider, ok := p.(provider.LogLevelProvider)
	if !ok {
		return LogLevelSettings{}, fmt.Errorf("%s 不支持日志级别设置", core)
	}
	configPath := a.Layout.Resolve(p.ConfigPath())
	a.progressf("读取 %s 当前日志级别", core)
	config, err := os.ReadFile(configPath)
	if os.IsNotExist(err) {
		return LogLevelSettings{}, fmt.Errorf("尚未找到 %s 服务端配置 %s", core, p.ConfigPath())
	}
	if err != nil {
		return LogLevelSettings{}, fmt.Errorf("读取 %s 服务端配置: %w", core, err)
	}
	current, err := logProvider.CurrentLogLevel(config)
	if err != nil {
		return LogLevelSettings{}, err
	}
	return LogLevelSettings{Current: current, Levels: logProvider.LogLevels()}, nil
}

func (a *App) SetLogLevel(ctx context.Context, core, requested string) (LogLevelChange, error) {
	change := LogLevelChange{Current: strings.ToLower(strings.TrimSpace(requested))}
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
	logProvider, ok := p.(provider.LogLevelProvider)
	if !ok {
		return change, fmt.Errorf("%s 不支持日志级别设置", core)
	}
	configPath := a.Layout.Resolve(p.ConfigPath())
	a.progressf("读取并修改 %s 日志级别", core)
	oldConfig, err := os.ReadFile(configPath)
	if os.IsNotExist(err) {
		return change, fmt.Errorf("尚未找到 %s 服务端配置 %s", core, p.ConfigPath())
	}
	if err != nil {
		return change, fmt.Errorf("读取 %s 服务端配置: %w", core, err)
	}
	change.Previous, err = logProvider.CurrentLogLevel(oldConfig)
	if err != nil {
		return change, err
	}
	patched, err := logProvider.PatchLogLevel(oldConfig, change.Current)
	if err != nil {
		return change, err
	}
	if change.Previous == change.Current {
		return change, nil
	}
	change.Restarted, err = a.applyServerSetting(ctx, p, core, "日志级别", oldConfig, patched)
	if err != nil {
		return change, err
	}
	change.Changed = true
	return change, nil
}
