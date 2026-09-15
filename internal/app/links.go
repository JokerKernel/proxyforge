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

var linkNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,31}$`)
var uuidPattern = regexp.MustCompile(`(?i)^[0-9a-f]{8}-[0-9a-f]{4}-[1-5][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
var shortIDPattern = regexp.MustCompile(`(?i)^[0-9a-f]{2,16}$`)
var relayTestLAN = mustCIDR("192.168.0.0/16")

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
	uuid, err := system.UUID()
	if err != nil {
		return domain.LandingAccess{}, err
	}
	access := domain.LandingAccess{Name: name, UserName: "proxyforge-landing-" + name, UUID: uuid, Enabled: true, UpdatedAt: a.Now().UTC()}
	n.LandingAccesses = append(n.LandingAccesses, access)
	if _, err := a.applyLinks(ctx, core, n, "落地接入"); err != nil {
		return domain.LandingAccess{}, err
	}
	return access, nil
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
	n.LandingAccesses = append(n.LandingAccesses[:index], n.LandingAccesses[index+1:]...)
	_, err = a.applyLinks(ctx, core, n, "落地接入")
	return err
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
		return LandingBundle{}, fmt.Errorf("落地接入 %q 已停用，不能导出连接文件", name)
	}
	return LandingBundle{ManagedBy: "proxyforge", SchemaVersion: 1, Kind: landingBundleKind, Peer: domain.LandingPeer{
		Name: access.Name, Core: core, Server: n.Server, Port: n.Port, SNI: n.SNI, UUID: access.UUID,
		PublicKey: n.PublicKey, ShortID: n.ShortID, Flow: domain.VisionFlow,
	}}, nil
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
		return domain.LandingPeer{}, fmt.Errorf("解析落地连接文件: %w", err)
	}
	if bundle.ManagedBy != "proxyforge" || bundle.Kind != landingBundleKind || bundle.SchemaVersion != 1 {
		return domain.LandingPeer{}, fmt.Errorf("落地连接文件标识或版本无效")
	}
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
	if _, ok := findRelay(n, name); ok {
		return domain.RelayLink{}, fmt.Errorf("中转线路 %q 已存在", name)
	}
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
	link := domain.RelayLink{Name: name, UserName: "proxyforge-relay-" + name, UUID: uuid, Enabled: true, Upstream: peer, UpdatedAt: a.Now().UTC()}
	n.RelayLinks = append(n.RelayLinks, link)
	if _, err := a.applyLinks(ctx, core, n, "中转线路"); err != nil {
		return domain.RelayLink{}, err
	}
	return link, nil
}

func (a *App) UpdateRelayLink(ctx context.Context, core, name string, peer domain.LandingPeer, opts RelayAddOptions) error {
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

func validateLandingPeer(peer domain.LandingPeer) error {
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
	if strings.TrimSpace(peer.PublicKey) == "" {
		return fmt.Errorf("落地 REALITY 公钥不能为空")
	}
	if !shortIDPattern.MatchString(peer.ShortID) || len(peer.ShortID)%2 != 0 {
		return fmt.Errorf("落地 short ID 必须为 2-16 个十六进制字符")
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
