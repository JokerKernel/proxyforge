package system

import (
	"context"
	"fmt"
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
		return m.Runner.Run(ctx, "journalctl", "-u", service, "-n", "100", "--no-pager")
	}
	args := []string{action, service}
	if action == "status" {
		args = append(args, "--no-pager")
	}
	return m.Runner.Run(ctx, "systemctl", args...)
}

func (m ServiceManager) Restart(ctx context.Context, service string) error {
	_, err := m.Runner.Run(ctx, "systemctl", "restart", service)
	return err
}

func (m ServiceManager) IsActive(ctx context.Context, service string) (domain.ServiceStatus, error) {
	b, err := m.Runner.Run(ctx, "systemctl", "is-active", service)
	detail := strings.TrimSpace(string(b))
	if err != nil {
		return domain.ServiceStatus{Detail: detail}, err
	}
	return domain.ServiceStatus{Active: detail == "active", Detail: detail}, nil
}

func (m ServiceManager) User(ctx context.Context, service string) string {
	b, err := m.Runner.Run(ctx, "systemctl", "show", service, "-p", "User", "--value")
	if err != nil || strings.TrimSpace(string(b)) == "" {
		return "root"
	}
	return strings.TrimSpace(string(b))
}
