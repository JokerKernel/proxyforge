package app

import (
	"context"
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
	PortFree  func(int) error
	Listening func(context.Context, int, time.Duration) error
}

func New(reg *provider.Registry, runner provider.Runner, layout system.Layout, out io.Writer) *App {
	return &App{
		Registry: reg, Runner: runner, Layout: layout, Store: system.StateStore{Layout: layout},
		Services: system.ServiceManager{Runner: runner}, Installer: install.Installer{Runner: runner, Layout: layout, Output: out},
		Targets: NetworkTargetValidator{}, Out: out, Progress: out, Now: time.Now,
		RootCheck: RequireRoot, PortFree: checkPortFree, Listening: waitListening,
	}
}

func (a *App) progressf(format string, args ...any) {
	if a.Progress == nil {
		return
	}
	fmt.Fprintf(a.Progress, "[步骤] "+format+"\n", args...)
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
	if _, lookErr := exec.LookPath(p.Binary()); lookErr == nil {
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
	fmt.Fprintln(a.Out, "服务当前为 inactive；这是尚未生成服务端配置时的正常状态。请继续选择“生成服务端配置”，配置成功后服务会自动启动。")
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
	fmt.Fprintf(w, "  %s %s成功\n", core, action)
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
	a.progressf("检查现有配置和 ProxyForge 管理状态")
	p, err := a.Registry.Get(core)
	if err != nil {
		return err
	}

	configPath := a.Layout.Resolve(p.ConfigPath())
	config, readErr := os.ReadFile(configPath)
	hadConfig := readErr == nil
	if readErr != nil && !os.IsNotExist(readErr) {
		return readErr
	}
	state, stateErr := a.Store.Load(core)
	hasManagedState := stateErr == nil
	if stateErr != nil && !errors.Is(stateErr, system.ErrNoState) {
		fmt.Fprintf(a.Out, "警告：无法验证现有 ProxyForge 状态（%v）；活动配置将保留。\n", stateErr)
	}
	managedConfig := hasManagedState && hadConfig && state.ConfigSHA256 != "" && system.SHA256(config) == state.ConfigSHA256
	if hadConfig {
		a.progressf("卸载前备份配置 %s", configPath)
		if backup, err := system.BackupFile(configPath, a.Layout.BackupRoot(core), a.Now()); err != nil {
			return err
		} else if backup != "" {
			a.progressf("配置已备份到 %s", backup)
		}
	}

	a.progressf("停止服务并卸载内核和 systemd unit")
	if err := a.Installer.Uninstall(ctx, p, opts); err != nil {
		return err
	}
	if managedConfig {
		a.progressf("删除受管活动配置 %s", configPath)
		if err := os.Remove(configPath); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("内核已卸载，但删除受管配置失败: %w", err)
		}
	} else if hadConfig {
		fmt.Fprintf(a.Out, "检测到非受管或已被外部修改的配置，已保留：%s\n", p.ConfigPath())
	}
	a.progressf("删除 %s 的 ProxyForge 状态", core)
	if err := a.Store.Delete(core); err != nil {
		return fmt.Errorf("内核已卸载，但删除 ProxyForge 状态失败: %w", err)
	}
	fmt.Fprintf(a.Out, "%s 已卸载；历史备份和安装脚本信任记录已保留。\n", core)
	return nil
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
		if version, err := p.Version(ctx, a.Runner); err == nil {
			return fmt.Errorf("仍检测到已安装的 %s（%s）；请先执行 uninstall", p.Name(), version)
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
	}
	if len(cleanupErrors) != 0 {
		return fmt.Errorf("清理未完全完成: %w", errors.Join(cleanupErrors...))
	}
	fmt.Fprintf(a.Out, "%s 的卸载残留已清理。\n", target)
	return nil
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
		fmt.Fprintln(a.Out, "警告："+warning)
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
	other := domain.CoreSingBox
	if core == other {
		other = domain.CoreXray
	}
	if otherState, e := a.Store.Load(other); e == nil && otherState.Port == opts.Port {
		return domain.NodeSpec{}, fmt.Errorf("端口 %d 已由受管的 %s 节点使用", opts.Port, other)
	}
	a.progressf("检测 %s 版本和配置能力", core)
	version, err := p.Version(ctx, a.Runner)
	if err != nil {
		return domain.NodeSpec{}, fmt.Errorf("内核不可用或不支持所需能力: %w", err)
	}
	n := domain.NodeSpec{ManagedBy: "proxyforge", Core: core, InboundTag: inboundTag, Server: opts.Server, Port: opts.Port, SNI: opts.SNI, Target: opts.Target, UserName: userName, CoreVersion: version, UpdatedAt: a.Now().UTC()}
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
	n.ConfigSHA256 = system.SHA256(config)
	configPath := a.Layout.Resolve(p.ConfigPath())
	oldConfig, readErr := os.ReadFile(configPath)
	hadConfig := readErr == nil
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
	if !hasOld || old.Port != opts.Port {
		status, _ := a.Services.IsActive(ctx, p.ServiceName())
		if status.Active {
			a.progressf("临时停止 systemd 服务 %s 以检查监听端口", p.ServiceName())
			if _, err := a.Services.Action(ctx, p.ServiceName(), "stop"); err != nil {
				return n, fmt.Errorf("停止 %s 失败: %w", p.ServiceName(), err)
			}
			serviceStopped = true
		}
		a.progressf("检查监听端口 %d 是否可用", opts.Port)
		if err := a.PortFree(opts.Port); err != nil {
			return n, restoreStoppedService(err)
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
	a.progressf("确认服务 active 并监听端口 %d", opts.Port)
	status, err := a.Services.IsActive(ctx, p.ServiceName())
	if err != nil || !status.Active {
		return n, rollback(fmt.Errorf("%s 未进入 active 状态: %s: %w", p.ServiceName(), status.Detail, err))
	}
	if err := a.Listening(ctx, opts.Port, 4*time.Second); err != nil {
		return n, rollback(err)
	}
	a.progressf("保存 ProxyForge 节点状态")
	if err := a.Store.Save(n); err != nil {
		return n, rollback(fmt.Errorf("保存状态失败: %w", err))
	}
	a.firewallHint(opts.Port)
	return n, nil
}

// ResetCredentials atomically rotates every client credential, optionally
// changing SNI and target while preserving the node's address and port.
func (a *App) ResetCredentials(ctx context.Context, core string, opts domain.ResetOptions) (domain.NodeSpec, error) {
	a.progressf("开始重置 %s 节点凭据", core)
	if err := a.RootCheck(); err != nil {
		return domain.NodeSpec{}, err
	}
	current, err := a.Store.Load(core)
	if err != nil {
		return domain.NodeSpec{}, err
	}
	desiredSNI := strings.TrimSpace(opts.SNI)
	if desiredSNI == "" {
		desiredSNI = current.SNI
	}
	desiredTarget := strings.TrimSpace(opts.Target)
	if desiredTarget == "" {
		desiredTarget = current.Target
		if opts.SNI != "" && desiredSNI != current.SNI {
			desiredTarget = net.JoinHostPort(desiredSNI, "443")
		}
	}
	return a.Generate(ctx, core, domain.GenerateOptions{
		Server:            current.Server,
		Port:              current.Port,
		SNI:               desiredSNI,
		Target:            desiredTarget,
		UserName:          current.UserName,
		InboundTag:        current.InboundTag,
		RotateCredentials: true,
		NonInteractive:    true,
	})
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
	return true, nil
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
			fmt.Fprintf(a.Out, "提示：检测到 %s，请确认已放行 TCP/%d；ProxyForge 不会自动修改防火墙。\n", name, port)
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
