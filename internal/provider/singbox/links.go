package singbox

import (
	"encoding/json"
	"fmt"

	"proxyforge/internal/domain"
)

func (*Provider) PatchLinks(config []byte, old, next domain.NodeSpec) ([]byte, error) {
	var root map[string]any
	if err := json.Unmarshal(config, &root); err != nil {
		return nil, fmt.Errorf("解析现有 sing-box 配置: %w", err)
	}
	inbound, err := singBoxInbound(root, next.InboundTag)
	if err != nil {
		return nil, err
	}
	rawUsers, ok := inbound["users"].([]any)
	if !ok {
		return nil, fmt.Errorf("sing-box 入站 users 不存在或格式无效")
	}
	oldNames, oldUUIDs, oldTags := singBoxLinkIdentity(old)
	for _, access := range old.LandingAccesses {
		if access.Enabled && countSingBoxUsers(rawUsers, access.UserName, access.UUID) != 1 {
			return nil, fmt.Errorf("现有 sing-box 配置中落地接入 %q 的用户不唯一或不存在，拒绝修改", access.Name)
		}
	}
	for _, link := range old.RelayLinks {
		if link.Enabled && countSingBoxUsers(rawUsers, link.UserName, link.UUID) != 1 {
			return nil, fmt.Errorf("现有 sing-box 配置中中转线路 %q 的用户不唯一或不存在，拒绝修改", link.Name)
		}
	}
	users := make([]any, 0, len(rawUsers)+len(next.LandingAccesses)+len(next.RelayLinks))
	for _, item := range rawUsers {
		user, ok := item.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("sing-box 入站 user 不是对象")
		}
		name, _ := user["name"].(string)
		uuid, _ := user["uuid"].(string)
		if oldNames[name] || oldUUIDs[uuid] {
			continue
		}
		users = append(users, user)
	}
	seenNames, seenUUIDs := map[string]bool{}, map[string]bool{}
	for _, item := range users {
		if user, ok := item.(map[string]any); ok {
			if value, _ := user["name"].(string); value != "" {
				seenNames[value] = true
			}
			if value, _ := user["uuid"].(string); value != "" {
				seenUUIDs[value] = true
			}
		}
	}
	addUser := func(name, uuid string) error {
		if seenNames[name] || seenUUIDs[uuid] {
			return fmt.Errorf("sing-box 入站用户名称或 UUID 冲突: %s", name)
		}
		seenNames[name], seenUUIDs[uuid] = true, true
		users = append(users, map[string]any{"name": name, "uuid": uuid, "flow": domain.VisionFlow})
		return nil
	}
	for _, access := range next.LandingAccesses {
		if access.Enabled {
			if err := addUser(access.UserName, access.UUID); err != nil {
				return nil, err
			}
		}
	}
	for _, link := range next.RelayLinks {
		if link.Enabled {
			if err := addUser(link.UserName, link.UUID); err != nil {
				return nil, err
			}
		}
	}
	inbound["users"] = users

	rawOutbounds, ok := root["outbounds"].([]any)
	if !ok {
		return nil, fmt.Errorf("sing-box outbounds 不存在或格式无效")
	}
	for _, link := range old.RelayLinks {
		if link.Enabled && countTaggedObjects(rawOutbounds, relayOutboundTag(link.Name)) != 1 {
			return nil, fmt.Errorf("现有 sing-box 配置中中转线路 %q 的出站不唯一或不存在，拒绝修改", link.Name)
		}
	}
	outbounds := make([]any, 0, len(rawOutbounds)+len(next.RelayLinks))
	for _, item := range rawOutbounds {
		outbound, ok := item.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("sing-box outbound 不是对象")
		}
		tag, _ := outbound["tag"].(string)
		if oldTags[tag] {
			continue
		}
		outbounds = append(outbounds, outbound)
	}
	for _, link := range next.RelayLinks {
		if !link.Enabled {
			continue
		}
		tag := relayOutboundTag(link.Name)
		for _, item := range outbounds {
			if outbound, ok := item.(map[string]any); ok && outbound["tag"] == tag {
				return nil, fmt.Errorf("sing-box 出站 tag 冲突: %s", tag)
			}
		}
		peer := link.Upstream
		outbounds = append(outbounds, map[string]any{
			"type": "vless", "tag": tag, "server": peer.Server, "server_port": peer.Port,
			"uuid": peer.UUID, "flow": domain.VisionFlow,
			"tls": map[string]any{"enabled": true, "server_name": peer.SNI,
				"utls":    map[string]any{"enabled": true, "fingerprint": "chrome"},
				"reality": map[string]any{"enabled": true, "public_key": peer.PublicKey, "short_id": peer.ShortID}},
		})
	}
	root["outbounds"] = outbounds

	route, err := childObject(root, "route", "sing-box route")
	if err != nil {
		return nil, err
	}
	rawRules, ok := route["rules"].([]any)
	if !ok {
		return nil, fmt.Errorf("sing-box route.rules 不存在或格式无效")
	}
	for _, link := range old.RelayLinks {
		if link.Enabled && countSingBoxRelayRules(rawRules, link.UserName, relayOutboundTag(link.Name)) != 1 {
			return nil, fmt.Errorf("现有 sing-box 配置中中转线路 %q 的路由不唯一或不存在，拒绝修改", link.Name)
		}
	}
	rules := make([]any, 0, len(rawRules)+len(next.RelayLinks))
	for _, item := range rawRules {
		rule, ok := item.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("sing-box route rule 不是对象")
		}
		outbound, _ := rule["outbound"].(string)
		if oldTags[outbound] {
			continue
		}
		rules = append(rules, rule)
	}
	for _, link := range next.RelayLinks {
		if !link.Enabled {
			continue
		}
		rules = append(rules, map[string]any{
			"inbound": []string{next.InboundTag}, "auth_user": []string{link.UserName},
			"action": "route", "outbound": relayOutboundTag(link.Name),
		})
	}
	route["rules"] = rules
	return marshalSingBox(root)
}

func relayOutboundTag(name string) string { return "proxyforge-relay-" + name }

func singBoxLinkIdentity(n domain.NodeSpec) (map[string]bool, map[string]bool, map[string]bool) {
	names, uuids, tags := map[string]bool{}, map[string]bool{}, map[string]bool{}
	for _, access := range n.LandingAccesses {
		names[access.UserName], uuids[access.UUID] = true, true
	}
	for _, link := range n.RelayLinks {
		names[link.UserName], uuids[link.UUID], tags[relayOutboundTag(link.Name)] = true, true, true
	}
	return names, uuids, tags
}

func countSingBoxUsers(items []any, name, uuid string) int {
	count := 0
	for _, item := range items {
		user, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if user["name"] == name || user["uuid"] == uuid {
			count++
		}
	}
	return count
}

func countTaggedObjects(items []any, tag string) int {
	count := 0
	for _, item := range items {
		if object, ok := item.(map[string]any); ok && object["tag"] == tag {
			count++
		}
	}
	return count
}

func countSingBoxRelayRules(items []any, user, outbound string) int {
	count := 0
	for _, item := range items {
		rule, ok := item.(map[string]any)
		if ok && rule["outbound"] == outbound && stringListContains(rule["auth_user"], user) {
			count++
		}
	}
	return count
}
