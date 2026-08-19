package app

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"proxyforge/internal/provider/mihomo"
)

const (
	ClientFormatNative = "native"
	ClientFormatClash  = "clash"
)

func (a *App) Client(ctx context.Context, core, output string, force bool) ([]byte, error) {
	return a.ClientConfig(ctx, core, ClientFormatNative, output, force)
}

func (a *App) ServerConfigPath(core string) (string, error) {
	if err := a.RootCheck(); err != nil {
		return "", err
	}
	p, err := a.Registry.Get(core)
	if err != nil {
		return "", err
	}
	path := a.Layout.Resolve(p.ConfigPath())
	info, err := os.Stat(path)
	if os.IsNotExist(err) {
		return "", fmt.Errorf("尚未找到 %s 服务端配置 %s", core, p.ConfigPath())
	}
	if err != nil {
		return "", fmt.Errorf("检查 %s 服务端配置 %s: %w", core, p.ConfigPath(), err)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("服务端配置路径不是普通文件: %s", p.ConfigPath())
	}
	return path, nil
}

func (a *App) ServerConfig(core string) ([]byte, error) {
	if err := a.RootCheck(); err != nil {
		return nil, err
	}
	p, err := a.Registry.Get(core)
	if err != nil {
		return nil, err
	}
	path := a.Layout.Resolve(p.ConfigPath())
	a.progressf("读取当前 %s 服务端配置 %s", core, path)
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, fmt.Errorf("尚未找到 %s 服务端配置 %s", core, p.ConfigPath())
	}
	if err != nil {
		return nil, fmt.Errorf("读取 %s 服务端配置 %s: %w", core, p.ConfigPath(), err)
	}
	return b, nil
}

func (a *App) ServerConfigExists(core string) (bool, error) {
	if err := a.RootCheck(); err != nil {
		return false, err
	}
	p, err := a.Registry.Get(core)
	if err != nil {
		return false, err
	}
	path := a.Layout.Resolve(p.ConfigPath())
	info, err := os.Stat(path)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("检查 %s 服务端配置 %s: %w", core, p.ConfigPath(), err)
	}
	if !info.Mode().IsRegular() {
		return false, fmt.Errorf("服务端配置路径不是普通文件: %s", p.ConfigPath())
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return false, fmt.Errorf("读取 %s 服务端配置 %s: %w", core, p.ConfigPath(), err)
	}
	return !isEmptyServerConfigPlaceholder(b), nil
}

func isEmptyServerConfigPlaceholder(b []byte) bool {
	trimmed := strings.TrimSpace(string(b))
	if trimmed == "" {
		return true
	}
	var object map[string]json.RawMessage
	return json.Unmarshal([]byte(trimmed), &object) == nil && object != nil && len(object) == 0
}

func (a *App) ClientConfig(ctx context.Context, core, format, output string, force bool) ([]byte, error) {
	if err := a.RootCheck(); err != nil {
		return nil, err
	}
	format = strings.ToLower(strings.TrimSpace(format))
	if format == "" {
		format = ClientFormatNative
	}
	if format == "mihomo" || format == "clash-meta" {
		format = ClientFormatClash
	}
	if format != ClientFormatNative && format != ClientFormatClash {
		return nil, fmt.Errorf("不支持的客户端格式 %q（可选 native 或 clash）", format)
	}
	a.progressf("开始生成 %s 客户端配置（格式：%s）", core, format)
	p, err := a.Registry.Get(core)
	if err != nil {
		return nil, err
	}
	a.progressf("读取受管节点状态并渲染客户端配置")
	n, err := a.Store.Load(core)
	if err != nil {
		return nil, err
	}
	var b []byte
	if format == ClientFormatClash {
		b, err = mihomo.RenderClient(n)
		if err != nil {
			return nil, err
		}
		a.progressf("已生成 Mihomo/Clash Meta YAML；不使用 %s 校验非原生格式", core)
	} else {
		b, err = p.RenderClient(n)
		if err != nil {
			return nil, err
		}
		tmp, createErr := os.CreateTemp("", "proxyforge-client-*.json")
		if createErr != nil {
			return nil, createErr
		}
		path := tmp.Name()
		defer os.Remove(path)
		if err = tmp.Chmod(0600); err == nil {
			_, err = tmp.Write(b)
		}
		if closeErr := tmp.Close(); err == nil {
			err = closeErr
		}
		if err != nil {
			return nil, err
		}
		a.progressf("使用 %s 原生命令校验客户端配置", core)
		if err = p.ValidateConfig(ctx, a.Runner, path); err != nil {
			return nil, err
		}
	}
	if output == "" {
		a.progressf("客户端配置生成完成，将 %s 输出到 stdout", clientFormatLabel(format))
		return b, nil
	}
	a.progressf("以 0600 权限写入客户端配置 %s", output)
	flags := os.O_WRONLY | os.O_CREATE
	if force {
		flags |= os.O_TRUNC
	} else {
		flags |= os.O_EXCL
	}
	f, err := os.OpenFile(output, flags, 0600)
	if err != nil {
		return nil, fmt.Errorf("写客户端配置: %w", err)
	}
	if _, err = f.Write(b); err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	return b, err
}

func clientFormatLabel(format string) string {
	if format == ClientFormatClash {
		return "YAML"
	}
	return "JSON"
}
