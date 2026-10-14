package docker

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"proxyforge/internal/docker/model"
)

type Metadata struct {
	SchemaVersion    int       `json:"schema_version"`
	ManagedBy        string    `json:"managed_by"`
	InstanceID       string    `json:"instance_id"`
	ImageRef         string    `json:"image_ref"`
	ImageID          string    `json:"image_id"`
	Version          string    `json:"version"`
	RequestedVersion string    `json:"requested_version"`
	Channel          string    `json:"channel"`
	ComposeSHA256    string    `json:"compose_sha256"`
	DesiredState     string    `json:"desired_state"`
	UpdatedAt        time.Time `json:"updated_at"`
}
type State struct {
	SchemaVersion int        `json:"schema_version"`
	ManagedBy     string     `json:"managed_by"`
	InstanceID    string     `json:"instance_id"`
	Node          model.Node `json:"node"`
}

func (m *Manager) metadata() (Metadata, error) {
	var md Metadata
	err := loadJSON(m.paths.Metadata(), &md)
	if err != nil {
		return md, err
	}
	if md.SchemaVersion != 1 || md.ManagedBy != "proxyforge" || md.InstanceID != Instance || !validImageRef(md.ImageRef) || !validImageID(md.ImageID) || (md.DesiredState != "stopped" && md.DesiredState != "running") || (md.Channel != "stable" && md.Channel != "pinned") {
		return md, fmt.Errorf("Docker 部署记录标识或镜像无效")
	}
	return md, nil
}
func (m *Manager) LoadState() (State, error) {
	var s State
	err := loadJSON(m.paths.State(), &s)
	if err != nil {
		return s, err
	}
	if s.SchemaVersion != 1 || s.ManagedBy != "proxyforge" || s.InstanceID != Instance || s.Node.Core != model.CoreXray || s.Node.ManagedBy != "proxyforge" {
		return s, fmt.Errorf("Docker 节点状态标识无效")
	}
	return s, nil
}
func (m *Manager) managedFiles() []string {
	return []string{m.paths.Compose(), m.paths.Config(), m.paths.Metadata(), m.paths.State(), filepath.Join(m.paths.Data(), "access.json"), filepath.Join(m.paths.Data(), "landings.json"), filepath.Join(m.paths.Data(), "relays.json"), filepath.Join(m.paths.Data(), "preferences.json")}
}
func (m *Manager) saveNode(n model.Node) error {
	if n.Core != model.CoreXray || n.ManagedBy != "proxyforge" {
		return fmt.Errorf("拒绝保存非 Docker Xray 节点")
	}
	s := State{SchemaVersion: 1, ManagedBy: "proxyforge", InstanceID: Instance, Node: n}
	for _, f := range []struct {
		name string
		v    any
	}{{"access.json", n.AccessPolicy}, {"landings.json", n.LandingAccesses}, {"relays.json", n.RelayLinks}, {"state.json", s}} {
		data, err := jsonBytes(f.v)
		if err != nil {
			return err
		}
		if err := m.stateWrite(filepath.Join(m.paths.Data(), f.name), data, 0600); err != nil {
			return err
		}
	}
	return nil
}
func (m *Manager) checkCompose(md Metadata) error {
	b, err := readPrivate(m.paths.Compose())
	if err != nil {
		return err
	}
	expected, err := renderCompose(md.ImageRef)
	if err != nil {
		return err
	}
	if digest(b) != md.ComposeSHA256 || string(b) != string(expected) {
		return fmt.Errorf("Compose 文件已被外部修改，拒绝按未知定义操作容器")
	}
	return nil
}
func (m *Manager) managedConfig() (State, []byte, error) {
	s, err := m.LoadState()
	if err != nil {
		return s, nil, err
	}
	b, err := readPrivate(m.paths.Config())
	if err != nil {
		return s, nil, err
	}
	if s.Node.ConfigSHA256 == "" || digest(b) != s.Node.ConfigSHA256 {
		return s, nil, fmt.Errorf("配置与受管状态不一致；请重新生成配置或通过编辑入口恢复一致性")
	}
	return s, b, nil
}
func (m *Manager) hasData() bool {
	for _, p := range []string{m.paths.Data(), m.paths.Deploy()} {
		if _, err := os.Lstat(p); !os.IsNotExist(err) {
			return true
		}
	}
	return false
}
