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
	a.progressf("安装或升级内核", "%s", core)
	if err := a.RootCheck(); err != nil {
		return err
	}
	a.progressf("检查运行环境", "运行平台和 systemd")
	if err := system.CheckPlatform(a.Layout); err != nil {
		return err
	}
	p, err := a.Registry.Get(core)
	if err != nil {
		return err
	}
	previousVersion := ""
	if _, lookErr := a.lookPath(p.Binary()); lookErr == nil {
		previousVersion, _ = p.Version(ctx, a.Runner)
	}
	config := a.Layout.Resolve(p.ConfigPath())
	a.progressf("检查并备份配置", "%s", config)
	if backup, err := system.BackupFile(config, a.Layout.BackupRoot(core), a.Now()); err != nil {
		return err
	} else if backup != "" {
		a.progressf("备份位置", "%s", backup)
	}
	a.progressf("执行管理脚本", "下载并校验官方脚本")
	if _, err := a.Installer.Run(ctx, p, opts); err != nil {
		return err
	}
	a.progressf("核验安装结果", "已安装版本和 REALITY/Vision 能力")
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
	a.progressf("检查服务状态", "systemd")
	status, statusErr := a.Services.IsActive(ctx, p.ServiceName())
	running, err := installedServiceRunning(status, statusErr)
	if err != nil {
		return fmt.Errorf("安装完成但服务状态异常（%s）: %w", status.Detail, err)
	}
	if err := a.rememberInstallChannel(core, opts); err != nil {
		return fmt.Errorf("内核安装完成，但保存版本渠道失败: %w", err)
	}
	printInstallSuccess(a.Out, core, previousVersion, version, running)
	return nil
}

func printInstallSuccess(w io.Writer, core, previousVersion, version string, running bool) {
	title, versionLine := installResultSummary(previousVersion, version)
	serviceStatus := "未运行  -- inactive"
	if running {
		serviceStatus = "运行中  -- active"
	}
	rows := [][2]string{
		{"内核版本", versionLine},
		{"服务状态", serviceStatus},
	}
	if !running {
		rows = append(rows, [2]string{"说明", "尚未生成服务端配置时服务不运行，生成配置后会自动启动。"})
	}
	system.WriteLabeledCard(w, core+" "+title, rows)
}

func installResultSummary(previous, current string) (string, string) {
	previous = strings.TrimSpace(previous)
	current = strings.TrimSpace(current)
	switch {
	case previous == "":
		return "安装成功", current
	case previous == current:
		return "版本未变化", current + "  -- 与安装前相同"
	default:
		return "升级成功", previous + "  ->  " + current
	}
}

func (a *App) Uninstall(ctx context.Context, core string, opts install.Options) error {
	a.progressf("卸载内核", "%s", core)
	if err := a.RootCheck(); err != nil {
		return err
	}
	if err := system.CheckPlatform(a.Layout); err != nil {
		return err
	}
	a.progressf("检查卸载状态", "现有配置和安装状态")
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
		a.progressf("跳过重复卸载", "未检测到 %s 二进制、systemd unit、运行中或已启用的服务", core)
	}

	configPath := a.Layout.Resolve(p.ConfigPath())
	_, readErr := os.Stat(configPath)
	hadConfig := readErr == nil
	if readErr != nil && !os.IsNotExist(readErr) {
		return readErr
	}
	if hadConfig {
		a.progressf("备份卸载配置", "%s（卸载成功后会自动清理）", configPath)
		if backup, err := system.BackupFile(configPath, a.Layout.BackupRoot(core), a.Now()); err != nil {
			return err
		} else if backup != "" {
			a.progressf("备份位置", "%s", backup)
		}
	}

	if !alreadyUninstalled {
		if before.unit || before.serviceRunning || before.serviceEnabled {
			a.progressf("停用服务", "%s", p.ServiceName())
			if err := a.Services.DisableNow(ctx, p.ServiceName()); err != nil {
				return fmt.Errorf("停止并禁用 %s 失败: %w", p.ServiceName(), err)
			}
		}
		a.progressf("移除内核文件", "软件包和 systemd unit")
		if err := a.Installer.Uninstall(ctx, p, opts); err != nil {
			return err
		}
		a.progressf("刷新服务缓存", "systemd unit")
		if err := a.Services.DaemonReload(ctx); err != nil {
			return fmt.Errorf("内核卸载后刷新 systemd 失败；活动配置和 ProxyForge 状态已保留: %w", err)
		}
		a.progressf("核验卸载结果", "二进制、systemd unit、运行状态和开机启动状态")
		if err := a.verifyUninstalled(ctx, p); err != nil {
			return err
		}
	}
	a.progressf("清理卸载残留", "%s，卸载完成", core)
	if err := a.Cleanup(ctx, core); err != nil {
		return fmt.Errorf("内核已卸载，但自动清理失败: %w", err)
	}
	system.WriteDetail(a.Out, "操作结果", "%s 已卸载并完成残留清理。", core)
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
	a.progressf("清理卸载残留", "%s", target)
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
		a.progressf("确认卸载状态", "%s", p.Name())
		artifacts, err := a.inspectUninstallArtifacts(ctx, p)
		if err != nil {
			return fmt.Errorf("无法确认 %s 已完全卸载，拒绝清理: %w", p.Name(), err)
		}
		if remaining := describeUninstallArtifacts(p, artifacts); len(remaining) != 0 {
			return fmt.Errorf("仍检测到 %s 未完成卸载：%s；请先执行 uninstall", p.Name(), strings.Join(remaining, "、"))
		}
		a.progressf("继续清理", "未检测到 %s", p.Name())
	}

	var cleanupErrors []error
	for _, p := range providers {
		if p.Name() == domain.CoreXray {
			a.progressf("清理专用账号", "ProxyForge 创建的 Xray 系统账号")
			if err := a.cleanupOwnedXrayServiceAccount(ctx); err != nil {
				return fmt.Errorf("清理 Xray 专用系统账号失败；尚未删除其他残留: %w", err)
			}
		}
	}
	for _, p := range providers {
		paths := []string{
			a.Layout.StatePath(p.Name()),
			a.Layout.AccessPolicyPath(p.Name()),
			a.Layout.LandingPath(p.Name()),
			a.Layout.RelayPath(p.Name()),
			a.Layout.TrustPath(p.Name()),
			a.Layout.InstallPreferencesPath(p.Name()),
			a.Layout.BackupRoot(p.Name()),
			a.Layout.TLSRoot(p.Name()),
		}
		for _, path := range p.CleanupPaths() {
			paths = append(paths, a.Layout.Resolve(path))
		}
		for _, path := range paths {
			a.progressf("删除残留路径", "%s", path)
			if err := removeCleanupPath(path); err != nil {
				cleanupErrors = append(cleanupErrors, fmt.Errorf("删除 %s: %w", path, err))
			}
		}
	}
	if target == "all" {
		path := a.Layout.Resolve("/var/lib/proxyforge")
		a.progressf("清理管理数据", "%s", path)
		if err := removeCleanupPath(path); err != nil {
			cleanupErrors = append(cleanupErrors, fmt.Errorf("删除 %s: %w", path, err))
		}
	} else if err := a.removeEmptyProxyForgeRoot(); err != nil {
		cleanupErrors = append(cleanupErrors, err)
	}
	a.progressf("刷新服务缓存", "systemd unit")
	if err := a.Services.DaemonReload(ctx); err != nil {
		cleanupErrors = append(cleanupErrors, fmt.Errorf("清理后刷新 systemd: %w", err))
	}
	if len(cleanupErrors) != 0 {
		return fmt.Errorf("清理未完全完成: %w", errors.Join(cleanupErrors...))
	}
	system.WriteDetail(a.Out, "操作结果", "%s 的卸载残留已清理。", target)
	return nil
}

// CheckCleanup verifies native uninstall prerequisites without changing files.
func (a *App) CheckCleanup(ctx context.Context, target string) error {
	if err := a.RootCheck(); err != nil {
		return err
	}
	cores := []string{target}
	if target == "all" {
		cores = a.Registry.Names()
	}
	for _, core := range cores {
		p, err := a.Registry.Get(core)
		if err != nil {
			return err
		}
		artifacts, err := a.inspectUninstallArtifacts(ctx, p)
		if err != nil {
			return err
		}
		if remaining := describeUninstallArtifacts(p, artifacts); len(remaining) > 0 {
			return fmt.Errorf("仍检测到 %s 未完成卸载：%s；请先执行 uninstall", core, strings.Join(remaining, "、"))
		}
	}
	return nil
}

func (a *App) removeEmptyProxyForgeRoot() error {
	root := a.Layout.Resolve("/var/lib/proxyforge")
	for _, path := range []string{
		filepath.Join(root, "state"),
		filepath.Join(root, "access"),
		filepath.Join(root, "landings"),
		filepath.Join(root, "relays"),
		filepath.Join(root, "trust"),
		filepath.Join(root, "preferences"),
		filepath.Join(root, "backups"),
		filepath.Join(root, "tls"),
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
