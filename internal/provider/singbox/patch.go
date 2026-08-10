package singbox

import (
	"encoding/json"
	"fmt"
	"net"
	"strconv"

	"proxyforge/internal/domain"
	"proxyforge/internal/provider/jsonutil"
)

// PatchServer rotates the managed node fields while preserving unrelated and
// manually added configuration.
func (*Provider) PatchServer(config []byte, old, next domain.NodeSpec, updateEndpoint bool) ([]byte, error) {
	var root map[string]any
	if err := json.Unmarshal(config, &root); err != nil {
		return nil, fmt.Errorf("解析现有 sing-box 配置: %w", err)
	}
	inbound, err := singBoxInbound(root, old.InboundTag)
	if err != nil {
		return nil, err
	}
	users, err := objectList(inbound, "users", "sing-box 入站 users")
	if err != nil {
		return nil, err
	}
	user, err := managedObject(users, "uuid", old.UUID, "name", old.UserName, "sing-box 用户")
	if err != nil {
		return nil, err
	}
	user["uuid"] = next.UUID

	tls, err := childObject(inbound, "tls", "sing-box 入站 tls")
	if err != nil {
		return nil, err
	}
	reality, err := childObject(tls, "reality", "sing-box tls.reality")
	if err != nil {
		return nil, err
	}
	reality["private_key"] = next.PrivateKey
	if err := replaceManagedString(reality, "short_id", old.ShortID, next.ShortID, "sing-box REALITY short_id"); err != nil {
		return nil, err
	}

	if updateEndpoint {
		tls["server_name"] = next.SNI
		host, rawPort, err := net.SplitHostPort(next.Target)
		if err != nil {
			return nil, fmt.Errorf("解析 REALITY target %s: %w", next.Target, err)
		}
		port, err := strconv.Atoi(rawPort)
		if err != nil {
			return nil, fmt.Errorf("解析 REALITY target 端口 %s: %w", rawPort, err)
		}
		if old.SingBoxFallbackGuard {
			if err := patchFallbackTarget(root, host, port); err != nil {
				return nil, err
			}
			if err := patchFallbackDomain(root, old.SNI, next.SNI); err != nil {
				return nil, err
			}
		} else {
			handshake, err := childObject(reality, "handshake", "sing-box reality.handshake")
			if err != nil {
				return nil, err
			}
			handshake["server"] = host
			handshake["server_port"] = port
		}
	}
	return jsonutil.Marshal(root)
}

func patchFallbackTarget(root map[string]any, host string, port int) error {
	inbound, err := singBoxInbound(root, fallbackGuardInboundTag)
	if err != nil {
		return err
	}
	inbound["override_address"] = host
	inbound["override_port"] = port
	return nil
}

func patchFallbackDomain(root map[string]any, oldSNI, nextSNI string) error {
	route, err := childObject(root, "route", "sing-box route")
	if err != nil {
		return err
	}
	rules, err := objectList(route, "rules", "sing-box route rules")
	if err != nil {
		return err
	}
	var matches []map[string]any
	for _, rule := range rules {
		if rule["action"] == "route" && rule["outbound"] == "direct" && stringListContains(rule["inbound"], fallbackGuardInboundTag) {
			matches = append(matches, rule)
		}
	}
	if len(matches) != 1 {
		return fmt.Errorf("现有 sing-box 配置中回落放行规则数量为 %d，无法安全定点重置", len(matches))
	}
	return replaceManagedString(matches[0], "domain", oldSNI, nextSNI, "sing-box 回落放行域名")
}

func stringListContains(value any, want string) bool {
	values, ok := value.([]any)
	if !ok {
		return false
	}
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func singBoxInbound(root map[string]any, tag string) (map[string]any, error) {
	inbounds, err := objectList(root, "inbounds", "sing-box inbounds")
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
		return nil, fmt.Errorf("现有 sing-box 配置中 tag=%q 的入站数量为 %d，无法安全定点重置", tag, len(matches))
	}
	return matches[0], nil
}

func objectList(parent map[string]any, key, label string) ([]map[string]any, error) {
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

func childObject(parent map[string]any, key, label string) (map[string]any, error) {
	object, ok := parent[key].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("%s 不存在或不是对象", label)
	}
	return object, nil
}

func managedObject(objects []map[string]any, primaryKey, primaryValue, fallbackKey, fallbackValue, label string) (map[string]any, error) {
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

func replaceManagedString(parent map[string]any, key, oldValue, newValue, label string) error {
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
