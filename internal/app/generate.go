package app

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"math/big"
	"net"
	"os"
	"os/exec"
	"strings"
	"time"

	"proxyforge/internal/domain"
	"proxyforge/internal/provider"
	"proxyforge/internal/system"
)

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
	if opts.StandardConfig && (opts.SimplifiedConfig || opts.SingBoxFallbackGuard || opts.SingBoxFallbackPort != 0 || opts.SingBoxFallbackHTTPDomain || opts.SingBoxFallbackExactDomain || opts.XrayFallbackGuard || opts.XrayFallbackPort != 0 || opts.XrayFallbackHTTPDomain || opts.XrayFallbackExactDomain) {
		return domain.NodeSpec{}, fmt.Errorf("--standard-config 不能与简化或回落防偷跑配置参数同时使用")
	}
	if opts.SingBoxFallbackGuard && opts.SimplifiedConfig {
		return domain.NodeSpec{}, fmt.Errorf("sing-box 简化配置不能与回落防偷跑配置同时启用")
	}
	if (opts.SingBoxFallbackGuard || opts.SingBoxFallbackPort != 0 || opts.SingBoxFallbackHTTPDomain || opts.SingBoxFallbackExactDomain) && core != domain.CoreSingBox {
		return domain.NodeSpec{}, fmt.Errorf("sing-box 回落防偷跑配置仅支持 sing-box")
	}
	if (opts.XrayFallbackGuard || opts.XrayFallbackPort != 0 || opts.XrayFallbackHTTPDomain || opts.XrayFallbackExactDomain) && core != domain.CoreXray {
		return domain.NodeSpec{}, fmt.Errorf("REALITY 回落防偷跑配置仅支持 xray")
	}
	if !opts.StandardConfig {
		switch core {
		case domain.CoreSingBox:
			if !opts.SimplifiedConfig {
				opts.SingBoxFallbackGuard = true
			}
		case domain.CoreXray:
			opts.XrayFallbackGuard = true
		}
	}
	if opts.SingBoxFallbackGuard && opts.SingBoxFallbackPort == 0 {
		port, err := PickFallbackPort(a.Store, core, opts.Port)
		if err != nil {
			return domain.NodeSpec{}, err
		}
		opts.SingBoxFallbackPort = port
	}
	if opts.SingBoxFallbackHTTPDomain && !opts.SingBoxFallbackGuard {
		return domain.NodeSpec{}, fmt.Errorf("sing-box HTTP 回落域名限制必须与回落防偷跑配置同时启用")
	}
	if opts.SingBoxFallbackExactDomain && !opts.SingBoxFallbackGuard {
		return domain.NodeSpec{}, fmt.Errorf("sing-box 回落域名严格匹配必须与回落防偷跑配置同时启用")
	}
	if opts.XrayFallbackGuard && opts.XrayFallbackPort == 0 {
		port, err := PickFallbackPort(a.Store, core, opts.Port)
		if err != nil {
			return domain.NodeSpec{}, err
		}
		opts.XrayFallbackPort = port
	}
	if opts.XrayFallbackHTTPDomain && !opts.XrayFallbackGuard {
		return domain.NodeSpec{}, fmt.Errorf("Xray HTTP 回落域名限制必须与回落防偷跑配置同时启用")
	}
	if opts.XrayFallbackExactDomain && !opts.XrayFallbackGuard {
		return domain.NodeSpec{}, fmt.Errorf("Xray 回落域名严格匹配必须与回落防偷跑配置同时启用")
	}
	if err := validateGenerate(opts); err != nil {
		return domain.NodeSpec{}, err
	}
	if opts.Target == "" {
		opts.Target = net.JoinHostPort(opts.SNI, "443")
	}
	a.progressf("验证服务地址、端口、SNI 和 REALITY target")
	a.progressf("服务地址格式通过：%s", opts.Server)
	a.progressf("监听端口范围通过：TCP/%d", opts.Port)
	a.progressf("SNI 格式通过：%s", opts.SNI)
	warnings, err := a.Targets.Validate(ctx, opts.Target, opts.SNI, opts.Server)
	if err != nil {
		return domain.NodeSpec{}, err
	}
	for _, warning := range warnings {
		fmt.Fprintln(a.Out, "[警告] "+warning)
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
		SingBoxFallbackHTTPDomain: opts.SingBoxFallbackHTTPDomain, SingBoxFallbackExactDomain: opts.SingBoxFallbackExactDomain,
		XrayFallbackGuard: opts.XrayFallbackGuard, XrayFallbackPort: opts.XrayFallbackPort,
		XrayFallbackHTTPDomain:  opts.XrayFallbackHTTPDomain,
		XrayFallbackExactDomain: opts.XrayFallbackExactDomain,
		CoreVersion:             version, UpdatedAt: a.Now().UTC(),
	}
	if n.SimplifiedConfig {
		fmt.Fprintln(a.Out, "[警告] 已选择 sing-box 简化配置；域名将在出站连接阶段由系统 DNS 解析，域名解析到私网地址时可能绕过路由私网拦截。")
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

func (a *App) CoreVersion(ctx context.Context, core string) string {
	if a == nil || a.Registry == nil {
		return ""
	}
	p, err := a.Registry.Get(core)
	if err != nil {
		return ""
	}
	runner := a.Runner
	if logging, ok := runner.(*system.LoggingRunner); ok && logging.Runner != nil {
		runner = logging.Runner
	}
	if runner == nil {
		return ""
	}
	version, err := p.Version(ctx, runner)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(strings.SplitN(version, "\n", 2)[0])
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

func (a *App) firewallHint(port int) {
	for _, name := range []string{"ufw", "firewall-cmd"} {
		if _, err := a.lookPath(name); err == nil {
			fmt.Fprintf(a.Out, "[提示] 检测到 %s，请确认已放行 TCP/%d；ProxyForge 不会自动修改防火墙。\n", name, port)
			return
		}
	}
}

func PickFallbackPort(store system.StateStore, core string, publicPort int) (int, error) {
	if current, err := store.Load(core); err == nil {
		if port := nodeFallbackPort(current); port != 0 {
			return port, nil
		}
	}
	avoid := map[int]struct{}{}
	if publicPort != 0 {
		avoid[publicPort] = struct{}{}
	}
	other := domain.CoreSingBox
	if core == domain.CoreSingBox {
		other = domain.CoreXray
	}
	if otherState, err := store.Load(other); err == nil {
		if otherState.Port != 0 {
			avoid[otherState.Port] = struct{}{}
		}
		if port := nodeFallbackPort(otherState); port != 0 {
			avoid[port] = struct{}{}
		}
	}
	return pickFallbackPortInRange(domain.FallbackPortMin, domain.FallbackPortMax, avoid, func(port int) bool {
		return checkPortFree(port) == nil
	})
}

func pickFallbackPortInRange(min, max int, avoid map[int]struct{}, available func(int) bool) (int, error) {
	if max < min {
		return 0, fmt.Errorf("回落端口范围无效")
	}
	span := max - min + 1
	try := func(port int) bool {
		if _, used := avoid[port]; used {
			return false
		}
		return available == nil || available(port)
	}
	for i := 0; i < 32; i++ {
		n, err := rand.Int(rand.Reader, big.NewInt(int64(span)))
		if err != nil {
			return 0, fmt.Errorf("生成回落端口: %w", err)
		}
		port := min + int(n.Int64())
		if try(port) {
			return port, nil
		}
	}
	start := min
	if n, err := rand.Int(rand.Reader, big.NewInt(int64(span))); err == nil {
		start = min + int(n.Int64())
	}
	for i := 0; i < span; i++ {
		port := min + (start-min+i)%span
		if try(port) {
			return port, nil
		}
	}
	return 0, fmt.Errorf("无法在 %d-%d 之间分配可用的回落端口", min, max)
}

func DefaultPort(store system.StateStore, core string) int {
	if current, err := store.Load(core); err == nil && current.Port > 0 {
		return current.Port
	}
	other := domain.CoreSingBox
	if core == domain.CoreSingBox {
		other = domain.CoreXray
	}
	otherState, err := store.Load(other)
	if err != nil {
		return 443
	}
	if !portUsedByNode(otherState, 443) {
		return 443
	}
	if !portUsedByNode(otherState, 8443) {
		return 8443
	}
	return 443
}

func portUsedByNode(n domain.NodeSpec, port int) bool {
	return n.Port == port || nodeFallbackPort(n) == port
}
