package xray

import (
	"encoding/json"
	"fmt"

	"proxyforge/internal/domain"
	"proxyforge/internal/provider/jsonutil"
)

// PatchServer rotates the managed node fields while preserving unrelated and
// manually added configuration.
func (*Provider) PatchServer(config []byte, old, next domain.NodeSpec, updateEndpoint bool) ([]byte, error) {
	var root map[string]any
	if err := json.Unmarshal(config, &root); err != nil {
		return nil, fmt.Errorf("解析现有 Xray 配置: %w", err)
	}
	inbound, err := xrayInbound(root, old.InboundTag)
	if err != nil {
		return nil, err
	}
	settings, err := xrayChildObject(inbound, "settings", "Xray 入站 settings")
	if err != nil {
		return nil, err
	}
	clients, err := xrayObjectList(settings, "clients", "Xray 入站 clients")
	if err != nil {
		return nil, err
	}
	client, err := xrayManagedObject(clients, "id", old.UUID, "email", old.UserName, "Xray 客户端")
	if err != nil {
		return nil, err
	}
	client["id"] = next.UUID

	stream, err := xrayChildObject(inbound, "streamSettings", "Xray 入站 streamSettings")
	if err != nil {
		return nil, err
	}
	reality, err := xrayChildObject(stream, "realitySettings", "Xray realitySettings")
	if err != nil {
		return nil, err
	}
	reality["privateKey"] = next.PrivateKey
	if err := xrayReplaceManagedString(reality, "shortIds", old.ShortID, next.ShortID, "Xray REALITY shortIds"); err != nil {
		return nil, err
	}
	if updateEndpoint {
		reality["target"] = next.Target
		if err := xrayReplaceManagedString(reality, "serverNames", old.SNI, next.SNI, "Xray REALITY serverNames"); err != nil {
			return nil, err
		}
	}
	return jsonutil.Marshal(root)
}

func xrayInbound(root map[string]any, tag string) (map[string]any, error) {
	inbounds, err := xrayObjectList(root, "inbounds", "Xray inbounds")
	if err != nil {
		return nil, err
	}
	var matches []map[string]any
	for _, inbound := range inbounds {
		if value, _ := inbound["tag"].(string); value == tag {
			matches = append(matches, inbound)
		}
	}
	if len(matches) != 1 {
		return nil, fmt.Errorf("现有 Xray 配置中 tag=%q 的入站数量为 %d，无法安全定点重置", tag, len(matches))
	}
	return matches[0], nil
}

func xrayObjectList(parent map[string]any, key, label string) ([]map[string]any, error) {
	raw, ok := parent[key].([]any)
	if !ok || len(raw) == 0 {
		return nil, fmt.Errorf("%s 不存在或为空", label)
	}
	objects := make([]map[string]any, 0, len(raw))
	for index, item := range raw {
		object, ok := item.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("%s[%d] 不是对象", label, index)
		}
		objects = append(objects, object)
	}
	return objects, nil
}

func xrayChildObject(parent map[string]any, key, label string) (map[string]any, error) {
	object, ok := parent[key].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("%s 不存在或不是对象", label)
	}
	return object, nil
}

func xrayManagedObject(objects []map[string]any, primaryKey, primaryValue, fallbackKey, fallbackValue, label string) (map[string]any, error) {
	match := func(key, value string) []map[string]any {
		if value == "" {
			return nil
		}
		var matches []map[string]any
		for _, object := range objects {
			if current, _ := object[key].(string); current == value {
				matches = append(matches, object)
			}
		}
		return matches
	}
	if matches := match(primaryKey, primaryValue); len(matches) == 1 {
		return matches[0], nil
	} else if len(matches) > 1 {
		return nil, fmt.Errorf("现有配置中匹配 %s=%q 的%s不唯一", primaryKey, primaryValue, label)
	}
	if matches := match(fallbackKey, fallbackValue); len(matches) == 1 {
		return matches[0], nil
	} else if len(matches) > 1 {
		return nil, fmt.Errorf("现有配置中匹配 %s=%q 的%s不唯一", fallbackKey, fallbackValue, label)
	}
	if len(objects) == 1 {
		return objects[0], nil
	}
	return nil, fmt.Errorf("现有配置中找不到受管%s，无法安全定点重置", label)
}

func xrayReplaceManagedString(parent map[string]any, key, oldValue, newValue, label string) error {
	values, ok := parent[key].([]any)
	if !ok || len(values) == 0 {
		return fmt.Errorf("%s 不存在或为空", label)
	}
	matched := -1
	for index, value := range values {
		if current, _ := value.(string); current == oldValue {
			if matched >= 0 {
				return fmt.Errorf("%s 中原值不唯一，无法安全定点重置", label)
			}
			matched = index
		}
	}
	if matched < 0 {
		if len(values) != 1 {
			return fmt.Errorf("%s 中找不到原值，无法安全定点重置", label)
		}
		matched = 0
	}
	values[matched] = newValue
	parent[key] = values
	return nil
}
