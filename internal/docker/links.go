package docker

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"proxyforge/internal/docker/model"
)

func cloneNode(n model.Node) (model.Node, error) {
	b, err := jsonBytes(n)
	if err != nil {
		return n, err
	}
	var copy model.Node
	err = decodeNode(b, &copy)
	return copy, err
}
func linkName(name string) error {
	if !namePattern.MatchString(name) || strings.HasPrefix(name, "access-") || name == "direct" || name == "private-block" || name == "fallback-in" || name == "fallback-out" {
		return fmt.Errorf("线路名称无效或保留")
	}
	return nil
}
func validatePeer(p model.LandingPeer) error {
	if !validServer(p.Server) || p.Port < 1 || p.Port > 65535 || !hostname(p.SNI, true) || !uuidPattern.MatchString(p.UUID) {
		return fmt.Errorf("上游节点地址/端口/SNI/UUID 无效")
	}
	security := model.NormalizeLandingSecurity(p.Security)
	if security == "tls" {
		if !model.ValidCertificateSHA256(p.CertificateSHA256) {
			return fmt.Errorf("上游 TLS 节点缺少证书固定指纹")
		}
	} else if security == "reality" {
		b, err := base64.RawURLEncoding.DecodeString(p.PublicKey)
		if err != nil || len(b) != 32 {
			return fmt.Errorf("上游 REALITY 公钥无效")
		}
		b, err = hex.DecodeString(p.ShortID)
		if err != nil || len(b) == 0 || len(b) > 8 {
			return fmt.Errorf("上游 REALITY short ID 无效")
		}
	} else {
		return fmt.Errorf("上游协议无效")
	}
	return nil
}
func (m *Manager) patchLinks(ctx context.Context, md Metadata, in *inspection, old, next model.Node, b []byte) error {
	oldRender, err := m.renderNode(old)
	if err != nil {
		return err
	}
	newRender, err := m.renderNode(next)
	if err != nil {
		return err
	}
	patched, err := m.codec.PatchLinks(b, oldRender, newRender)
	if err != nil {
		return err
	}
	if old.AccessPolicy.Active() || next.AccessPolicy.Active() {
		patched, err = m.codec.PatchAccessPolicy(patched, oldRender, newRender)
		if err != nil {
			return err
		}
	}
	return m.applyConfig(ctx, md, in, next, patched, false)
}
func (m *Manager) AddLanding(ctx context.Context, name, security string, port int) (model.LandingPeer, error) {
	var peer model.LandingPeer
	err := m.withLock(ctx, func() error {
		if err := linkName(name); err != nil {
			return err
		}
		md, in, err := m.readOperation(ctx)
		if err != nil {
			return err
		}
		s, b, err := m.managedConfig()
		if err != nil {
			return err
		}
		next, err := cloneNode(s.Node)
		if err != nil {
			return err
		}
		for _, a := range next.LandingAccesses {
			if a.Name == name {
				return fmt.Errorf("落地接入已存在")
			}
		}
		for _, r := range next.RelayLinks {
			if r.Name == name {
				return fmt.Errorf("名称已被中转线路占用")
			}
		}
		security = model.NormalizeLandingSecurity(security)
		if security == "reality" && next.IsTLS() {
			return fmt.Errorf("TLS 主节点不支持共享 REALITY 落地，请选择 tls")
		}
		id, err := newUUID()
		if err != nil {
			return err
		}
		access := model.LandingAccess{Name: name, UserName: "landing-" + name, UUID: id, Security: security, Enabled: true, UpdatedAt: m.now().UTC()}
		certDir := ""
		success := false
		defer func() {
			if !success && certDir != "" {
				if _, err := os.Lstat(m.paths.Journal()); os.IsNotExist(err) {
					_ = os.RemoveAll(certDir)
				}
			}
		}()
		if security == "tls" {
			if port == 0 {
				port, err = m.freeHighPort(next.Port)
				if err != nil {
					return err
				}
			}
			if port < 1024 || port > 65535 || slices.Contains(nodePorts(next), port) {
				return fmt.Errorf("TLS 落地端口无效或重复")
			}
			cert, e := m.newCertificate(name)
			if e != nil {
				return e
			}
			certDir = filepath.Dir(cert.CertificateFile)
			access.SNI = cert.SNI
			access.Port = port
			access.CertificateFile = cert.CertificateFile
			access.KeyFile = cert.KeyFile
			access.CertificateSHA256 = cert.CertificateSHA256
			access.CertificatePublicKeySHA256 = cert.CertificatePublicKeySHA256
		} else if security != "reality" {
			return fmt.Errorf("落地协议必须为 reality 或 tls")
		}
		next.LandingAccesses = append(next.LandingAccesses, access)
		if err = m.patchLinks(ctx, md, in, s.Node, next, b); err != nil {
			return err
		}
		success = true
		peer = landingPeer(next, access)
		return nil
	})
	return peer, err
}
func landingPeer(n model.Node, a model.LandingAccess) model.LandingPeer {
	p := model.LandingPeer{Name: a.Name, Core: "xray", Security: model.NormalizeLandingSecurity(a.Security), Server: n.Server, Port: n.Port, SNI: n.SNI, UUID: a.UUID, Flow: model.VisionFlow}
	if a.UsesPrimaryInbound() {
		p.PublicKey = n.PublicKey
		p.ShortID = n.ShortID
	} else {
		p.Port = a.Port
		p.SNI = a.SNI
		p.CertificateSHA256 = a.CertificateSHA256
		p.CertificatePublicKeySHA256 = a.CertificatePublicKeySHA256
	}
	return p
}
func (m *Manager) AddRelay(ctx context.Context, name string, peer model.LandingPeer) error {
	return m.withLock(ctx, func() error {
		if err := linkName(name); err != nil {
			return err
		}
		if err := validatePeer(peer); err != nil {
			return err
		}
		md, in, err := m.readOperation(ctx)
		if err != nil {
			return err
		}
		s, b, err := m.managedConfig()
		if err != nil {
			return err
		}
		next, err := cloneNode(s.Node)
		if err != nil {
			return err
		}
		for _, r := range next.RelayLinks {
			if r.Name == name {
				return fmt.Errorf("中转线路已存在")
			}
		}
		for _, a := range next.LandingAccesses {
			if a.Name == name {
				return fmt.Errorf("名称已被落地接入占用")
			}
		}
		id, err := newUUID()
		if err != nil {
			return err
		}
		peer.Core = "xray"
		peer.Flow = model.VisionFlow
		peer.Security = model.NormalizeLandingSecurity(peer.Security)
		next.RelayLinks = append(next.RelayLinks, model.RelayLink{Name: name, UserName: "relay-" + name, UUID: id, Enabled: true, Upstream: peer, UpdatedAt: m.now().UTC()})
		return m.patchLinks(ctx, md, in, s.Node, next, b)
	})
}
func (m *Manager) ChangeLink(ctx context.Context, kind, name, action string) error {
	return m.withLock(ctx, func() error {
		md, in, err := m.readOperation(ctx)
		if err != nil {
			return err
		}
		s, b, err := m.managedConfig()
		if err != nil {
			return err
		}
		next, err := cloneNode(s.Node)
		if err != nil {
			return err
		}
		found := false
		if action != "remove" && action != "enable" && action != "disable" {
			return fmt.Errorf("线路操作无效")
		}
		switch kind {
		case "landing":
			for i, a := range next.LandingAccesses {
				if a.Name == name {
					found = true
					if action == "remove" {
						next.LandingAccesses = append(next.LandingAccesses[:i], next.LandingAccesses[i+1:]...)
					} else {
						next.LandingAccesses[i].Enabled = action == "enable"
						next.LandingAccesses[i].UpdatedAt = m.now().UTC()
					}
					break
				}
			}
		case "relay":
			for i, r := range next.RelayLinks {
				if r.Name == name {
					found = true
					if action == "remove" {
						next.RelayLinks = append(next.RelayLinks[:i], next.RelayLinks[i+1:]...)
					} else {
						next.RelayLinks[i].Enabled = action == "enable"
						next.RelayLinks[i].UpdatedAt = m.now().UTC()
					}
					break
				}
			}
		default:
			return fmt.Errorf("线路类型无效")
		}
		if !found {
			return fmt.Errorf("未找到线路 %s", name)
		}
		return m.patchLinks(ctx, md, in, s.Node, next, b)
	})
}
func (m *Manager) Peer(name string) (model.LandingPeer, error) {
	if err := m.rootCheck(); err != nil {
		return model.LandingPeer{}, err
	}
	s, _, err := m.managedConfig()
	if err != nil {
		return model.LandingPeer{}, err
	}
	for _, a := range s.Node.LandingAccesses {
		if a.Name == name {
			if !a.Enabled {
				return model.LandingPeer{}, fmt.Errorf("落地接入已禁用")
			}
			return landingPeer(s.Node, a), nil
		}
	}
	return model.LandingPeer{}, fmt.Errorf("未找到落地接入")
}
