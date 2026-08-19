package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	"proxyforge/internal/domain"
	"proxyforge/internal/install"
	"proxyforge/internal/provider"
	"proxyforge/internal/system"
)

func (a *App) Install(ctx context.Context, core string, opts install.Options) error {
	a.progressf("开始安装/升级 %s", core)
	if err := a.RootCheck(); err != nil {
		return err
	}
	a.progressf("检查运行平台和 systemd 环境")
	if err := system.CheckPlatform(a.Layout); err != nil {
		return err
	}
	p, err := a.Registry.Get(core)
	if err != nil {
		return err
	}
	resultAction := "安装"
	previousVersion := ""
	if _, lookErr := a.lookPath(p.Binary()); lookErr == nil {
		resultAction = "升级"
		previousVersion, _ = p.Version(ctx, a.Runner)
	}
	config := a.Layout.Resolve(p.ConfigPath())
	a.progressf("检查并备份现有配置 %s", config)
	if backup, err := system.BackupFile(config, a.Layout.BackupRoot(core), a.Now()); err != nil {
		return err
	} else if backup != "" {
		a.progressf("现有配置已备份到 %s", backup)
	}
	a.progressf("下载、校验并执行官方管理脚本")
	if _, err := a.Installer.Run(ctx, p, opts); err != nil {
		return err
	}
	a.progressf("检测已安装版本和 REALITY/Vision 能力")
	version, err := p.Version(ctx, a.Runner)
	if err != nil {
		return fmt.Errorf("安装后能力检测失败: %w", err)
	}
	if opts.Version != "" && !versionMatches(version, opts.Version) {
		return fmt.Errorf("请求版本 %s，但安装后的实际版本为 %s", opts.Version, version)
	}
	keys, err := p.GenerateKeyPair(ctx, a.Runner)
	if err != nil {
		return fmt.Errorf("版本 %s 不支持所需 REALITY 密钥命令: %w", version, err)
	}
	uuid, err := system.UUID()
	if err != nil {
		return err
	}
	shortID, err := system.ShortID()
	if err != nil {
		return err
	}
	probe := domain.NodeSpec{Core: core, InboundTag: domain.DefaultInboundTag(core), Server: "127.0.0.1", Port: 443, SNI: "example.com", Target: "example.com:443", UserName: domain.DefaultUserName, UUID: uuid, PrivateKey: keys.Private, PublicKey: keys.Public, ShortID: shortID, CoreVersion: version}
	serverConfig, err := p.RenderServer(probe)
	if err != nil {
		return fmt.Errorf("渲染能力检测配置: %w", err)
	}
	clientConfig, err := p.RenderClient(probe)
	if err != nil {
		return fmt.Errorf("渲染能力检测客户端: %w", err)
	}
	if err := validateTemporary(ctx, p, a.Runner, serverConfig); err != nil {
		return fmt.Errorf("版本 %s 不支持服务端 REALITY/Vision 配置: %w", version, err)
	}
	if err := validateTemporary(ctx, p, a.Runner, clientConfig); err != nil {
		return fmt.Errorf("版本 %s 不支持客户端 REALITY/Vision 配置: %w", version, err)
	}
	if _, err := a.Runner.Run(ctx, "systemctl", "cat", p.ServiceName()); err != nil {
		return fmt.Errorf("未找到 systemd unit %s: %w", p.ServiceName(), err)
	}
	a.progressf("检查 systemd 服务状态")
	status, statusErr := a.Services.IsActive(ctx, p.ServiceName())
	running, err := installedServiceRunning(status, statusErr)
	if err != nil {
		return fmt.Errorf("安装完成但服务状态异常（%s）: %w", status.Detail, err)
	}
	if running {
		printInstallSuccess(a.Out, core, resultAction, previousVersion, version, true)
		return nil
	}
	printInstallSuccess(a.Out, core, resultAction, previousVersion, version, false)
	fmt.Fprintln(a.Out, "[提示] 服务当前为 inactive；这是尚未生成服务端配置时的正常状态。请继续选择“生成服务端配置”，配置成功后服务会自动启动。")
	return nil
}

func printInstallSuccess(w io.Writer, core, action, previousVersion, version string, running bool) {
	const border = "========================================================"
	serviceStatus := "inactive（尚未运行）"
	if running {
		serviceStatus = "active（运行中）"
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, border)
	fmt.Fprintf(w, "  [结果] %s %s成功\n", core, action)
	fmt.Fprintln(w, border)
	if action == "升级" && previousVersion != "" {
		fmt.Fprintf(w, "版本：%s  ->  %s\n", previousVersion, version)
	} else {
		fmt.Fprintf(w, "版本：%s\n", version)
	}
	fmt.Fprintf(w, "服务：%s\n", serviceStatus)
	fmt.Fprintln(w, border)
}

func (a *App) Uninstall(ctx context.Context, core string, opts install.Options) error {
	a.progressf("开始卸载 %s", core)
	if err := a.RootCheck(); err != nil {
		return err
	}
	if err := system.CheckPlatform(a.Layout); err != nil {
		return err
	}
	a.progressf("检查现有配置和卸载状态")
	p, err := a.Registry.Get(core)
	if err != nil {
		return err
	}
	before, err := a.inspectUninstallArtifacts(ctx, p)
	if err != nil {
		return err
	}
	alreadyUninstalled := !before.binary && !before.unit && !before.serviceRunning && !before.serviceEnabled
	if alreadyUninstalled {
		a.progressf("未检测到 %s 二进制、systemd unit、运行中或已启用的服务；跳过重复卸载", core)
	}

	configPath := a.Layout.Resolve(p.ConfigPath())
	_, readErr := os.Stat(configPath)
	hadConfig := readErr == nil
	if readErr != nil && !os.IsNotExist(readErr) {
		return readErr
	}
	if hadConfig {
		a.progressf("卸载前临时备份配置 %s（卸载成功后会自动清理）", configPath)
		if backup, err := system.BackupFile(configPath, a.Layout.BackupRoot(core), a.Now()); err != nil {
			return err
		} else if backup != "" {
			a.progressf("配置已备份到 %s", backup)
		}
	}

	if !alreadyUninstalled {
		if before.unit || before.serviceRunning || before.serviceEnabled {
			a.progressf("手动停止并禁用 systemd 服务 %s", p.ServiceName())
			if err := a.Services.DisableNow(ctx, p.ServiceName()); err != nil {
				return fmt.Errorf("停止并禁用 %s 失败: %w", p.ServiceName(), err)
			}
		}
		a.progressf("卸载内核软件包和 systemd unit")
		if err := a.Installer.Uninstall(ctx, p, opts); err != nil {
			return err
		}
		a.progressf("刷新 systemd unit 缓存")
		if err := a.Services.DaemonReload(ctx); err != nil {
			return fmt.Errorf("内核卸载后刷新 systemd 失败；活动配置和 ProxyForge 状态已保留: %w", err)
		}
		a.progressf("核验二进制、systemd unit、运行状态和开机启动状态")
		if err := a.verifyUninstalled(ctx, p); err != nil {
			return err
		}
	}
	a.progressf("卸载完成，自动清理 %s 的所有残留", core)
	if err := a.Cleanup(ctx, core); err != nil {
		return fmt.Errorf("内核已卸载，但自动清理失败: %w", err)
	}
	fmt.Fprintf(a.Out, "[结果] %s 已卸载并完成残留清理。\n", core)
	return nil
}

type uninstallArtifacts struct {
	binary         bool
	unit           bool
	serviceRunning bool
	serviceEnabled bool
	unitLoadState  string
	serviceState   string
	enabledState   string
}

func (a *App) inspectUninstallArtifacts(ctx context.Context, p provider.CoreProvider) (uninstallArtifacts, error) {
	var result uninstallArtifacts
	path, lookErr := a.lookPath(p.Binary())
	if lookErr == nil || path != "" {
		result.binary = true
	} else if !errors.Is(lookErr, exec.ErrNotFound) {
		return result, fmt.Errorf("检查二进制 %s: %w", p.Binary(), lookErr)
	}
	loadState, err := a.Services.UnitLoadState(ctx, p.ServiceName())
	if err != nil {
		return result, err
	}
	result.unitLoadState = loadState
	result.unit = loadState != "not-found"
	status, statusErr := a.Services.IsActive(ctx, p.ServiceName())
	result.serviceState = strings.TrimSpace(status.Detail)
	switch result.serviceState {
	case "inactive", "failed", "unknown":
		// systemctl uses a non-zero exit status for these normal non-running states.
	case "":
		if statusErr != nil && result.unit {
			return result, fmt.Errorf("检查 %s 服务状态: %w", p.ServiceName(), statusErr)
		}
	default:
		result.serviceRunning = true
	}
	if status.Active {
		result.serviceRunning = true
	}
	enabledState, enabledErr := a.Services.EnabledState(ctx, p.ServiceName())
	result.enabledState = enabledState
	switch enabledState {
	case "enabled", "enabled-runtime", "linked", "linked-runtime", "alias":
		result.serviceEnabled = true
	case "disabled", "static", "indirect", "generated", "transient", "not-found", "masked", "masked-runtime":
		// These states do not install a persistent boot-time enablement link.
	case "":
		if enabledErr != nil && result.unit {
			return result, fmt.Errorf("检查 %s 开机启动状态: %w", p.ServiceName(), enabledErr)
		}
	default:
		// Treat an unfamiliar state as a remaining artifact instead of silently
		// accepting a potentially enabled service.
		result.serviceEnabled = true
	}
	return result, nil
}

func (a *App) verifyUninstalled(ctx context.Context, p provider.CoreProvider) error {
	artifacts, err := a.inspectUninstallArtifacts(ctx, p)
	if err != nil {
		return fmt.Errorf("卸载命令已完成，但无法完成卸载后核验；活动配置和 ProxyForge 状态已保留: %w", err)
	}
	remaining := describeUninstallArtifacts(p, artifacts)
	if len(remaining) > 0 {
		return fmt.Errorf("卸载命令已完成，但卸载后核验失败，仍存在：%s；活动配置和 ProxyForge 状态已保留", strings.Join(remaining, "、"))
	}
	return nil
}

func describeUninstallArtifacts(p provider.CoreProvider, artifacts uninstallArtifacts) []string {
	var remaining []string
	if artifacts.binary {
		remaining = append(remaining, "二进制 "+p.Binary())
	}
	if artifacts.unit {
		remaining = append(remaining, fmt.Sprintf("systemd unit %s（LoadState=%s）", p.ServiceName(), artifacts.unitLoadState))
	}
	if artifacts.serviceRunning {
		remaining = append(remaining, fmt.Sprintf("服务 %s（状态=%s）", p.ServiceName(), artifacts.serviceState))
	}
	if artifacts.serviceEnabled {
		remaining = append(remaining, fmt.Sprintf("服务 %s 仍启用开机启动（状态=%s）", p.ServiceName(), artifacts.enabledState))
	}
	return remaining
}

func (a *App) Cleanup(ctx context.Context, target string) error {
	a.progressf("开始清理 %s 的卸载残留", target)
	if err := a.RootCheck(); err != nil {
		return err
	}
	cores := []string{target}
	if target == "all" {
		cores = a.Registry.Names()
	}
	providers := make([]provider.CoreProvider, 0, len(cores))
	for _, core := range cores {
		p, err := a.Registry.Get(core)
		if err != nil {
			return err
		}
		providers = append(providers, p)
	}
	for _, p := range providers {
		a.progressf("确认 %s 已卸载", p.Name())
		artifacts, err := a.inspectUninstallArtifacts(ctx, p)
		if err != nil {
			return fmt.Errorf("无法确认 %s 已完全卸载，拒绝清理: %w", p.Name(), err)
		}
		if remaining := describeUninstallArtifacts(p, artifacts); len(remaining) != 0 {
			return fmt.Errorf("仍检测到 %s 未完成卸载：%s；请先执行 uninstall", p.Name(), strings.Join(remaining, "、"))
		}
		a.progressf("未检测到 %s，继续清理", p.Name())
	}

	var cleanupErrors []error
	for _, p := range providers {
		paths := []string{
			a.Layout.StatePath(p.Name()),
			a.Layout.TrustPath(p.Name()),
			a.Layout.BackupRoot(p.Name()),
		}
		for _, path := range p.CleanupPaths() {
			paths = append(paths, a.Layout.Resolve(path))
		}
		for _, path := range paths {
			a.progressf("删除残留路径 %s", path)
			if err := removeCleanupPath(path); err != nil {
				cleanupErrors = append(cleanupErrors, fmt.Errorf("删除 %s: %w", path, err))
			}
		}
	}
	if target == "all" {
		path := a.Layout.Resolve("/var/lib/proxyforge")
		a.progressf("删除 ProxyForge 数据根目录 %s", path)
		if err := removeCleanupPath(path); err != nil {
			cleanupErrors = append(cleanupErrors, fmt.Errorf("删除 %s: %w", path, err))
		}
	} else if err := a.removeEmptyProxyForgeRoot(); err != nil {
		cleanupErrors = append(cleanupErrors, err)
	}
	if len(cleanupErrors) != 0 {
		return fmt.Errorf("清理未完全完成: %w", errors.Join(cleanupErrors...))
	}
	fmt.Fprintf(a.Out, "[结果] %s 的卸载残留已清理。\n", target)
	return nil
}

func (a *App) removeEmptyProxyForgeRoot() error {
	root := a.Layout.Resolve("/var/lib/proxyforge")
	for _, path := range []string{
		filepath.Join(root, "state"),
		filepath.Join(root, "trust"),
		filepath.Join(root, "backups"),
		root,
	} {
		if err := removeEmptyCleanupDirectory(path); err != nil {
			return fmt.Errorf("删除空目录 %s: %w", path, err)
		}
	}
	return nil
}

func removeEmptyCleanupDirectory(path string) error {
	clean := filepath.Clean(path)
	if !filepath.IsAbs(clean) || clean == "/" {
		return fmt.Errorf("拒绝不安全的清理路径 %q", path)
	}
	err := os.Remove(clean)
	if err == nil || os.IsNotExist(err) || errors.Is(err, syscall.ENOTEMPTY) || errors.Is(err, syscall.EEXIST) {
		return nil
	}
	return err
}

func removeCleanupPath(path string) error {
	clean := filepath.Clean(path)
	if !filepath.IsAbs(clean) || clean == "/" {
		return fmt.Errorf("拒绝不安全的清理路径 %q", path)
	}
	return os.RemoveAll(clean)
}

func installedServiceRunning(status domain.ServiceStatus, checkErr error) (bool, error) {
	detail := strings.TrimSpace(status.Detail)
	if status.Active || detail == "active" {
		return true, nil
	}
	if detail == "inactive" {
		return false, nil
	}
	if checkErr != nil {
		return false, checkErr
	}
	if detail == "" {
		detail = "未知"
	}
	return false, fmt.Errorf("未识别的服务状态 %q", detail)
}

func versionMatches(actual, requested string) bool {
	requested = strings.TrimPrefix(strings.TrimSpace(requested), "v")
	for _, field := range strings.Fields(actual) {
		candidate := strings.Trim(strings.TrimPrefix(field, "v"), "(),;")
		if candidate == requested {
			return true
		}
	}
	return false
}
