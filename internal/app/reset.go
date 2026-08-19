package app

import (
	"context"
	"fmt"
	"net"
	"os"
	"strings"

	"proxyforge/internal/domain"
	"proxyforge/internal/system"
)

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
			fmt.Fprintln(a.Out, "[警告] "+warning)
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
	rotateCredentials := opts.Credentials || (requestedSNI == "" && requestedTarget == "")
	if !rotateCredentials {
		n.UUID, n.PrivateKey, n.PublicKey, n.ShortID = current.UUID, current.PrivateKey, current.PublicKey, current.ShortID
	}
	if rotateCredentials {
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
	}
	if rotateCredentials && (n.UUID == current.UUID || n.PrivateKey == current.PrivateKey || n.PublicKey == current.PublicKey || n.ShortID == current.ShortID) {
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
		fmt.Fprintln(a.Out, "[提示] 检测到配置包含手动修改；本次只更新受管节点字段，其他内容和外部配置属性将保留。")
	}
	a.progressf("仅更新现有配置中的受管节点字段，保留其他手动配置")
	patched, err := p.PatchServer(currentConfig, current, n, updateEndpoint)
	if err != nil {
		return n, err
	}
	return a.applyServerConfig(ctx, p, core, n, current, true, patched, managedConfig)
}
