package docker

import (
	"context"
	"encoding/json"
	"fmt"
	"runtime"
	"strings"
)

func (m *Manager) CheckReady(ctx context.Context) error {
	if err := m.rootCheck(); err != nil {
		return err
	}
	if runtime.GOOS != "linux" || (runtime.GOARCH != "amd64" && runtime.GOARCH != "arm64") {
		return fmt.Errorf("Docker Xray 仅支持 Linux amd64/arm64")
	}
	if _, err := m.lookPath("docker"); err != nil {
		return fmt.Errorf("Docker 未安装: %w", err)
	}
	version, err := m.docker(ctx, "compose", "version", "--short")
	if err != nil {
		return fmt.Errorf("Compose 不可用，请安装 Docker Compose 插件: %w", err)
	}
	if !strings.HasPrefix(strings.TrimSpace(string(version)), "2.") && !strings.HasPrefix(strings.TrimSpace(string(version)), "v2.") {
		return fmt.Errorf("需要 Docker Compose v2，当前为 %s", strings.TrimSpace(string(version)))
	}
	b, err := m.docker(ctx, "info", "--format", "{{json .}}")
	if err != nil {
		return fmt.Errorf("无法访问本机 Docker daemon: %w", err)
	}
	var info struct {
		OSType          string
		Architecture    string
		SecurityOptions []string
	}
	if err = json.Unmarshal(b, &info); err != nil {
		return fmt.Errorf("Docker daemon 返回无效状态: %w", err)
	}
	if info.OSType != "linux" {
		return fmt.Errorf("需要本机 Linux Docker daemon")
	}
	for _, opt := range info.SecurityOptions {
		if strings.Contains(opt, "rootless") || strings.Contains(opt, "userns") {
			return fmt.Errorf("首版不支持 rootless/userns-remap Docker")
		}
	}
	return nil
}
