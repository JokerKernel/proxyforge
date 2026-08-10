package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"proxyforge/internal/domain"
	"proxyforge/internal/install"
	"proxyforge/internal/provider"
	"proxyforge/internal/provider/mihomo"
	"proxyforge/internal/system"
)

const (
	ClientFormatNative = "native"
	ClientFormatClash  = "clash"
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

type DNSSettings struct {
	Current  string
	Profiles []string
}

type DNSChange struct {
	Previous  string
	Current   string
	Restarted bool
	Changed   bool
}

type App struct {
	Registry  *provider.Registry
	Runner    provider.Runner
	Layout    system.Layout
	Store     system.StateStore
	Services  system.ServiceManager
	Installer install.Installer
	Targets   TargetValidator
	Out       io.Writer
	Progress  io.Writer
	Now       func() time.Time
	RootCheck func() error
	LookPath  func(string) (string, error)
	PortFree  func(int) error
	Listening func(context.Context, int, time.Duration) error
}

func New(reg *provider.Registry, runner provider.Runner, layout system.Layout, out io.Writer) *App {
	return &App{
		Registry: reg, Runner: runner, Layout: layout, Store: system.StateStore{Layout: layout},
		Services: system.ServiceManager{Runner: runner}, Installer: install.Installer{Runner: runner, Layout: layout, Output: out},
		Targets: NetworkTargetValidator{}, Out: out, Progress: out, Now: time.Now,
		RootCheck: RequireRoot, LookPath: exec.LookPath, PortFree: checkPortFree, Listening: waitListening,
	}
}

func (a *App) progressf(format string, args ...any) {
	if a.Progress == nil {
		return
	}
	fmt.Fprintf(a.Progress, "[ProxyForge/步骤] "+format+"\n", args...)
}

func RequireRoot() error {
	if os.Geteuid() != 0 {
		return fmt.Errorf("此操作必须以 root 运行")
	}
	return nil
}

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
	fmt.Fprintln(a.Out, "[ProxyForge/提示] 服务当前为 inactive；这是尚未生成服务端配置时的正常状态。请继续选择“生成服务端配置”，配置成功后服务会自动启动。")
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
	fmt.Fprintf(w, "  [ProxyForge/结果] %s %s成功\n", core, action)
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
	fmt.Fprintf(a.Out, "[ProxyForge/结果] %s 已卸载并完成残留清理。\n", core)
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

func (a *App) lookPath(name string) (string, error) {
	if a.LookPath != nil {
		return a.LookPath(name)
	}
	return exec.LookPath(name)
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
	fmt.Fprintf(a.Out, "[ProxyForge/结果] %s 的卸载残留已清理。\n", target)
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

func (a *App) Generate(ctx context.Context, core string, opts domain.GenerateOptions) (domain.NodeSpec, error) {
	a.progressf("开始生成并应用 %s 服务端配置", core)
	if err := a.RootCheck(); err != nil {
		return domain.NodeSpec{}, err
	}
	p, err := a.Registry.Get(core)
	if err != nil {
		return domain.NodeSpec{}, err
	}
	if err := a.checkCoreInstalled(ctx, p); err != nil {
		return domain.NodeSpec{}, err
	}
	if opts.SimplifiedConfig && core != domain.CoreSingBox {
		return domain.NodeSpec{}, fmt.Errorf("简化服务端配置目前仅支持 sing-box")
	}
	if opts.SingBoxFallbackGuard && opts.SimplifiedConfig {
		return domain.NodeSpec{}, fmt.Errorf("sing-box 简化配置不能与回落防偷跑配置同时启用")
	}
	if opts.SingBoxFallbackGuard && opts.SingBoxFallbackPort == 0 {
		opts.SingBoxFallbackPort = 4432
	}
	if (opts.SingBoxFallbackGuard || opts.SingBoxFallbackPort != 0) && core != domain.CoreSingBox {
		return domain.NodeSpec{}, fmt.Errorf("sing-box 回落防偷跑配置仅支持 sing-box")
	}
	if !opts.SingBoxFallbackGuard && opts.SingBoxFallbackPort != 0 {
		return domain.NodeSpec{}, fmt.Errorf("设置 sing-box 回落端口时必须同时启用 --sing-box-fallback-guard")
	}
	if opts.XrayFallbackGuard && opts.XrayFallbackPort == 0 {
		opts.XrayFallbackPort = 4431
	}
	if (opts.XrayFallbackGuard || opts.XrayFallbackPort != 0) && core != domain.CoreXray {
		return domain.NodeSpec{}, fmt.Errorf("REALITY 回落防偷跑配置仅支持 xray")
	}
	if !opts.XrayFallbackGuard && opts.XrayFallbackPort != 0 {
		return domain.NodeSpec{}, fmt.Errorf("设置 Xray 回落端口时必须同时启用 --xray-fallback-guard")
	}
	if err := validateGenerate(opts); err != nil {
		return domain.NodeSpec{}, err
	}
	if opts.Target == "" {
		opts.Target = net.JoinHostPort(opts.SNI, "443")
	}
	a.progressf("验证服务地址、端口、SNI 和 REALITY target")
	warnings, err := a.Targets.Validate(ctx, opts.Target, opts.SNI, opts.Server)
	if err != nil {
		return domain.NodeSpec{}, err
	}
	for _, warning := range warnings {
		fmt.Fprintln(a.Out, "[ProxyForge/警告] "+warning)
	}
	old, loadErr := a.Store.Load(core)
	hasOld := loadErr == nil
	if loadErr != nil && !errors.Is(loadErr, system.ErrNoState) {
		return domain.NodeSpec{}, loadErr
	}
	userName := strings.TrimSpace(opts.UserName)
	if userName == "" && hasOld {
		userName = strings.TrimSpace(old.UserName)
	}
	if userName == "" {
		userName = domain.DefaultUserName
	}
	if err := system.ValidateUserName(userName); err != nil {
		return domain.NodeSpec{}, err
	}
	inboundTag := strings.TrimSpace(opts.InboundTag)
	if inboundTag == "" && hasOld {
		inboundTag = strings.TrimSpace(old.InboundTag)
	}
	if inboundTag == "" {
		inboundTag = domain.DefaultInboundTag(core)
	}
	if err := system.ValidateTag(inboundTag); err != nil {
		return domain.NodeSpec{}, err
	}
	if opts.XrayFallbackGuard && inboundTag == "dokodemo-in" {
		return domain.NodeSpec{}, fmt.Errorf("入站标签 %q 已由 Xray 回落防偷跑入站使用，请选择其他标签", inboundTag)
	}
	if opts.SingBoxFallbackGuard && inboundTag == "singbox-fallback-in" {
		return domain.NodeSpec{}, fmt.Errorf("入站标签 %q 已由 sing-box 回落防偷跑入站使用，请选择其他标签", inboundTag)
	}
	other := domain.CoreSingBox
	if core == other {
		other = domain.CoreXray
	}
	if otherState, e := a.Store.Load(other); e == nil {
		otherFallbackPort := nodeFallbackPort(otherState)
		if otherState.Port == opts.Port || otherFallbackPort == opts.Port {
			return domain.NodeSpec{}, fmt.Errorf("端口 %d 已由受管的 %s 节点使用", opts.Port, other)
		}
		fallbackPort := optionsFallbackPort(opts)
		if fallbackPort != 0 && (otherState.Port == fallbackPort || otherFallbackPort == fallbackPort) {
			return domain.NodeSpec{}, fmt.Errorf("回落端口 %d 已由受管的 %s 节点使用", fallbackPort, other)
		}
	}
	a.progressf("检测 %s 版本和配置能力", core)
	version, err := p.Version(ctx, a.Runner)
	if err != nil {
		return domain.NodeSpec{}, fmt.Errorf("内核不可用或不支持所需能力: %w", err)
	}
	n := domain.NodeSpec{
		ManagedBy: "proxyforge", Core: core, InboundTag: inboundTag, Server: opts.Server, Port: opts.Port,
		SNI: opts.SNI, Target: opts.Target, UserName: userName, SimplifiedConfig: opts.SimplifiedConfig,
		SingBoxFallbackGuard: opts.SingBoxFallbackGuard, SingBoxFallbackPort: opts.SingBoxFallbackPort,
		XrayFallbackGuard: opts.XrayFallbackGuard, XrayFallbackPort: opts.XrayFallbackPort,
		CoreVersion: version, UpdatedAt: a.Now().UTC(),
	}
	if n.SimplifiedConfig {
		fmt.Fprintln(a.Out, "[ProxyForge/警告] 已选择 sing-box 简化配置；域名将在出站连接阶段由系统 DNS 解析，域名解析到私网地址时可能绕过路由私网拦截。")
	}
	if hasOld && !opts.RotateCredentials {
		a.progressf("保留现有 UUID、REALITY 密钥和 short ID")
		n.UUID, n.PrivateKey, n.PublicKey, n.ShortID = old.UUID, old.PrivateKey, old.PublicKey, old.ShortID
	} else {
		a.progressf("生成新的 UUID、REALITY 密钥和 short ID")
		if n.UUID, err = system.UUID(); err != nil {
			return n, err
		}
		if n.ShortID, err = system.ShortID(); err != nil {
			return n, err
		}
		keys, keyErr := p.GenerateKeyPair(ctx, a.Runner)
		if keyErr != nil {
			return n, keyErr
		}
		n.PrivateKey, n.PublicKey = keys.Private, keys.Public
	}
	if hasOld && opts.RotateCredentials && (n.UUID == old.UUID || n.PrivateKey == old.PrivateKey || n.PublicKey == old.PublicKey || n.ShortID == old.ShortID) {
		return n, fmt.Errorf("凭据生成器返回了重复值，已拒绝修改现有节点")
	}
	config, err := p.RenderServer(n)
	if err != nil {
		return n, err
	}
	return a.applyServerConfig(ctx, p, core, n, old, hasOld, config, true)
}

func (a *App) CheckCoreInstalled(ctx context.Context, core string) error {
	if err := a.RootCheck(); err != nil {
		return err
	}
	p, err := a.Registry.Get(core)
	if err != nil {
		return err
	}
	return a.checkCoreInstalled(ctx, p)
}

func (a *App) checkCoreInstalled(ctx context.Context, p provider.CoreProvider) error {
	path, err := a.lookPath(p.Binary())
	if errors.Is(err, exec.ErrNotFound) || path == "" {
		return fmt.Errorf("尚未安装 %s：未找到内核二进制 %s；请先执行安装/升级", p.Name(), p.Binary())
	}
	if err != nil {
		return fmt.Errorf("检查 %s 内核二进制 %s: %w", p.Name(), p.Binary(), err)
	}
	loadState, err := a.Services.UnitLoadState(ctx, p.ServiceName())
	if err != nil {
		return fmt.Errorf("检查 %s 是否已安装: %w", p.Name(), err)
	}
	if loadState == "not-found" {
		return fmt.Errorf("%s 安装不完整：未找到 systemd unit %s；请先执行安装/升级", p.Name(), p.ServiceName())
	}
	return nil
}

func (a *App) applyServerConfig(ctx context.Context, p provider.CoreProvider, core string, n, old domain.NodeSpec, hasOld bool, config []byte, managedConfig bool) (domain.NodeSpec, error) {
	if managedConfig {
		n.ConfigSHA256 = system.SHA256(config)
	} else {
		n.ConfigSHA256 = ""
	}
	configPath := a.Layout.Resolve(p.ConfigPath())
	oldConfig, readErr := os.ReadFile(configPath)
	hadConfig := readErr == nil && !isEmptyServerConfigPlaceholder(oldConfig)
	if readErr != nil && !os.IsNotExist(readErr) {
		return n, readErr
	}
	oldMetadata, err := readMetadata(configPath, hadConfig)
	if err != nil {
		return n, err
	}
	a.progressf("使用 %s 原生命令校验候选配置", core)
	if err := validateRendered(ctx, p, a.Runner, configPath, config); err != nil {
		return n, err
	}
	if hadConfig {
		a.progressf("备份现有服务端配置 %s", configPath)
		if backup, err := system.BackupFile(configPath, a.Layout.BackupRoot(core), a.Now()); err != nil {
			return n, err
		} else if backup != "" {
			a.progressf("现有配置已备份到 %s", backup)
		}
	}
	serviceStopped := false
	restoreStoppedService := func(cause error) error {
		if !serviceStopped {
			return cause
		}
		a.progressf("操作未应用，正在恢复 systemd 服务 %s", p.ServiceName())
		if _, startErr := a.Services.Action(ctx, p.ServiceName(), "start"); startErr != nil {
			return fmt.Errorf("%v；且恢复 %s 失败: %w", cause, p.ServiceName(), startErr)
		}
		serviceStopped = false
		return cause
	}
	checkPublicPort := !hasOld || old.Port != n.Port
	nFallbackPort, oldFallbackPort := nodeFallbackPort(n), nodeFallbackPort(old)
	checkFallbackPort := nFallbackPort != 0 && (!hasOld || oldFallbackPort != nFallbackPort)
	if checkPublicPort || checkFallbackPort {
		status, _ := a.Services.IsActive(ctx, p.ServiceName())
		if status.Active && checkPublicPort {
			a.progressf("临时停止 systemd 服务 %s 以检查监听端口", p.ServiceName())
			if _, err := a.Services.Action(ctx, p.ServiceName(), "stop"); err != nil {
				return n, fmt.Errorf("停止 %s 失败: %w", p.ServiceName(), err)
			}
			serviceStopped = true
		}
		ports := []int{}
		if checkPublicPort {
			ports = append(ports, n.Port)
		}
		if checkFallbackPort {
			ports = append(ports, nFallbackPort)
		}
		for _, port := range ports {
			a.progressf("检查监听端口 %d 是否可用", port)
			if err := a.PortFree(port); err != nil {
				return n, restoreStoppedService(err)
			}
		}
	}
	a.progressf("原子写入服务端配置 %s", configPath)
	if err := system.AtomicWrite(configPath, config, 0600); err != nil {
		return n, restoreStoppedService(err)
	}
	serviceUser := a.Services.User(ctx, p.ServiceName())
	a.progressf("按 systemd 服务用户 %s 设置配置权限", serviceUser)
	if err := secureConfigForUser(configPath, serviceUser); err != nil {
		_ = restoreFile(configPath, oldConfig, hadConfig, oldMetadata)
		return n, restoreStoppedService(err)
	}
	rollback := func(cause error) error {
		a.progressf("操作失败，正在恢复旧配置、状态和服务")
		restoreErr := restoreFile(configPath, oldConfig, hadConfig, oldMetadata)
		if hasOld {
			_ = a.Store.Save(old)
		} else {
			_ = a.Store.Delete(core)
		}
		_ = a.Services.Restart(ctx, p.ServiceName())
		if restoreErr != nil {
			return fmt.Errorf("%v；且恢复旧配置失败: %w", cause, restoreErr)
		}
		return fmt.Errorf("%v；已恢复旧配置和状态", cause)
	}
	a.progressf("重启 systemd 服务 %s", p.ServiceName())
	if err := a.Services.Restart(ctx, p.ServiceName()); err != nil {
		return n, rollback(fmt.Errorf("重启 %s 失败: %w", p.ServiceName(), err))
	}
	a.progressf("确认服务 active 并监听端口 %d", n.Port)
	status, err := a.Services.IsActive(ctx, p.ServiceName())
	if err != nil || !status.Active {
		return n, rollback(fmt.Errorf("%s 未进入 active 状态: %s: %w", p.ServiceName(), status.Detail, err))
	}
	if err := a.Listening(ctx, n.Port, 4*time.Second); err != nil {
		return n, rollback(err)
	}
	a.progressf("启用 systemd 服务 %s 开机启动", p.ServiceName())
	if err := a.Services.Enable(ctx, p.ServiceName()); err != nil {
		return n, rollback(fmt.Errorf("启用 %s 开机启动失败: %w", p.ServiceName(), err))
	}
	a.progressf("保存 ProxyForge 节点状态")
	if err := a.Store.Save(n); err != nil {
		return n, rollback(fmt.Errorf("保存状态失败: %w", err))
	}
	a.firewallHint(n.Port)
	return n, nil
}

// ResetCredentials atomically rotates every client credential, optionally
// changing SNI and target while preserving the node's address and port.
func (a *App) ResetCredentials(ctx context.Context, core string, opts domain.ResetOptions) (domain.NodeSpec, error) {
	a.progressf("开始重置 %s 节点凭据", core)
	if err := a.RootCheck(); err != nil {
		return domain.NodeSpec{}, err
	}
	p, err := a.Registry.Get(core)
	if err != nil {
		return domain.NodeSpec{}, err
	}
	current, err := a.Store.Load(core)
	if err != nil {
		return domain.NodeSpec{}, err
	}
	requestedSNI := strings.TrimSpace(opts.SNI)
	requestedTarget := strings.TrimSpace(opts.Target)
	updateEndpoint := requestedSNI != "" || requestedTarget != ""
	desiredSNI := requestedSNI
	if desiredSNI == "" {
		desiredSNI = current.SNI
	}
	desiredTarget := requestedTarget
	if desiredTarget == "" {
		desiredTarget = current.Target
		if requestedSNI != "" && desiredSNI != current.SNI {
			desiredTarget = net.JoinHostPort(desiredSNI, "443")
		}
	}
	if updateEndpoint {
		a.progressf("验证新的 SNI 和 REALITY target")
		warnings, err := a.Targets.Validate(ctx, desiredTarget, desiredSNI, current.Server)
		if err != nil {
			return domain.NodeSpec{}, err
		}
		for _, warning := range warnings {
			fmt.Fprintln(a.Out, "[ProxyForge/警告] "+warning)
		}
	}
	a.progressf("检测 %s 版本并生成新的 UUID、REALITY 密钥和 short ID", core)
	version, err := p.Version(ctx, a.Runner)
	if err != nil {
		return domain.NodeSpec{}, fmt.Errorf("内核不可用或不支持所需能力: %w", err)
	}
	n := current
	n.SNI = desiredSNI
	n.Target = desiredTarget
	n.CoreVersion = version
	n.UpdatedAt = a.Now().UTC()
	if n.UUID, err = system.UUID(); err != nil {
		return n, err
	}
	if n.ShortID, err = system.ShortID(); err != nil {
		return n, err
	}
	keys, err := p.GenerateKeyPair(ctx, a.Runner)
	if err != nil {
		return n, err
	}
	n.PrivateKey, n.PublicKey = keys.Private, keys.Public
	if n.UUID == current.UUID || n.PrivateKey == current.PrivateKey || n.PublicKey == current.PublicKey || n.ShortID == current.ShortID {
		return n, fmt.Errorf("凭据生成器返回了重复值，已拒绝修改现有节点")
	}
	configPath := a.Layout.Resolve(p.ConfigPath())
	currentConfig, err := os.ReadFile(configPath)
	if os.IsNotExist(err) {
		return n, fmt.Errorf("尚未找到 %s 服务端配置 %s，无法定点重置", core, p.ConfigPath())
	}
	if err != nil {
		return n, fmt.Errorf("读取现有 %s 服务端配置: %w", core, err)
	}
	managedConfig := current.ConfigSHA256 != "" && system.SHA256(currentConfig) == current.ConfigSHA256
	if !managedConfig {
		fmt.Fprintln(a.Out, "[ProxyForge/提示] 检测到配置包含手动修改；本次只更新受管节点字段，其他内容和外部配置属性将保留。")
	}
	a.progressf("仅更新现有配置中的受管节点字段，保留其他手动配置")
	patched, err := p.PatchServer(currentConfig, current, n, updateEndpoint)
	if err != nil {
		return n, err
	}
	return a.applyServerConfig(ctx, p, core, n, current, true, patched, managedConfig)
}

func (a *App) Client(ctx context.Context, core, output string, force bool) ([]byte, error) {
	return a.ClientConfig(ctx, core, ClientFormatNative, output, force)
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

func (a *App) DNSSettings(ctx context.Context, core string) (DNSSettings, error) {
	if err := a.RootCheck(); err != nil {
		return DNSSettings{}, err
	}
	p, err := a.Registry.Get(core)
	if err != nil {
		return DNSSettings{}, err
	}
	if err := a.checkCoreInstalled(ctx, p); err != nil {
		return DNSSettings{}, err
	}
	dnsProvider, ok := p.(provider.DNSProfileProvider)
	if !ok {
		return DNSSettings{}, fmt.Errorf("%s 不支持 DNS 设置", core)
	}
	configPath := a.Layout.Resolve(p.ConfigPath())
	a.progressf("读取 %s 当前 DNS 设置", core)
	config, err := os.ReadFile(configPath)
	if os.IsNotExist(err) {
		return DNSSettings{}, fmt.Errorf("尚未找到 %s 服务端配置 %s", core, p.ConfigPath())
	}
	if err != nil {
		return DNSSettings{}, fmt.Errorf("读取 %s 服务端配置: %w", core, err)
	}
	current, err := dnsProvider.CurrentDNSProfile(config)
	if err != nil {
		return DNSSettings{}, err
	}
	return DNSSettings{Current: current, Profiles: dnsProvider.DNSProfiles()}, nil
}

func (a *App) SetDNSProfile(ctx context.Context, core, requested string) (DNSChange, error) {
	change := DNSChange{Current: strings.ToLower(strings.TrimSpace(requested))}
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
	dnsProvider, ok := p.(provider.DNSProfileProvider)
	if !ok {
		return change, fmt.Errorf("%s 不支持 DNS 设置", core)
	}
	configPath := a.Layout.Resolve(p.ConfigPath())
	a.progressf("读取并修改 %s DNS 设置", core)
	oldConfig, err := os.ReadFile(configPath)
	if os.IsNotExist(err) {
		return change, fmt.Errorf("尚未找到 %s 服务端配置 %s", core, p.ConfigPath())
	}
	if err != nil {
		return change, fmt.Errorf("读取 %s 服务端配置: %w", core, err)
	}
	change.Previous, err = dnsProvider.CurrentDNSProfile(oldConfig)
	if err != nil {
		return change, err
	}
	patched, err := dnsProvider.PatchDNSProfile(oldConfig, change.Current)
	if err != nil {
		return change, err
	}
	if change.Previous == change.Current {
		return change, nil
	}
	change.Restarted, err = a.applyServerSetting(ctx, p, core, "DNS", oldConfig, patched)
	if err != nil {
		return change, err
	}
	change.Changed = true
	return change, nil
}

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

func optionsFallbackPort(o domain.GenerateOptions) int {
	if o.SingBoxFallbackGuard {
		return o.SingBoxFallbackPort
	}
	if o.XrayFallbackGuard {
		return o.XrayFallbackPort
	}
	return 0
}

func nodeFallbackPort(n domain.NodeSpec) int {
	if n.SingBoxFallbackGuard {
		return n.SingBoxFallbackPort
	}
	if n.XrayFallbackGuard {
		return n.XrayFallbackPort
	}
	return 0
}

func validateGenerate(o domain.GenerateOptions) error {
	if o.NonInteractive && (o.Server == "" || o.Port == 0 || o.SNI == "") {
		return fmt.Errorf("非交互模式必须显式提供 --server、--port 和 --sni")
	}
	if err := system.ValidateServer(o.Server); err != nil {
		return err
	}
	if err := system.ValidatePort(o.Port); err != nil {
		return err
	}
	if o.XrayFallbackGuard {
		if err := system.ValidatePort(o.XrayFallbackPort); err != nil {
			return fmt.Errorf("Xray 回落端口无效: %w", err)
		}
		if o.XrayFallbackPort == o.Port {
			return fmt.Errorf("Xray 回落端口不能与公网监听端口相同")
		}
	}
	if o.SingBoxFallbackGuard {
		if err := system.ValidatePort(o.SingBoxFallbackPort); err != nil {
			return fmt.Errorf("sing-box 回落端口无效: %w", err)
		}
		if o.SingBoxFallbackPort == o.Port {
			return fmt.Errorf("sing-box 回落端口不能与公网监听端口相同")
		}
	}
	if err := system.ValidateSNI(o.SNI); err != nil {
		return err
	}
	if o.UserName != "" {
		if err := system.ValidateUserName(o.UserName); err != nil {
			return err
		}
	}
	if o.InboundTag != "" {
		if err := system.ValidateTag(o.InboundTag); err != nil {
			return err
		}
	}
	if o.Target != "" {
		return system.ValidateTarget(o.Target)
	}
	return nil
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

func (a *App) firewallHint(port int) {
	for _, name := range []string{"ufw", "firewall-cmd"} {
		if _, err := a.Runner.Run(context.Background(), "sh", "-c", "command -v "+name); err == nil {
			fmt.Fprintf(a.Out, "[ProxyForge/提示] 检测到 %s，请确认已放行 TCP/%d；ProxyForge 不会自动修改防火墙。\n", name, port)
			return
		}
	}
}

func DefaultPort(store system.StateStore, core string) int {
	other := domain.CoreSingBox
	if core == other {
		other = domain.CoreXray
	}
	if _, err := store.Load(other); err == nil {
		return 8443
	}
	return 443
}

func PublicAddress(ctx context.Context) (string, error) {
	// Keep discovery deliberately simple and HTTPS-only; interactive users can edit it.
	req, _ := httpRequest(ctx, "https://api.ipify.org")
	client := &netHTTPClient{timeout: 5 * time.Second}
	b, err := client.do(req)
	if err != nil {
		return "", err
	}
	address := strings.TrimSpace(string(b))
	if err := system.ValidateServer(address); err != nil {
		return "", err
	}
	return address, nil
}
