package app

import (
	"context"
	"errors"
	"fmt"
	"time"

	"proxyforge/internal/domain"
)

// Recovery must still run when the operation's context has been canceled.
func recoveryContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
}

func recoveryResult(cause error, success string, failures ...error) error {
	if failure := errors.Join(failures...); failure != nil {
		return fmt.Errorf("%w；且回滚未完整完成: %w", cause, failure)
	}
	return fmt.Errorf("%w；%s", cause, success)
}

func (a *App) restartForRecovery(ctx context.Context, service string) error {
	if err := a.Services.Restart(ctx, service); err != nil {
		return fmt.Errorf("恢复 %s 运行状态: %w", service, err)
	}
	status, err := a.Services.IsActive(ctx, service)
	if err := serviceActiveError(service, status, err); err != nil {
		return fmt.Errorf("恢复 %s 运行状态: %w", service, err)
	}
	return nil
}

func serviceActiveError(service string, status domain.ServiceStatus, err error) error {
	if err == nil && status.Active {
		return nil
	}
	message := fmt.Sprintf("%s 未进入 active 状态: %s", service, status.Detail)
	if err != nil {
		return fmt.Errorf("%s: %w", message, err)
	}
	return errors.New(message)
}
