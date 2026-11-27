package app

import (
	"context"
	"fmt"
	"net"
	"os"
	"strings"

	"proxyforge/internal/domain"
	"proxyforge/internal/provider"
	"proxyforge/internal/system"
)

// ResetCredentials atomically rotates every client credential, optionally
// changing SNI and target while preserving the node's address and port.
func (a *App) ResetCredentials(ctx context.Context, core string, opts domain.ResetOptions) (domain.NodeSpec, error) {
	a.progressf("重置节点凭据", "%s", core)
	if err := a.RootCheck(); err != nil {
		return domain.NodeSpec{}, err
	}
	p, err := a.Registry.Get(core)
	if err != nil {
		return domain.NodeSpec{}, err
	}
	unlock, err := a.lockConfigUpdate(ctx, core)
	if err != nil {
		return domain.NodeSpec{}, err
	}
	defer unlock()
	current, err := a.Store.Load(core)
	if err != nil {
		return domain.NodeSpec{}, err
	}
	if current.IsTLS() {
		return a.resetTLSNode(ctx, p, current, opts)
	}
	if opts.RotateCertificate {
		return domain.NodeSpec{}, fmt.Errorf("重新生成证书仅适用于 TLS 节点")
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
		a.progressf("验证节点修改", "新的 SNI 和 REALITY target")
		warnings, err := a.Targets.Validate(ctx, desiredTarget, desiredSNI, current.Server)
		if err != nil {
			return domain.NodeSpec{}, err
		}
		for _, warning := range warnings {
			system.WriteDetail(a.Out, "风险提示", "%s", warning)
		}
	}
	a.progressf("生成节点凭据", "%s 版本检查，UUID、REALITY 密钥和 short ID", core)
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
	if (rotateCredentials || updateEndpoint) && (len(current.LandingAccesses) > 0 || len(current.RelayLinks) > 0) {
		system.WriteDetail(a.Out, "风险提示", "%s", "SNI 或 REALITY 凭据变化后，需要重新导出受影响的中转客户端，并重新生成落地连接文本。")
	}
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
		system.WriteDetail(a.Out, "操作提示", "%s", "检测到配置包含手动修改；本次只更新受管节点字段，其他内容和外部配置属性将保留。")
	}
	a.progressf("更新受管节点", "仅修改受管字段，保留其他手动配置")
	patched, err := p.PatchServer(currentConfig, current, n, updateEndpoint)
	if err != nil {
		return n, err
	}
	return a.applyServerConfig(ctx, p, core, n, current, true, patched, managedConfig)
}

func (a *App) resetTLSNode(ctx context.Context, p provider.CoreProvider, current domain.NodeSpec, opts domain.ResetOptions) (domain.NodeSpec, error) {
	if opts.SNI != "" || opts.Target != "" {
		return current, fmt.Errorf("自签 TLS 的证书名称自动生成，请使用 --rotate-certificate 更换证书")
	}
	if err := a.validateNodeTLS(current); err != nil && !opts.RotateCertificate {
		return current, err
	}
	n := current
	version, err := p.Version(ctx, a.Runner)
	if err != nil {
		return n, err
	}
	n.CoreVersion, n.SchemaVersion, n.UpdatedAt = version, domain.StateSchemaVersion, a.Now().UTC()
	if opts.Credentials || !opts.RotateCertificate {
		n.UUID, err = system.UUID()
		if err != nil {
			return n, err
		}
		if n.UUID == current.UUID {
			return n, fmt.Errorf("UUID 生成器返回了重复值")
		}
	}
	committed := false
	if opts.RotateCertificate {
		n.TLSCertificate, err = a.generateNodeTLSCertificate(ctx, n.Core)
		if err != nil {
			return n, err
		}
		n.SNI = n.TLSCertificate.SNI
		defer func() {
			if !committed {
				_ = a.removeManagedTLSAccess(n.Core, n.TLSCertificate.Name)
			}
		}()
	}
	config, err := os.ReadFile(a.Layout.Resolve(p.ConfigPath()))
	if err != nil {
		return n, err
	}
	managed := current.ConfigSHA256 != "" && system.SHA256(config) == current.ConfigSHA256
	patched, err := p.PatchServer(config, current, n, opts.RotateCertificate)
	if err != nil {
		return n, err
	}
	applied, err := a.applyServerConfig(ctx, p, n.Core, n, current, true, patched, managed)
	if err == nil {
		committed = true
	}
	return applied, err
}
