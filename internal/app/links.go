package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"regexp"
	"strings"
	"time"

	"proxyforge/internal/domain"
	"proxyforge/internal/provider/mihomo"
	"proxyforge/internal/system"
)

const landingBundleKind = "proxyforge-landing"

type LandingBundle struct {
	ManagedBy     string             `json:"managed_by"`
	SchemaVersion int                `json:"schema_version"`
	Kind          string             `json:"kind"`
	Peer          domain.LandingPeer `json:"peer"`
}

type RelayAddOptions struct {
	AllowUnreachable bool
}

type LandingAddOptions struct {
	Security string
}

var linkNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,31}$`)
var uuidPattern = regexp.MustCompile(`(?i)^[0-9a-f]{8}-[0-9a-f]{4}-[1-5][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
var shortIDPattern = regexp.MustCompile(`(?i)^[0-9a-f]{2,16}$`)
var relayTestLAN = mustCIDR("192.168.0.0/16")
var reservedLinkNames = map[string]struct{}{
	"api": {}, "blocked-private": {}, "bootstrap": {}, "cloudflare": {}, "cloudflare-doh": {},
	"direct": {}, "dns-in": {}, "dns-out": {}, "dokodemo-in": {}, "fallback-direct": {},
	"google": {}, "google-doh": {}, "local": {}, "mixed-in": {}, "proxy": {},
	"singbox-fallback-in": {},
}

func (a *App) LandingAccesses(core string) ([]domain.LandingAccess, error) {
	n, err := a.Store.Load(core)
	if err != nil {
		return nil, err
	}
	return append([]domain.LandingAccess(nil), n.LandingAccesses...), nil
}

func (a *App) RelayLinks(core string) ([]domain.RelayLink, error) {
	n, err := a.Store.Load(core)
	if err != nil {
		return nil, err
	}
	return append([]domain.RelayLink(nil), n.RelayLinks...), nil
}

func (a *App) AddLandingAccess(ctx context.Context, core, name string) (domain.LandingAccess, error) {
	return a.AddLandingAccessWithOptions(ctx, core, name, LandingAddOptions{Security: domain.LandingSecurityReality})
}

func (a *App) AddLandingAccessWithOptions(ctx context.Context, core, name string, opts LandingAddOptions) (domain.LandingAccess, error) {
	if err := validateLinkName(name); err != nil {
		return domain.LandingAccess{}, err
	}
	n, err := a.loadLinkNode(core)
	if err != nil {
		return domain.LandingAccess{}, err
	}
	if _, ok := findLanding(n, name); ok {
		return domain.LandingAccess{}, fmt.Errorf("落地接入 %q 已存在", name)
	}
	if err := validateAvailableLinkName(n, name); err != nil {
		return domain.LandingAccess{}, err
	}
	security := domain.NormalizeLandingSecurity(strings.ToLower(strings.TrimSpace(opts.Security)))
	if security != domain.LandingSecurityReality && security != domain.LandingSecurityTLS {
		return domain.LandingAccess{}, fmt.Errorf("落地安全协议无效: %q（可选 reality 或 tls）", opts.Security)
	}
	port := 0
	if security == domain.LandingSecurityTLS {
		port, err = a.PickLandingTLSPort(core)
		if err != nil {
			return domain.LandingAccess{}, err
		}
		if err := a.validateLandingTLSPort(n, core, port); err != nil {
			return domain.LandingAccess{}, err
		}
	}
	uuid, err := system.UUID()
	if err != nil {
		return domain.LandingAccess{}, err
	}
	access := domain.LandingAccess{
		Name: name, UserName: name, UUID: uuid, Security: security,
		Port:    port,
		Enabled: true, UpdatedAt: a.Now().UTC(),
	}
	generatedTLS := false
	if security == domain.LandingSecurityTLS {
		p, getErr := a.Registry.Get(core)
		if getErr != nil {
			return domain.LandingAccess{}, getErr
		}
		certificate, generateErr := a.generateManagedTLSCertificate(core, name, a.Services.User(ctx, p.ServiceName()))
		if generateErr != nil {
			return domain.LandingAccess{}, generateErr
		}
		access.SNI = certificate.SNI
		access.CertificateFile = certificate.CertificateFile
		access.KeyFile = certificate.KeyFile
		access.CertificateSHA256 = certificate.CertificateSHA256
		access.CertificatePublicKeySHA256 = certificate.CertificatePublicKeySHA256
		generatedTLS = true
	}
	if security == domain.LandingSecurityReality {
		access.Port, access.SNI, access.CertificateFile, access.KeyFile = 0, "", "", ""
	}
	n.LandingAccesses = append(n.LandingAccesses, access)
	if _, err := a.applyLinks(ctx, core, n, "落地接入"); err != nil {
		if generatedTLS {
			if cleanupErr := a.removeManagedTLSAccess(core, name); cleanupErr != nil {
				return domain.LandingAccess{}, fmt.Errorf("%v；且清理新生成的 TLS 证书失败: %w", err, cleanupErr)
			}
		}
		return domain.LandingAccess{}, err
	}
	if security == domain.LandingSecurityTLS {
		a.firewallHint(access.Port)
	}
	return access, nil
}

// PickLandingTLSPort returns a random, currently available high port while
// avoiding all ports managed by ProxyForge for both cores.
func (a *App) PickLandingTLSPort(core string) (int, error) {
	if core != domain.CoreXray && core != domain.CoreSingBox {
		return 0, fmt.Errorf("不支持的内核 %q", core)
	}
	avoid := map[int]struct{}{}
	for _, candidateCore := range []string{domain.CoreXray, domain.CoreSingBox} {
		n, err := a.Store.Load(candidateCore)
		if err != nil {
			continue
		}
		if n.Port != 0 {
			avoid[n.Port] = struct{}{}
		}
		if port := nodeFallbackPort(n); port != 0 {
			avoid[port] = struct{}{}
		}
		for _, access := range n.LandingAccesses {
			if domain.NormalizeLandingSecurity(access.Security) == domain.LandingSecurityTLS && access.Port != 0 {
				avoid[access.Port] = struct{}{}
			}
		}
	}
	return pickAvailablePortInRange("TLS 落地", domain.LandingTLSPortMin, domain.LandingTLSPortMax, avoid, func(port int) bool {
		return a.PortFree == nil || a.PortFree(port) == nil
	})
}

func (a *App) SetLandingAccessEnabled(ctx context.Context, core, name string, enabled bool) error {
	n, err := a.loadLinkNode(core)
	if err != nil {
		return err
	}
	index, ok := findLanding(n, name)
	if !ok {
		return fmt.Errorf("找不到落地接入 %q", name)
	}
	n.LandingAccesses[index].Enabled = enabled
	n.LandingAccesses[index].UpdatedAt = a.Now().UTC()
	_, err = a.applyLinks(ctx, core, n, "落地接入")
	return err
}

func (a *App) RotateLandingAccess(ctx context.Context, core, name string) (domain.LandingAccess, error) {
	n, err := a.loadLinkNode(core)
	if err != nil {
		return domain.LandingAccess{}, err
	}
	index, ok := findLanding(n, name)
	if !ok {
		return domain.LandingAccess{}, fmt.Errorf("找不到落地接入 %q", name)
	}
	uuid, err := system.UUID()
	if err != nil {
		return domain.LandingAccess{}, err
	}
	n.LandingAccesses[index].UUID = uuid
	n.LandingAccesses[index].UpdatedAt = a.Now().UTC()
	if _, err = a.applyLinks(ctx, core, n, "落地接入凭据"); err != nil {
		return domain.LandingAccess{}, err
	}
	return n.LandingAccesses[index], nil
}

func (a *App) RemoveLandingAccess(ctx context.Context, core, name string) error {
	n, err := a.loadLinkNode(core)
	if err != nil {
		return err
	}
	index, ok := findLanding(n, name)
	if !ok {
		return fmt.Errorf("找不到落地接入 %q", name)
	}
	removed := n.LandingAccesses[index]
	n.LandingAccesses = append(n.LandingAccesses[:index], n.LandingAccesses[index+1:]...)
	if _, err = a.applyLinks(ctx, core, n, "落地接入"); err != nil {
		return err
	}
	if a.isManagedTLSAccess(core, removed) {
		if err := a.removeManagedTLSAccess(core, name); err != nil {
			return fmt.Errorf("落地接入已删除，但清理受管 TLS 证书失败: %w", err)
		}
	}
	return nil
}

func (a *App) LandingBundle(core, name string) (LandingBundle, error) {
	n, err := a.loadLinkNode(core)
	if err != nil {
		return LandingBundle{}, err
	}
	index, ok := findLanding(n, name)
	if !ok {
		return LandingBundle{}, fmt.Errorf("找不到落地接入 %q", name)
	}
	access := n.LandingAccesses[index]
	if !access.Enabled {
		return LandingBundle{}, fmt.Errorf("落地接入 %q 已停用，不能生成连接文本", name)
	}
	security := domain.NormalizeLandingSecurity(access.Security)
	peer := domain.LandingPeer{Name: access.Name, Core: core, Security: security, Server: n.Server, UUID: access.UUID, Flow: domain.VisionFlow}
	if security == domain.LandingSecurityTLS {
		if !domain.ValidCertificateSHA256(access.CertificateSHA256) || !domain.ValidCertificatePublicKeySHA256(access.CertificatePublicKeySHA256) {
			return LandingBundle{}, fmt.Errorf("TLS 落地接入 %q 缺少合法证书指纹；请删除并重新创建该接入", name)
		}
		now := time.Now()
		if a.Now != nil {
			now = a.Now()
		}
		if err := validateManagedTLSCertificate(access, now); err != nil {
			return LandingBundle{}, fmt.Errorf("TLS 落地接入 %q 无法导出: %w", name, err)
		}
		peer.Port, peer.SNI = access.Port, access.SNI
		peer.CertificateSHA256 = access.CertificateSHA256
		peer.CertificatePublicKeySHA256 = access.CertificatePublicKeySHA256
	} else {
		peer.Port, peer.SNI, peer.PublicKey, peer.ShortID = n.Port, n.SNI, n.PublicKey, n.ShortID
	}
	return LandingBundle{ManagedBy: "proxyforge", SchemaVersion: 3, Kind: landingBundleKind, Peer: peer}, nil
}

func (a *App) ExportLandingBundle(core, name, output string, force bool) ([]byte, error) {
	bundle, err := a.LandingBundle(core, name)
	if err != nil {
		return nil, err
	}
	b, err := json.MarshalIndent(bundle, "", "  ")
	if err != nil {
		return nil, err
	}
	b = append(b, '\n')
	if output != "" {
		err = writePrivateFile(output, b, force)
	}
	return b, err
}

func ParseLandingBundle(b []byte) (domain.LandingPeer, error) {
	var bundle LandingBundle
	if err := json.Unmarshal(b, &bundle); err != nil {
		return domain.LandingPeer{}, fmt.Errorf("解析落地连接文本: %w", err)
	}
	if bundle.ManagedBy != "proxyforge" || bundle.Kind != landingBundleKind || bundle.SchemaVersion < 1 || bundle.SchemaVersion > 3 {
		return domain.LandingPeer{}, fmt.Errorf("落地连接文本标识或版本无效")
	}
	bundle.Peer.Security = domain.NormalizeLandingSecurity(strings.ToLower(strings.TrimSpace(bundle.Peer.Security)))
	if err := validateLandingPeer(bundle.Peer); err != nil {
		return domain.LandingPeer{}, err
	}
	return bundle.Peer, nil
}

func ReadLandingBundle(path string) (domain.LandingPeer, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return domain.LandingPeer{}, fmt.Errorf("读取落地连接文件: %w", err)
	}
	return ParseLandingBundle(b)
}

func (a *App) AddRelayLink(ctx context.Context, core, name string, peer domain.LandingPeer, opts RelayAddOptions) (domain.RelayLink, error) {
	peer.Security = domain.NormalizeLandingSecurity(strings.ToLower(strings.TrimSpace(peer.Security)))
	if err := validateLinkName(name); err != nil {
		return domain.RelayLink{}, err
	}
	if err := validateLandingPeer(peer); err != nil {
		return domain.RelayLink{}, err
	}
	n, err := a.loadLinkNode(core)
	if err != nil {
		return domain.RelayLink{}, err
	}
	if err := validateAvailableLinkName(n, name); err != nil {
		return domain.RelayLink{}, err
	}
	userName := name
	if strings.EqualFold(peer.Server, n.Server) && peer.Port == n.Port {
		return domain.RelayLink{}, fmt.Errorf("落地端点不能指向本机当前监听地址和端口")
	}
	if err := a.probeRelayTCP(ctx, peer.Server, peer.Port); err != nil {
		if !opts.AllowUnreachable {
			return domain.RelayLink{}, fmt.Errorf("落地端点不可达: %w（确认仍要配置时使用 --allow-unreachable）", err)
		}
		fmt.Fprintln(a.Out, "[警告] 落地端点当前不可达，已按要求继续配置："+err.Error())
	}
	uuid, err := system.UUID()
	if err != nil {
		return domain.RelayLink{}, err
	}
	link := domain.RelayLink{Name: name, UserName: userName, UUID: uuid, Enabled: true, Upstream: peer, UpdatedAt: a.Now().UTC()}
	n.RelayLinks = append(n.RelayLinks, link)
	if _, err := a.applyLinks(ctx, core, n, "中转线路"); err != nil {
		return domain.RelayLink{}, err
	}
	return link, nil
}

func (a *App) UpdateRelayLink(ctx context.Context, core, name string, peer domain.LandingPeer, opts RelayAddOptions) error {
	peer.Security = domain.NormalizeLandingSecurity(strings.ToLower(strings.TrimSpace(peer.Security)))
	if err := validateLandingPeer(peer); err != nil {
		return err
	}
	n, err := a.loadLinkNode(core)
	if err != nil {
		return err
	}
	index, ok := findRelay(n, name)
	if !ok {
		return fmt.Errorf("找不到中转线路 %q", name)
	}
	if strings.EqualFold(peer.Server, n.Server) && peer.Port == n.Port {
		return fmt.Errorf("落地端点不能指向本机当前监听地址和端口")
	}
	if err := a.probeRelayTCP(ctx, peer.Server, peer.Port); err != nil {
		if !opts.AllowUnreachable {
			return fmt.Errorf("落地端点不可达: %w（确认仍要配置时使用 --allow-unreachable）", err)
		}
		fmt.Fprintln(a.Out, "[警告] 落地端点当前不可达，已按要求继续配置："+err.Error())
	}
	n.RelayLinks[index].Upstream = peer
	n.RelayLinks[index].UpdatedAt = a.Now().UTC()
	_, err = a.applyLinks(ctx, core, n, "中转线路")
	return err
}

func (a *App) SetRelayLinkEnabled(ctx context.Context, core, name string, enabled bool) error {
	n, err := a.loadLinkNode(core)
	if err != nil {
		return err
	}
	index, ok := findRelay(n, name)
	if !ok {
		return fmt.Errorf("找不到中转线路 %q", name)
	}
	n.RelayLinks[index].Enabled = enabled
	n.RelayLinks[index].UpdatedAt = a.Now().UTC()
	_, err = a.applyLinks(ctx, core, n, "中转线路")
	return err
}

func (a *App) RotateRelayLink(ctx context.Context, core, name string) (domain.RelayLink, error) {
	n, err := a.loadLinkNode(core)
	if err != nil {
		return domain.RelayLink{}, err
	}
	index, ok := findRelay(n, name)
	if !ok {
		return domain.RelayLink{}, fmt.Errorf("找不到中转线路 %q", name)
	}
	uuid, err := system.UUID()
	if err != nil {
		return domain.RelayLink{}, err
	}
	n.RelayLinks[index].UUID = uuid
	n.RelayLinks[index].UpdatedAt = a.Now().UTC()
	if _, err = a.applyLinks(ctx, core, n, "中转线路凭据"); err != nil {
		return domain.RelayLink{}, err
	}
	return n.RelayLinks[index], nil
}

func (a *App) RemoveRelayLink(ctx context.Context, core, name string) error {
	n, err := a.loadLinkNode(core)
	if err != nil {
		return err
	}
	index, ok := findRelay(n, name)
	if !ok {
		return fmt.Errorf("找不到中转线路 %q", name)
	}
	n.RelayLinks = append(n.RelayLinks[:index], n.RelayLinks[index+1:]...)
	_, err = a.applyLinks(ctx, core, n, "中转线路")
	return err
}

func (a *App) TestRelayLink(ctx context.Context, core, name string) error {
	n, err := a.loadLinkNode(core)
	if err != nil {
		return err
	}
	index, ok := findRelay(n, name)
	if !ok {
		return fmt.Errorf("找不到中转线路 %q", name)
	}
	peer := n.RelayLinks[index].Upstream
	return a.probeRelayTCP(ctx, peer.Server, peer.Port)
}

func (a *App) RelayClientConfig(ctx context.Context, core, name, format, output string, force bool) ([]byte, error) {
	n, err := a.loadLinkNode(core)
	if err != nil {
		return nil, err
	}
	index, ok := findRelay(n, name)
	if !ok {
		return nil, fmt.Errorf("找不到中转线路 %q", name)
	}
	link := n.RelayLinks[index]
	if !link.Enabled {
		return nil, fmt.Errorf("中转线路 %q 已停用，不能导出客户端配置", name)
	}
	clientNode := n
	clientNode.UUID, clientNode.UserName = link.UUID, link.UserName
	var b []byte
	format = strings.ToLower(strings.TrimSpace(format))
	if format == "mihomo" || format == "clash-meta" {
		format = ClientFormatClash
	}
	if format == ClientFormatClash {
		b, err = mihomo.RenderClient(clientNode)
	} else if format == "" || format == ClientFormatNative {
		p, getErr := a.Registry.Get(core)
		if getErr != nil {
			return nil, getErr
		}
		b, err = p.RenderClient(clientNode)
		if err == nil {
			err = validateTemporary(ctx, p, a.Runner, b)
		}
	} else {
		return nil, fmt.Errorf("不支持的客户端格式 %q（可选 native 或 clash）", format)
	}
	if err != nil {
		return nil, err
	}
	if output != "" {
		err = writePrivateFile(output, b, force)
	}
	return b, err
}

func (a *App) loadLinkNode(core string) (domain.NodeSpec, error) {
	if err := a.RootCheck(); err != nil {
		return domain.NodeSpec{}, err
	}
	n, err := a.Store.Load(core)
	if err != nil {
		return domain.NodeSpec{}, err
	}
	if n.InboundTag == "" || n.UUID == "" {
		return domain.NodeSpec{}, fmt.Errorf("当前 %s 状态不完整，请先生成受管服务端配置", core)
	}
	return n, nil
}

func (a *App) applyLinks(ctx context.Context, core string, next domain.NodeSpec, label string) (domain.NodeSpec, error) {
	p, err := a.Registry.Get(core)
	if err != nil {
		return domain.NodeSpec{}, err
	}
	old, err := a.Store.Load(core)
	if err != nil {
		return domain.NodeSpec{}, err
	}
	path := a.Layout.Resolve(p.ConfigPath())
	currentConfig, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return domain.NodeSpec{}, fmt.Errorf("尚未找到 %s 服务端配置", core)
	}
	if err != nil {
		return domain.NodeSpec{}, err
	}
	managed := old.ConfigSHA256 != "" && old.ConfigSHA256 == system.SHA256(currentConfig)
	if !managed {
		fmt.Fprintln(a.Out, "[提示] 检测到手动修改；仅更新 ProxyForge 管理的线路组件并保留其他字段。")
	}
	a.progressf("更新%s，保留现有监听端口和普通用户路由", label)
	patched, err := p.PatchLinks(currentConfig, old, next)
	if err != nil {
		return domain.NodeSpec{}, err
	}
	next.SchemaVersion = domain.StateSchemaVersion
	next.UpdatedAt = a.Now().UTC()
	return a.applyServerConfig(ctx, p, core, next, old, true, patched, managed)
}

func findLanding(n domain.NodeSpec, name string) (int, bool) {
	for i := range n.LandingAccesses {
		if n.LandingAccesses[i].Name == name {
			return i, true
		}
	}
	return -1, false
}

func findRelay(n domain.NodeSpec, name string) (int, bool) {
	for i := range n.RelayLinks {
		if n.RelayLinks[i].Name == name {
			return i, true
		}
	}
	return -1, false
}

func validateLinkName(name string) error {
	if !linkNamePattern.MatchString(name) {
		return fmt.Errorf("线路名称必须为 1-32 个字母、数字、下划线或连字符，并以字母或数字开头")
	}
	return nil
}

func (a *App) ValidateLinkNameAvailable(core, name string) error {
	if err := validateLinkName(name); err != nil {
		return err
	}
	n, err := a.loadLinkNode(core)
	if err != nil {
		return err
	}
	return validateAvailableLinkName(n, name)
}

func validateAvailableLinkName(n domain.NodeSpec, name string) error {
	normalized := strings.ToLower(strings.TrimSpace(name))
	if _, reserved := reservedLinkNames[normalized]; reserved {
		return fmt.Errorf("名称 %q 是系统保留名称，请使用其他名称", name)
	}
	for label, value := range map[string]string{"普通用户": n.UserName, "主入站 tag": n.InboundTag} {
		if value != "" && strings.EqualFold(name, value) {
			return fmt.Errorf("名称 %q 已由%s使用，请使用其他名称", name, label)
		}
	}
	for _, access := range n.LandingAccesses {
		if strings.EqualFold(name, access.Name) || strings.EqualFold(name, access.UserName) {
			return fmt.Errorf("名称 %q 已由落地接入 %q 使用，请使用其他名称", name, access.Name)
		}
	}
	for _, link := range n.RelayLinks {
		if strings.EqualFold(name, link.Name) || strings.EqualFold(name, link.UserName) {
			return fmt.Errorf("名称 %q 已由中转线路 %q 使用，请使用其他名称", name, link.Name)
		}
	}
	return nil
}

func (a *App) validateLandingTLSPort(n domain.NodeSpec, core string, port int) error {
	if port == 0 {
		return nil
	}
	if err := system.ValidatePort(port); err != nil {
		return fmt.Errorf("TLS 落地端口无效: %w", err)
	}
	if port == n.Port || port == nodeFallbackPort(n) {
		return fmt.Errorf("TLS 落地端口 %d 与当前节点已有端口冲突", port)
	}
	for _, access := range n.LandingAccesses {
		if domain.NormalizeLandingSecurity(access.Security) == domain.LandingSecurityTLS && access.Port == port {
			return fmt.Errorf("TLS 落地端口 %d 已由接入 %q 使用", port, access.Name)
		}
	}
	otherCore := domain.CoreXray
	if core == domain.CoreXray {
		otherCore = domain.CoreSingBox
	}
	if other, err := a.Store.Load(otherCore); err == nil {
		if port == other.Port || port == nodeFallbackPort(other) {
			return fmt.Errorf("TLS 落地端口 %d 已由受管的 %s 节点使用", port, otherCore)
		}
		for _, access := range other.LandingAccesses {
			if domain.NormalizeLandingSecurity(access.Security) == domain.LandingSecurityTLS && access.Port == port {
				return fmt.Errorf("TLS 落地端口 %d 已由 %s 落地接入 %q 使用", port, otherCore, access.Name)
			}
		}
	}
	if a.PortFree != nil {
		if err := a.PortFree(port); err != nil {
			return fmt.Errorf("TLS 落地端口不可用: %w", err)
		}
	}
	return nil
}

func validateLandingPeer(peer domain.LandingPeer) error {
	security := domain.NormalizeLandingSecurity(strings.ToLower(strings.TrimSpace(peer.Security)))
	if security != domain.LandingSecurityReality && security != domain.LandingSecurityTLS {
		return fmt.Errorf("落地安全协议无效: %q", peer.Security)
	}
	if peer.Core != domain.CoreXray && peer.Core != domain.CoreSingBox {
		return fmt.Errorf("落地内核无效: %q", peer.Core)
	}
	if err := system.ValidateServer(peer.Server); err != nil {
		return fmt.Errorf("落地地址无效: %w", err)
	}
	if ip := net.ParseIP(peer.Server); ip != nil {
		allowTestLAN := relayTestLAN.Contains(ip)
		blocked := !ip.IsGlobalUnicast() || (ip.IsPrivate() && !allowTestLAN) || ip.IsLoopback() || ip.IsUnspecified() || ip.IsMulticast() || ip.IsLinkLocalUnicast()
		for _, raw := range domain.BlockedDestinationCIDRs() {
			_, network, err := net.ParseCIDR(raw)
			if err == nil && network.Contains(ip) && !allowTestLAN {
				blocked = true
				break
			}
		}
		if blocked {
			return fmt.Errorf("落地地址必须是公网单播 IP 或有效主机名")
		}
	}
	if err := system.ValidatePort(peer.Port); err != nil {
		return fmt.Errorf("落地端口无效: %w", err)
	}
	if err := system.ValidateSNI(peer.SNI); err != nil {
		return fmt.Errorf("落地 SNI 无效: %w", err)
	}
	if !uuidPattern.MatchString(peer.UUID) {
		return fmt.Errorf("落地 UUID 格式无效")
	}
	if security == domain.LandingSecurityReality {
		if strings.TrimSpace(peer.PublicKey) == "" {
			return fmt.Errorf("落地 REALITY 公钥不能为空")
		}
		if !shortIDPattern.MatchString(peer.ShortID) || len(peer.ShortID)%2 != 0 {
			return fmt.Errorf("落地 short ID 必须为 2-16 个十六进制字符")
		}
	} else {
		if !domain.ValidCertificateSHA256(peer.CertificateSHA256) || !domain.ValidCertificatePublicKeySHA256(peer.CertificatePublicKeySHA256) {
			return fmt.Errorf("TLS 落地连接文本缺少合法证书指纹；旧配置请在落地端删除并重新创建后再导入")
		}
	}
	if peer.Flow != domain.VisionFlow {
		return fmt.Errorf("落地 flow 必须为 %s", domain.VisionFlow)
	}
	return nil
}

func mustCIDR(raw string) *net.IPNet {
	_, network, err := net.ParseCIDR(raw)
	if err != nil {
		panic(err)
	}
	return network
}

func probeTCP(ctx context.Context, host string, port int) error {
	dialer := net.Dialer{Timeout: 3 * time.Second}
	conn, err := dialer.DialContext(ctx, "tcp", net.JoinHostPort(host, fmt.Sprintf("%d", port)))
	if err != nil {
		return err
	}
	return conn.Close()
}

func (a *App) probeRelayTCP(ctx context.Context, host string, port int) error {
	if a.Reachable != nil {
		return a.Reachable(ctx, host, port)
	}
	return probeTCP(ctx, host, port)
}

func writePrivateFile(path string, b []byte, force bool) error {
	flags := os.O_WRONLY | os.O_CREATE
	if force {
		flags |= os.O_TRUNC
	} else {
		flags |= os.O_EXCL
	}
	f, err := os.OpenFile(path, flags, 0600)
	if err != nil {
		return err
	}
	if err = f.Chmod(0600); err != nil {
		_ = f.Close()
		return err
	}
	if _, err = f.Write(b); err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	return err
}
