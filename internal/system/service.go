package system

import (
	"context"
	"fmt"
	"io"
	"strings"

	"proxyforge/internal/domain"
	"proxyforge/internal/provider"
)

type ServiceManager struct{ Runner provider.Runner }

func (m ServiceManager) Action(ctx context.Context, service, action string) ([]byte, error) {
	valid := map[string]bool{"start": true, "stop": true, "restart": true, "status": true, "logs": true}
	if !valid[action] {
		return nil, fmt.Errorf("不支持的服务操作 %q", action)
	}
	if action == "logs" {
		b, err := m.Runner.Run(ctx, "journalctl", "-u", service, "-n", "100", "--no-pager")
		return PrefixLines(b, serviceLogPrefix(service)), err
	}
	args := []string{action, service}
	if action == "status" {
		args = append(args, "--no-pager")
	}
	b, err := m.Runner.Run(ctx, "systemctl", args...)
	return PrefixLines(b, "[系统命令/输出] "), err
}

func (m ServiceManager) FollowLogs(ctx context.Context, service string, output io.Writer) error {
	streaming, ok := m.Runner.(provider.StreamingRunner)
	if !ok {
		return fmt.Errorf("当前命令执行器不支持实时日志输出")
	}
	prefixed := NewLinePrefixWriter(output, serviceLogPrefix(service))
	return streaming.RunStreaming(ctx, prefixed, prefixed, "journalctl", "-u", service, "-n", "100", "-f", "--no-pager")
}

func serviceLogPrefix(service string) string {
	return "[服务日志/" + strings.TrimSuffix(service, ".service") + "] "
}

func (m ServiceManager) Restart(ctx context.Context, service string) error {
	_, err := m.Runner.Run(ctx, "systemctl", "restart", service)
	return err
}

func (m ServiceManager) Enable(ctx context.Context, service string) error {
	_, err := m.Runner.Run(ctx, "systemctl", "enable", service)
	return err
}

func (m ServiceManager) DisableNow(ctx context.Context, service string) error {
	_, err := m.Runner.Run(ctx, "systemctl", "disable", "--now", service)
	return err
}

func (m ServiceManager) DaemonReload(ctx context.Context) error {
	_, err := m.Runner.Run(ctx, "systemctl", "daemon-reload")
	return err
}

func (m ServiceManager) EnabledState(ctx context.Context, service string) (string, error) {
	b, err := m.Runner.Run(ctx, "systemctl", "is-enabled", service)
	return strings.TrimSpace(string(b)), err
}

func (m ServiceManager) IsActive(ctx context.Context, service string) (domain.ServiceStatus, error) {
	b, err := m.Runner.Run(ctx, "systemctl", "is-active", service)
	detail := strings.TrimSpace(string(b))
	if err != nil {
		return domain.ServiceStatus{Detail: detail}, err
	}
	return domain.ServiceStatus{Active: detail == "active", Detail: detail}, nil
}

func (m ServiceManager) UnitLoadState(ctx context.Context, service string) (string, error) {
	b, err := m.Runner.Run(ctx, "systemctl", "show", service, "-p", "LoadState", "--value")
	if err != nil {
		return "", fmt.Errorf("检查 systemd unit %s: %w", service, err)
	}
	state := strings.TrimSpace(string(b))
	if state == "" {
		return "", fmt.Errorf("检查 systemd unit %s: systemctl 返回了空 LoadState", service)
	}
	return state, nil
}

func (m ServiceManager) User(ctx context.Context, service string) string {
	b, err := m.Runner.Run(ctx, "systemctl", "show", service, "-p", "User", "--value")
	if err != nil || strings.TrimSpace(string(b)) == "" {
		return "root"
	}
	return strings.TrimSpace(string(b))
}
