package docker

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strconv"

	"proxyforge/internal/docker/model"
	"proxyforge/internal/docker/xray/accesspolicy"
)

func decodeNode(b []byte, n *model.Node) error { return decodeStrict(b, n) }
func nodePorts(n model.Node) []int {
	seen := map[int]bool{n.Port: true}
	if n.XrayFallbackGuard {
		seen[n.XrayFallbackPort] = true
	}
	for _, a := range n.LandingAccesses {
		if a.Enabled && !a.UsesPrimaryInbound() {
			seen[a.Port] = true
		}
	}
	var ports []int
	for p := range seen {
		if p > 0 {
			ports = append(ports, p)
		}
	}
	sort.Ints(ports)
	return ports
}
func (m *Manager) readOperation(ctx context.Context) (Metadata, *inspection, error) {
	if err := m.CheckReady(ctx); err != nil {
		return Metadata{}, nil, err
	}
	if err := m.recoverLocked(ctx); err != nil {
		return Metadata{}, nil, err
	}
	if err := m.checkOwner(); err != nil {
		return Metadata{}, nil, err
	}
	md, err := m.metadata()
	if err != nil {
		return md, nil, err
	}
	if err = m.checkCompose(md); err != nil {
		return md, nil, err
	}
	if err = m.checkProject(ctx); err != nil {
		return md, nil, err
	}
	in, err := m.inspect(ctx)
	if err == nil && in != nil && in.State.Paused {
		err = fmt.Errorf("实例已暂停，请先恢复容器运行状态")
	}
	if err == nil && in != nil && in.Image != md.ImageID {
		err = fmt.Errorf("实际容器镜像与记录不一致")
	}
	return md, in, err
}
func (m *Manager) Deploy(ctx context.Context, version string, opts model.GenerateOptions) error {
	if _, err := m.metadata(); os.IsNotExist(err) {
		if err = m.Prepare(ctx, version); err != nil {
			return err
		}
	} else if err != nil {
		return err
	} else if version != "" {
		md, err := m.metadata()
		if err != nil {
			return err
		}
		if err = checkPreparedVersion(md, version); err != nil {
			return err
		}
	}
	return m.Generate(ctx, opts, true)
}
func (m *Manager) Generate(ctx context.Context, opts model.GenerateOptions, start bool) error {
	return m.withLock(ctx, func() error {
		md, in, err := m.readOperation(ctx)
		if err != nil {
			return err
		}
		oldState, err := m.LoadState()
		hasOld := err == nil
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		if hasOld {
			if _, _, err = m.managedConfig(); err != nil {
				return err
			}
		} else if _, err = os.Lstat(m.paths.Config()); !os.IsNotExist(err) {
			return fmt.Errorf("存在无状态的配置，拒绝覆盖")
		}
		n := oldState.Node
		n.Core = model.CoreXray
		n.ManagedBy = "proxyforge"
		n.SchemaVersion = 1
		n.UpdatedAt = m.now().UTC()
		n.CoreVersion = md.Version
		security := opts.Security
		if security == "" && hasOld {
			security = n.Security
		}
		n.Security = model.NormalizeLandingSecurity(security)
		if opts.DropLinks {
			n.LandingAccesses = nil
			n.RelayLinks = nil
		} else if hasOld && n.Security != model.NormalizeLandingSecurity(oldState.Node.Security) && (len(n.LandingAccesses) > 0 || len(n.RelayLinks) > 0) {
			return fmt.Errorf("切换主节点协议前请移除线路，或显式使用 --drop-links")
		}
		if opts.Server != "" {
			n.Server = opts.Server
		}
		if opts.Port != 0 {
			n.Port = opts.Port
		}
		if n.Port == 0 {
			n.Port = 443
		}
		if opts.UserName != "" {
			n.UserName = opts.UserName
		}
		if n.UserName == "" {
			n.UserName = "one"
		}
		if opts.InboundTag != "" {
			n.InboundTag = opts.InboundTag
		}
		if n.InboundTag == "" {
			n.InboundTag = "docker-one"
		}
		var newCert string
		success := false
		defer func() {
			if !success && newCert != "" {
				if _, err := os.Lstat(m.paths.Journal()); os.IsNotExist(err) {
					_ = os.RemoveAll(newCert)
				}
			}
		}()
		if n.IsTLS() {
			n.Target = ""
			n.XrayFallbackGuard = false
			n.XrayFallbackPort = 0
			n.XrayFallbackHTTPDomain = false
			n.XrayFallbackExactDomain = false
			if n.TLSCertificate == nil || opts.RotateCertificate || !oldState.Node.IsTLS() {
				suffix, err := randomHex(10)
				if err != nil {
					return err
				}
				cert, err := m.newCertificate("main-" + suffix)
				if err != nil {
					return err
				}
				newCert = filepath.Dir(cert.CertificateFile)
				n.TLSCertificate = &cert
			}
			n.SNI = n.TLSCertificate.SNI
		} else {
			n.TLSCertificate = nil
			if opts.SNI != "" {
				n.SNI = opts.SNI
			}
			if opts.Target != "" {
				n.Target = opts.Target
			}
			if n.Target == "" && n.SNI != "" {
				n.Target = n.SNI + ":443"
			}
			if !hasOld || opts.StandardConfig || opts.XrayFallbackGuard {
				n.XrayFallbackGuard = !opts.StandardConfig
			}
			if n.XrayFallbackGuard {
				if opts.XrayFallbackPort != 0 {
					n.XrayFallbackPort = opts.XrayFallbackPort
				}
				if n.XrayFallbackPort == 0 {
					n.XrayFallbackPort, err = m.freeHighPort(n.Port)
					if err != nil {
						return err
					}
				}
			} else {
				n.XrayFallbackPort = 0
			}
			if !n.XrayFallbackGuard {
				n.XrayFallbackHTTPDomain = false
				n.XrayFallbackExactDomain = false
			} else if opts.ApplyFallbackPolicy {
				n.XrayFallbackHTTPDomain = opts.XrayFallbackHTTPDomain
				n.XrayFallbackExactDomain = opts.XrayFallbackExactDomain
			} else {
				if opts.XrayFallbackHTTPDomain {
					n.XrayFallbackHTTPDomain = true
				}
				if opts.XrayFallbackExactDomain {
					n.XrayFallbackExactDomain = true
				}
			}
			if n.XrayFallbackGuard && n.InboundTag == "fallback-in" {
				return fmt.Errorf("入站标签 %q 已由 Xray 回落防偷跑入站使用，请选择其他标签", n.InboundTag)
			}
			if err = m.targetCheck(ctx, n.Target, n.SNI, n.Server); err != nil {
				return err
			}
		}
		if !hasOld || opts.RotateCredentials || n.UUID == "" {
			n.UUID, err = newUUID()
			if err != nil {
				return err
			}
			if !n.IsTLS() {
				keys, e := m.keyPair(ctx, md.ImageID)
				if e != nil {
					return e
				}
				n.PrivateKey, n.PublicKey = keys.Private, keys.Public
				n.ShortID, err = randomHex(8)
				if err != nil {
					return err
				}
			}
		} else if !n.IsTLS() && n.PrivateKey == "" {
			keys, e := m.keyPair(ctx, md.ImageID)
			if e != nil {
				return e
			}
			n.PrivateKey, n.PublicKey = keys.Private, keys.Public
			n.ShortID, err = randomHex(8)
			if err != nil {
				return err
			}
		}
		if n.IsTLS() {
			n.PrivateKey = ""
			n.PublicKey = ""
			n.ShortID = ""
		}
		if err = validateNode(n); err != nil {
			return err
		}
		rendered, err := m.renderNode(n)
		if err != nil {
			return err
		}
		b, err := m.codec.RenderServer(rendered)
		if err != nil {
			return err
		}
		if n.AccessPolicy.Active() {
			baseline := rendered
			baseline.AccessPolicy = model.AccessPolicy{}
			unrestricted, renderErr := m.codec.RenderServer(baseline)
			if renderErr != nil {
				return renderErr
			}
			n.AccessPolicySniffing, err = accesspolicy.SnapshotSniffing(unrestricted, rendered)
			if err != nil {
				return err
			}
		}
		if err = m.applyConfig(ctx, md, in, n, b, start || !hasOld); err != nil {
			return err
		}
		success = true
		m.progress("Xray Docker 配置已生成，端口 %d", n.Port)
		return nil
	})
}

// preferredListenPort uses 443 when it is free or already belongs to this node.
// Otherwise it keeps an existing high port, or allocates a new random one.
func (m *Manager) preferredListenPort(current int) (int, error) {
	if current == 443 || m.portFree(443) == nil {
		return 443, nil
	}
	if current >= model.FallbackPortMin && current <= model.FallbackPortMax {
		return current, nil
	}
	port, err := m.freeHighPort(443)
	if err != nil {
		return 0, fmt.Errorf("443 已被占用，且没有可用的高位端口")
	}
	return port, nil
}

func (m *Manager) freeHighPort(exclude int) (int, error) {
	for i := 0; i < 100; i++ {
		s, err := randomHex(2)
		if err != nil {
			return 0, err
		}
		v, _ := strconv.ParseInt(s, 16, 64)
		port := 30000 + int(v)%35000
		if port != exclude && m.portFree(port) == nil {
			return port, nil
		}
	}
	return 0, fmt.Errorf("找不到可用回落端口")
}
func (m *Manager) applyConfig(ctx context.Context, md Metadata, in *inspection, n model.Node, b []byte, start bool) error {
	if err := validateNode(n); err != nil {
		return err
	}
	if err := m.validateBytes(ctx, md.ImageID, b); err != nil {
		return err
	}
	old, readErr := readPrivate(m.paths.Config())
	if readErr != nil && !os.IsNotExist(readErr) {
		return readErr
	}
	configChanged := readErr != nil || string(old) != string(b)
	if !configChanged && !start {
		if s, err := m.LoadState(); err == nil {
			compare := n
			compare.ConfigSHA256 = digest(b)
			compare.UpdatedAt = s.Node.UpdatedAt
			if reflect.DeepEqual(compare, s.Node) {
				return nil
			}
		}
	}
	j, err := m.begin("config", in)
	if err != nil {
		return err
	}
	fail := func(e error) error { return m.rollback(j, e) }
	running := start || in != nil && in.State.Running
	var oldPorts []int
	if s, e := m.LoadState(); e == nil {
		oldPorts = nodePorts(s.Node)
	}
	ports := nodePorts(n)
	changed := !slices.Equal(oldPorts, ports)
	if running && changed {
		if in != nil && in.State.Running {
			if _, err = m.compose(ctx, "stop", Service); err != nil {
				return fail(err)
			}
		}
		for _, p := range ports {
			if err = m.portFree(p); err != nil {
				return fail(err)
			}
		}
	}
	if err = atomicWrite(m.paths.Config(), b, 0600); err != nil {
		return fail(err)
	}
	if err = m.phase(&j, "files-committed"); err != nil {
		return fail(err)
	}
	if running && (configChanged || start) {
		if in == nil {
			_, err = m.compose(ctx, "up", "-d", "--no-deps", "--pull", "never", Service)
		} else {
			_, err = m.compose(ctx, "restart", Service)
		}
		if err != nil {
			return fail(err)
		}
		if err = m.healthCheck(ctx, ports, md.ImageID); err != nil {
			return fail(err)
		}
	}
	if running {
		md.DesiredState = "running"
	} else {
		md.DesiredState = "stopped"
	}
	if err = m.phase(&j, "runtime-applied"); err != nil {
		return fail(err)
	}
	n.ConfigSHA256 = digest(b)
	n.UpdatedAt = m.now().UTC()
	if err = m.saveNode(n); err != nil {
		return fail(err)
	}
	md.UpdatedAt = m.now().UTC()
	if err = saveJSON(m.paths.Metadata(), md); err != nil {
		return fail(err)
	}
	if err = m.commit(j); err != nil {
		return fail(err)
	}
	return nil
}
func (m *Manager) PatchSetting(ctx context.Context, setting, value string) error {
	return m.withLock(ctx, func() error {
		md, in, err := m.readOperation(ctx)
		if err != nil {
			return err
		}
		s, b, err := m.managedConfig()
		if err != nil {
			return err
		}
		var patched []byte
		switch setting {
		case "dns":
			patched, err = m.codec.PatchDNSProfile(b, value)
		case "log":
			patched, err = m.codec.PatchLogLevel(b, value)
		case "outbound-ip":
			patched, err = m.codec.PatchOutboundIPStrategy(b, value)
		case "fallback-ip":
			if !s.Node.XrayFallbackGuard {
				return fmt.Errorf("未启用回落防护")
			}
			patched, err = m.codec.PatchFallbackIPStrategy(b, value)
		default:
			return fmt.Errorf("设置类型无效")
		}
		if err != nil {
			return err
		}
		if s.Node.AccessPolicy.Active() {
			patched, err = m.codec.PatchAccessPolicy(patched, s.Node, s.Node)
			if err != nil {
				return err
			}
		}
		return m.applyConfig(ctx, md, in, s.Node, patched, false)
	})
}
func (m *Manager) SetPolicy(ctx context.Context, policy model.AccessPolicy) error {
	return m.withLock(ctx, func() error {
		md, in, err := m.readOperation(ctx)
		if err != nil {
			return err
		}
		s, b, err := m.managedConfig()
		if err != nil {
			return err
		}
		old := s.Node
		normalized, err := policy.Normalize()
		if err != nil {
			return err
		}
		s.Node.AccessPolicy = normalized
		if !old.AccessPolicy.Active() && normalized.Active() {
			s.Node.AccessPolicySniffing, err = accesspolicy.SnapshotSniffing(b, s.Node)
			if err != nil {
				return err
			}
		}
		patched, err := m.codec.PatchAccessPolicy(b, old, s.Node)
		if err != nil {
			return err
		}
		return m.applyConfig(ctx, md, in, s.Node, patched, false)
	})
}
func (m *Manager) Reset(ctx context.Context, opts model.ResetOptions) error {
	return m.withLock(ctx, func() error {
		md, in, err := m.readOperation(ctx)
		if err != nil {
			return err
		}
		s, b, err := m.managedConfig()
		if err != nil {
			return err
		}
		old := s.Node
		n := old
		rotateCredentials := !opts.PreserveCredentials && (opts.Credentials || !opts.RotateCertificate)
		if rotateCredentials {
			n.UUID, err = newUUID()
			if err != nil {
				return err
			}
		}
		newCert := ""
		success := false
		defer func() {
			if !success && newCert != "" {
				if _, err := os.Lstat(m.paths.Journal()); os.IsNotExist(err) {
					_ = os.RemoveAll(newCert)
				}
			}
		}()
		if n.IsTLS() {
			if opts.SNI != "" || opts.Target != "" {
				return fmt.Errorf("TLS 节点请通过轮换证书更新 SNI")
			}
			if opts.RotateCertificate {
				suffix, e := randomHex(10)
				if e != nil {
					return e
				}
				cert, e := m.newCertificate("main-" + suffix)
				if e != nil {
					return e
				}
				n.TLSCertificate = &cert
				n.SNI = cert.SNI
				newCert = filepath.Dir(cert.CertificateFile)
			}
		} else {
			if opts.RotateCertificate {
				return fmt.Errorf("只有 TLS 节点可以轮换证书")
			}
			if rotateCredentials {
				keys, e := m.keyPair(ctx, md.ImageID)
				if e != nil {
					return e
				}
				n.PrivateKey, n.PublicKey = keys.Private, keys.Public
				n.ShortID, err = randomHex(8)
				if err != nil {
					return err
				}
			}
			if opts.SNI != "" {
				n.SNI = opts.SNI
				if opts.Target == "" {
					n.Target = n.SNI + ":443"
				}
			}
			if opts.Target != "" {
				n.Target = opts.Target
			}
			if err = m.targetCheck(ctx, n.Target, n.SNI, n.Server); err != nil {
				return err
			}
		}
		oldRender, err := m.renderNode(old)
		if err != nil {
			return err
		}
		newRender, err := m.renderNode(n)
		if err != nil {
			return err
		}
		patched, err := m.codec.PatchServer(b, oldRender, newRender, true)
		if err != nil {
			return err
		}
		if n.AccessPolicy.Active() {
			patched, err = m.codec.PatchAccessPolicy(patched, oldRender, newRender)
			if err != nil {
				return err
			}
		}
		if err = m.applyConfig(ctx, md, in, n, patched, false); err != nil {
			return err
		}
		success = true
		return nil
	})
}
func (m *Manager) ServerConfig() ([]byte, error) {
	if err := m.rootCheck(); err != nil {
		return nil, err
	}
	return readPrivate(m.paths.Config())
}
func (m *Manager) Validate(ctx context.Context) error {
	return m.withLock(ctx, func() error {
		md, _, err := m.readOperation(ctx)
		if err != nil {
			return err
		}
		b, err := readPrivate(m.paths.Config())
		if err != nil {
			return err
		}
		return m.validateBytes(ctx, md.ImageID, b)
	})
}
func (m *Manager) ApplyEdited(ctx context.Context, data []byte) error {
	return m.withLock(ctx, func() error {
		return m.applyEditedLocked(ctx, data)
	})
}
func (m *Manager) applyEditedLocked(ctx context.Context, data []byte) error {
	md, in, err := m.readOperation(ctx)
	if err != nil {
		return err
	}
	s, err := m.LoadState()
	if err != nil {
		return err
	}
	original, err := m.renderNode(s.Node)
	if err != nil {
		return err
	}
	// Retain explicit managed identity, allowing independent unknown JSON fields.
	if _, err = m.codec.PatchServer(data, original, original, false); err != nil {
		return fmt.Errorf("编辑破坏了受管节点身份: %w", err)
	}
	if s.Node.AccessPolicy.Active() {
		var e error
		data, e = m.codec.PatchAccessPolicy(data, original, original)
		if e != nil {
			return e
		}
	}
	if err = managedEndpointMatches(data, original); err != nil {
		return err
	}
	return m.applyConfig(ctx, md, in, s.Node, data, false)
}
func managedEndpointMatches(data []byte, n model.Node) error {
	var root struct {
		Inbounds []struct {
			Tag      string
			Port     int
			Stream   map[string]json.RawMessage `json:"streamSettings"`
			Settings json.RawMessage
		}
	}
	if err := json.Unmarshal(data, &root); err != nil {
		return err
	}
	found := 0
	for _, in := range root.Inbounds {
		if in.Tag == n.InboundTag {
			found++
			if in.Port != n.Port {
				return fmt.Errorf("请通过生成配置入口修改受管端口")
			}
			var settings struct {
				Clients []struct {
					ID    string
					Email string
					Flow  string
				}
			}
			if json.Unmarshal(in.Settings, &settings) != nil {
				return fmt.Errorf("受管客户端列表无效")
			}
			managedClients := 0
			for _, client := range settings.Clients {
				if client.Email == n.UserName || client.ID == n.UUID {
					managedClients++
					if client.ID != n.UUID || client.Email != n.UserName || client.Flow != model.VisionFlow {
						return fmt.Errorf("受管 UUID、用户名或 flow 已改变，请使用重置入口")
					}
				}
			}
			if managedClients != 1 {
				return fmt.Errorf("受管主客户端必须唯一存在")
			}
			var security string
			if json.Unmarshal(in.Stream["security"], &security) != nil || security != model.NormalizeLandingSecurity(n.Security) {
				return fmt.Errorf("受管入站协议已改变")
			}
			if n.IsTLS() {
				var tls struct {
					Certificates []struct {
						Cert string `json:"certificateFile"`
						Key  string `json:"keyFile"`
					}
				}
				if json.Unmarshal(in.Stream["tlsSettings"], &tls) != nil || len(tls.Certificates) != 1 || n.TLSCertificate == nil || tls.Certificates[0].Cert != n.TLSCertificate.CertificateFile || tls.Certificates[0].Key != n.TLSCertificate.KeyFile {
					return fmt.Errorf("受管 TLS 证书引用已改变")
				}
			} else {
				var reality struct {
					Private  string   `json:"privateKey"`
					Names    []string `json:"serverNames"`
					ShortIDs []string `json:"shortIds"`
					Target   string   `json:"target"`
				}
				if json.Unmarshal(in.Stream["realitySettings"], &reality) != nil || reality.Private != n.PrivateKey || !slices.Contains(reality.Names, n.SNI) || !slices.Contains(reality.ShortIDs, n.ShortID) {
					return fmt.Errorf("受管 REALITY 凭据或 SNI 已改变")
				}
				wantTarget := n.Target
				if n.XrayFallbackGuard {
					wantTarget = net.JoinHostPort("127.0.0.1", strconv.Itoa(n.XrayFallbackPort))
				}
				if reality.Target != wantTarget {
					return fmt.Errorf("请通过生成配置入口修改受管 REALITY target")
				}
			}
		}
	}
	if found != 1 {
		return fmt.Errorf("受管入站必须唯一存在")
	}
	return nil
}
