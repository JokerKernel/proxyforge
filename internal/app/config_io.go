package app

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"proxyforge/internal/provider"
	"proxyforge/internal/system"
)

func (a *App) applyServerSetting(ctx context.Context, p provider.CoreProvider, core, setting string, oldConfig, patched []byte) (bool, error) {
	configPath := a.Layout.Resolve(p.ConfigPath())
	metadata, err := readMetadata(configPath, true)
	if err != nil {
		return false, err
	}

	oldState, stateErr := a.Store.Load(core)
	hasState := stateErr == nil
	if stateErr != nil && !errors.Is(stateErr, system.ErrNoState) {
		return false, fmt.Errorf("读取 ProxyForge 节点状态: %w", stateErr)
	}
	managedState := hasState && oldState.ConfigSHA256 != "" && oldState.ConfigSHA256 == system.SHA256(oldConfig)

	a.progressf("使用 %s 原生命令校验 %s 配置", core, setting)
	if err := validateRendered(ctx, p, a.Runner, configPath, patched); err != nil {
		return false, err
	}
	status, statusErr := a.Services.IsActive(ctx, p.ServiceName())
	running, err := installedServiceRunning(status, statusErr)
	if err != nil {
		return false, fmt.Errorf("检查 %s 运行状态: %w", p.ServiceName(), err)
	}
	now := time.Now()
	if a.Now != nil {
		now = a.Now()
	}
	a.progressf("备份现有服务端配置 %s", configPath)
	if backup, err := system.BackupFile(configPath, a.Layout.BackupRoot(core), now); err != nil {
		return false, err
	} else if backup != "" {
		a.progressf("现有配置已备份到 %s", backup)
	}

	rollback := func(cause error) error {
		a.progressf("%s 修改失败，正在恢复旧配置和服务", setting)
		restoreErr := restoreFile(configPath, oldConfig, true, metadata)
		if running {
			_ = a.Services.Restart(ctx, p.ServiceName())
		}
		if restoreErr != nil {
			return fmt.Errorf("%v；且恢复旧配置失败: %w", cause, restoreErr)
		}
		return fmt.Errorf("%v；已恢复旧配置", cause)
	}

	a.progressf("原子写入 %s 配置 %s", setting, configPath)
	mode := metadata.mode
	if mode == 0 {
		mode = 0600
	}
	if err := system.AtomicWrite(configPath, patched, mode); err != nil {
		return false, err
	}
	serviceUser := a.Services.User(ctx, p.ServiceName())
	if err := secureConfigForUser(configPath, serviceUser); err != nil {
		return false, rollback(err)
	}
	if running {
		a.progressf("重启 systemd 服务 %s 使 %s 设置立即生效", p.ServiceName(), setting)
		if err := a.Services.Restart(ctx, p.ServiceName()); err != nil {
			return false, rollback(fmt.Errorf("重启 %s 失败: %w", p.ServiceName(), err))
		}
		active, activeErr := a.Services.IsActive(ctx, p.ServiceName())
		if activeErr != nil || !active.Active {
			return false, rollback(fmt.Errorf("%s 未进入 active 状态: %s: %w", p.ServiceName(), active.Detail, activeErr))
		}
	}
	if managedState {
		updatedState := oldState
		updatedState.ConfigSHA256 = system.SHA256(patched)
		updatedState.UpdatedAt = now.UTC()
		if err := a.Store.Save(updatedState); err != nil {
			return false, rollback(fmt.Errorf("更新 ProxyForge 节点状态失败: %w", err))
		}
	}
	return running, nil
}

func validateRendered(ctx context.Context, p provider.CoreProvider, runner provider.Runner, configPath string, b []byte) error {
	dir := filepath.Dir(configPath)
	if err := os.MkdirAll(dir, 0750); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".proxyforge-validate-*.json")
	if err != nil {
		return err
	}
	path := f.Name()
	defer os.Remove(path)
	if err := f.Chmod(0600); err == nil {
		_, err = f.Write(b)
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return p.ValidateConfig(ctx, runner, path)
}

func validateTemporary(ctx context.Context, p provider.CoreProvider, runner provider.Runner, b []byte) error {
	f, err := os.CreateTemp("", "proxyforge-capability-*.json")
	if err != nil {
		return err
	}
	path := f.Name()
	defer os.Remove(path)
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(b)
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return p.ValidateConfig(ctx, runner, path)
}

type fileMetadata struct {
	mode     os.FileMode
	uid, gid int
}

func readMetadata(path string, existed bool) (fileMetadata, error) {
	if !existed {
		return fileMetadata{}, nil
	}
	info, err := os.Stat(path)
	if err != nil {
		return fileMetadata{}, err
	}
	meta := fileMetadata{mode: info.Mode().Perm()}
	if stat, ok := info.Sys().(*syscall.Stat_t); ok {
		meta.uid = int(stat.Uid)
		meta.gid = int(stat.Gid)
	}
	return meta, nil
}

func restoreFile(path string, old []byte, existed bool, metadata fileMetadata) error {
	if existed {
		mode := metadata.mode
		if mode == 0 {
			mode = 0600
		}
		if err := system.AtomicWrite(path, old, mode); err != nil {
			return err
		}
		if err := os.Chown(path, metadata.uid, metadata.gid); err != nil {
			return err
		}
		return os.Chmod(path, mode)
	}
	err := os.Remove(path)
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

func secureConfigForUser(path, username string) error {
	if username == "" || username == "root" {
		return os.Chmod(path, 0600)
	}
	u, err := user.Lookup(username)
	if err != nil {
		return fmt.Errorf("查找服务用户 %s: %w", username, err)
	}
	gid, _ := strconv.Atoi(u.Gid)
	if err := os.Chown(filepath.Dir(path), 0, gid); err != nil {
		return err
	}
	if err := os.Chmod(filepath.Dir(path), 0750); err != nil {
		return err
	}
	if err := os.Chown(path, 0, gid); err != nil {
		return err
	}
	return os.Chmod(path, 0640)
}

func checkPortFree(port int) error {
	ln, err := net.Listen("tcp4", fmt.Sprintf("0.0.0.0:%d", port))
	if err != nil {
		return fmt.Errorf("端口 %d 已被系统监听: %w", port, err)
	}
	if err := ln.Close(); err != nil {
		return err
	}
	ln6, err := net.Listen("tcp6", fmt.Sprintf("[::]:%d", port))
	if err != nil {
		return fmt.Errorf("端口 %d 已被系统监听: %w", port, err)
	}
	return ln6.Close()
}

func waitListening(ctx context.Context, port int, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		for _, host := range []string{"127.0.0.1", "::1"} {
			c, err := (&net.Dialer{Timeout: 200 * time.Millisecond}).DialContext(ctx, "tcp", net.JoinHostPort(host, strconv.Itoa(port)))
			if err == nil {
				c.Close()
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
	return fmt.Errorf("服务重启后端口 %d 未监听", port)
}
