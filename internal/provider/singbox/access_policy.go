package singbox

import (
	"encoding/json"
	"fmt"

	"proxyforge/internal/domain"
	"proxyforge/internal/provider/accesspolicy"
)

func (p *Provider) RenderServer(n domain.NodeSpec) ([]byte, error) {
	base := n
	base.AccessPolicy = domain.AccessPolicy{}
	b, err := renderServer(base)
	if err != nil {
		return nil, err
	}
	return p.PatchAccessPolicy(b, domain.NodeSpec{}, n)
}

func (*Provider) PatchAccessPolicy(config []byte, old, next domain.NodeSpec) ([]byte, error) {
	normalized, err := next.AccessPolicy.Normalize()
	if err != nil {
		return nil, err
	}
	next.AccessPolicy = normalized
	if !old.AccessPolicy.Active() && !next.AccessPolicy.Active() {
		return config, nil
	}
	old.Core, next.Core = domain.CoreSingBox, domain.CoreSingBox
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
	return marshalSingBox(root)
}
