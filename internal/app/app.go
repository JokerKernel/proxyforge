package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"proxyforge/internal/domain"
	"proxyforge/internal/install"
	"proxyforge/internal/provider"
	"proxyforge/internal/system"
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
	Now       func() time.Time
	RootCheck func() error
	PortFree  func(int) error
	Listening func(context.Context, int, time.Duration) error
}

func New(reg *provider.Registry, runner provider.Runner, layout system.Layout, out io.Writer) *App {
	return &App{
		Registry: reg, Runner: runner, Layout: layout, Store: system.StateStore{Layout: layout},
		Services: system.ServiceManager{Runner: runner}, Installer: install.Installer{Runner: runner, Layout: layout, Output: out},
		Targets: NetworkTargetValidator{}, Out: out, Now: time.Now,
		RootCheck: RequireRoot, PortFree: checkPortFree, Listening: waitListening,
	}
}

func RequireRoot() error {
	if os.Geteuid() != 0 {
		return fmt.Errorf("此操作必须以 root 运行")
	}
	return nil
}

func (a *App) Install(ctx context.Context, core string, opts install.Options) error {
	if err := a.RootCheck(); err != nil {
		return err
	}
	if err := system.CheckPlatform(a.Layout); err != nil {
		return err
	}
	p, err := a.Registry.Get(core)
	if err != nil {
		return err
	}
	config := a.Layout.Resolve(p.ConfigPath())
	if _, err := system.BackupFile(config, a.Layout.BackupRoot(core), a.Now()); err != nil {
		return err
	}
	if _, err := a.Installer.Run(ctx, p, opts); err != nil {
		return err
	}
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
	probe := domain.NodeSpec{Core: core, Server: "127.0.0.1", Port: 443, SNI: "example.com", Target: "example.com:443", UUID: uuid, PrivateKey: keys.Private, PublicKey: keys.Public, ShortID: shortID, CoreVersion: version}
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
	status, statusErr := a.Services.IsActive(ctx, p.ServiceName())
	running, err := installedServiceRunning(status, statusErr)
	if err != nil {
		return fmt.Errorf("安装完成但服务状态异常（%s）: %w", status.Detail, err)
	}
	if running {
		fmt.Fprintf(a.Out, "%s 已安装并运行：%s\n", core, version)
		return nil
	}
	fmt.Fprintf(a.Out, "%s 已安装：%s\n", core, version)
	fmt.Fprintln(a.Out, "服务当前为 inactive；这是尚未生成服务端配置时的正常状态。请继续选择“生成服务端配置”，配置成功后服务会自动启动。")
	return nil
}

func (a *App) Uninstall(ctx context.Context, core string, opts install.Options) error {
	if err := a.RootCheck(); err != nil {
		return err
	}
	if err := system.CheckPlatform(a.Layout); err != nil {
		return err
	}
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
		if _, err := system.BackupFile(configPath, a.Layout.BackupRoot(core), a.Now()); err != nil {
			return err
		}
	}

	if err := a.Installer.Uninstall(ctx, p, opts); err != nil {
		return err
	}
	if managedConfig {
		if err := os.Remove(configPath); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("内核已卸载，但删除受管配置失败: %w", err)
		}
	} else if hadConfig {
		fmt.Fprintf(a.Out, "检测到非受管或已被外部修改的配置，已保留：%s\n", p.ConfigPath())
	}
	if err := a.Store.Delete(core); err != nil {
		return fmt.Errorf("内核已卸载，但删除 ProxyForge 状态失败: %w", err)
	}
	fmt.Fprintf(a.Out, "%s 已卸载；历史备份和安装脚本信任记录已保留。\n", core)
	return nil
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
	other := domain.CoreSingBox
	if core == other {
		other = domain.CoreXray
	}
	if otherState, e := a.Store.Load(other); e == nil && otherState.Port == opts.Port {
		return domain.NodeSpec{}, fmt.Errorf("端口 %d 已由受管的 %s 节点使用", opts.Port, other)
	}
	if !hasOld || old.Port != opts.Port {
		if err := a.PortFree(opts.Port); err != nil {
			return domain.NodeSpec{}, err
		}
	}
	version, err := p.Version(ctx, a.Runner)
	if err != nil {
		return domain.NodeSpec{}, fmt.Errorf("内核不可用或不支持所需能力: %w", err)
	}
	n := domain.NodeSpec{ManagedBy: "proxyforge", Core: core, Server: opts.Server, Port: opts.Port, SNI: opts.SNI, Target: opts.Target, CoreVersion: version, UpdatedAt: a.Now().UTC()}
	if hasOld && !opts.RotateCredentials {
		n.UUID, n.PrivateKey, n.PublicKey, n.ShortID = old.UUID, old.PrivateKey, old.PublicKey, old.ShortID
	} else {
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
	managedConfig := hasOld && hadConfig && old.ConfigSHA256 != "" && system.SHA256(oldConfig) == old.ConfigSHA256
	if hadConfig && !managedConfig && !opts.TakeOver {
		return n, fmt.Errorf("发现非 ProxyForge 管理或已被外部修改的配置 %s；确认后使用 --take-over", p.ConfigPath())
	}
	if err := validateRendered(ctx, p, a.Runner, configPath, config); err != nil {
		return n, err
	}
	if hadConfig {
		if _, err := system.BackupFile(configPath, a.Layout.BackupRoot(core), a.Now()); err != nil {
			return n, err
		}
	}
	if err := system.AtomicWrite(configPath, config, 0600); err != nil {
		return n, err
	}
	serviceUser := a.Services.User(ctx, p.ServiceName())
	if err := secureConfigForUser(configPath, serviceUser); err != nil {
		_ = restoreFile(configPath, oldConfig, hadConfig, oldMetadata)
		return n, err
	}
	rollback := func(cause error) error {
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
	if err := a.Services.Restart(ctx, p.ServiceName()); err != nil {
		return n, rollback(fmt.Errorf("重启 %s 失败: %w", p.ServiceName(), err))
	}
	status, err := a.Services.IsActive(ctx, p.ServiceName())
	if err != nil || !status.Active {
		return n, rollback(fmt.Errorf("%s 未进入 active 状态: %s: %w", p.ServiceName(), status.Detail, err))
	}
	if err := a.Listening(ctx, opts.Port, 4*time.Second); err != nil {
		return n, rollback(err)
	}
	if err := a.Store.Save(n); err != nil {
		return n, rollback(fmt.Errorf("保存状态失败: %w", err))
	}
	a.firewallHint(opts.Port)
	return n, nil
}

// ResetCredentials atomically rotates every client credential, optionally
// changing SNI and target while preserving the node's address and port.
func (a *App) ResetCredentials(ctx context.Context, core string, opts domain.ResetOptions) (domain.NodeSpec, error) {
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
		RotateCredentials: true,
		NonInteractive:    true,
	})
}

func (a *App) Client(ctx context.Context, core, output string, force bool) ([]byte, error) {
	if err := a.RootCheck(); err != nil {
		return nil, err
	}
	p, err := a.Registry.Get(core)
	if err != nil {
		return nil, err
	}
	n, err := a.Store.Load(core)
	if err != nil {
		return nil, err
	}
	b, err := p.RenderClient(n)
	if err != nil {
		return nil, err
	}
	tmp, err := os.CreateTemp("", "proxyforge-client-*.json")
	if err != nil {
		return nil, err
	}
	path := tmp.Name()
	defer os.Remove(path)
	if err := tmp.Chmod(0600); err == nil {
		_, err = tmp.Write(b)
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return nil, err
	}
	if err := p.ValidateConfig(ctx, a.Runner, path); err != nil {
		return nil, err
	}
	if output == "" {
		return b, nil
	}
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

func (a *App) Service(ctx context.Context, core, action string) ([]byte, error) {
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
