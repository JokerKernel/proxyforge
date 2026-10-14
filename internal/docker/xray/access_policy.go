package xray

import (
	"encoding/json"
	"fmt"

	"proxyforge/internal/docker/model"
	"proxyforge/internal/docker/xray/accesspolicy"
)

func (p *Codec) RenderServer(n model.Node) ([]byte, error) {
	base := n
	base.AccessPolicy = model.AccessPolicy{}
	b, err := renderServer(base)
	if err != nil {
		return nil, err
	}
	return p.PatchAccessPolicy(b, model.Node{}, n)
}

func (*Codec) PatchAccessPolicy(config []byte, old, next model.Node) ([]byte, error) {
	normalized, err := next.AccessPolicy.Normalize()
	if err != nil {
		return nil, err
	}
	next.AccessPolicy = normalized
	if !old.AccessPolicy.Active() && !next.AccessPolicy.Active() {
		return config, nil
	}
	old.Core, next.Core = model.CoreXray, model.CoreXray
	var root map[string]any
	if err := json.Unmarshal(config, &root); err != nil {
		return nil, err
	}
	if !old.AccessPolicy.Active() && accesspolicy.HasComponents(root, next.Core) {
		return nil, fmt.Errorf("配置已使用访问策略保留名称，拒绝覆盖")
	}
	if err := accesspolicy.Apply(root, next); err != nil {
		return nil, err
	}
	if old.AccessPolicy.Active() && !next.AccessPolicy.Active() {
		if err := accesspolicy.RestoreSniffing(root, old); err != nil {
			return nil, err
		}
	}
	return marshalXray(root)
}
