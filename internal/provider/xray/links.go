package xray

import (
	"encoding/json"
	"fmt"

	"proxyforge/internal/domain"
)

func (*Provider) PatchLinks(config []byte, old, next domain.NodeSpec) ([]byte, error) {
	var root map[string]any
	if err := json.Unmarshal(config, &root); err != nil {
		return nil, fmt.Errorf("解析现有 Xray 配置: %w", err)
	}
	inbound, err := xrayInbound(root, next.InboundTag)
	if err != nil {
		return nil, err
	}
	settings, err := xrayChildObject(inbound, "settings", "Xray 入站 settings")
	if err != nil {
		return nil, err
	}
	rawClients, ok := settings["clients"].([]any)
	if !ok {
		return nil, fmt.Errorf("Xray 入站 clients 不存在或格式无效")
	}
	oldNames, oldUUIDs, oldTags := xrayLinkIdentity(old)
	for _, access := range old.LandingAccesses {
		if domain.NormalizeLandingSecurity(access.Security) == domain.LandingSecurityReality && access.Enabled && countXrayClients(rawClients, access.UserName, access.UUID) != 1 {
			return nil, fmt.Errorf("现有 Xray 配置中落地接入 %q 的用户不唯一或不存在，拒绝修改", access.Name)
		}
	}
	for _, link := range old.RelayLinks {
		if link.Enabled && countXrayClients(rawClients, link.UserName, link.UUID) != 1 {
			return nil, fmt.Errorf("现有 Xray 配置中中转线路 %q 的用户不唯一或不存在，拒绝修改", link.Name)
		}
	}
	clients := make([]any, 0, len(rawClients)+len(next.LandingAccesses)+len(next.RelayLinks))
	for _, item := range rawClients {
		client, ok := item.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("Xray 入站 client 不是对象")
		}
		name, _ := client["email"].(string)
		uuid, _ := client["id"].(string)
		if oldNames[name] || oldUUIDs[uuid] {
			continue
		}
		clients = append(clients, client)
	}
	seenNames, seenUUIDs := map[string]bool{}, map[string]bool{}
	for _, item := range clients {
		if client, ok := item.(map[string]any); ok {
			if value, _ := client["email"].(string); value != "" {
				seenNames[value] = true
			}
			if value, _ := client["id"].(string); value != "" {
				seenUUIDs[value] = true
			}
		}
	}
	addClient := func(name, uuid string) error {
		if seenNames[name] || seenUUIDs[uuid] {
			return fmt.Errorf("Xray 入站用户名称或 UUID 冲突: %s", name)
		}
		seenNames[name], seenUUIDs[uuid] = true, true
		clients = append(clients, map[string]any{"id": uuid, "email": name, "flow": domain.VisionFlow})
		return nil
	}
	for _, access := range next.LandingAccesses {
		if domain.NormalizeLandingSecurity(access.Security) == domain.LandingSecurityReality && access.Enabled {
			if err := addClient(access.UserName, access.UUID); err != nil {
				return nil, err
			}
		}
	}
	for _, link := range next.RelayLinks {
		if link.Enabled {
			if err := addClient(link.UserName, link.UUID); err != nil {
				return nil, err
			}
		}
	}
	settings["clients"] = clients

	rawInbounds, ok := root["inbounds"].([]any)
	if !ok {
		return nil, fmt.Errorf("Xray inbounds 不存在或格式无效")
	}
	oldTLSInboundTags := map[string]bool{}
	for _, access := range old.LandingAccesses {
		if domain.NormalizeLandingSecurity(access.Security) != domain.LandingSecurityTLS {
			continue
		}
		tag := xrayLandingTLSInboundTag(access.Name)
		oldTLSInboundTags[tag] = true
		if access.Enabled && countXrayLandingTLSInbounds(rawInbounds, tag, access.UserName, access.UUID) != 1 {
			return nil, fmt.Errorf("现有 Xray 配置中 TLS 落地接入 %q 的入站不唯一或不存在，拒绝修改", access.Name)
		}
	}
	inbounds := make([]any, 0, len(rawInbounds)+len(next.LandingAccesses))
	for _, item := range rawInbounds {
		object, ok := item.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("Xray inbound 不是对象")
		}
		tag, _ := object["tag"].(string)
		if oldTLSInboundTags[tag] {
			continue
		}
		inbounds = append(inbounds, object)
	}
	for _, access := range next.LandingAccesses {
		if domain.NormalizeLandingSecurity(access.Security) != domain.LandingSecurityTLS || !access.Enabled {
			continue
		}
		tag := xrayLandingTLSInboundTag(access.Name)
		if countXrayTaggedObjects(inbounds, tag) != 0 {
			return nil, fmt.Errorf("Xray TLS 落地入站 tag 冲突: %s", tag)
		}
		inbounds = append(inbounds, map[string]any{
			"listen": "0.0.0.0", "port": access.Port, "protocol": "vless", "tag": tag,
			"settings": map[string]any{"decryption": "none", "clients": []any{map[string]any{
				"id": access.UUID, "email": access.UserName, "flow": domain.VisionFlow,
			}}},
			"streamSettings": map[string]any{
				"network": "raw", "security": "tls", "tlsSettings": map[string]any{
					"minVersion": "1.3", "certificates": []any{map[string]any{
						"certificateFile": access.CertificateFile, "keyFile": access.KeyFile,
					}},
				},
			},
		})
	}
	root["inbounds"] = inbounds

	rawOutbounds, ok := root["outbounds"].([]any)
	if !ok {
		return nil, fmt.Errorf("Xray outbounds 不存在或格式无效")
	}
	for _, link := range old.RelayLinks {
		if link.Enabled && countXrayTaggedObjects(rawOutbounds, xrayRelayOutboundTag(link.Name)) != 1 {
			return nil, fmt.Errorf("现有 Xray 配置中中转线路 %q 的出站不唯一或不存在，拒绝修改", link.Name)
		}
	}
	outbounds := make([]any, 0, len(rawOutbounds)+len(next.RelayLinks))
	for _, item := range rawOutbounds {
		outbound, ok := item.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("Xray outbound 不是对象")
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
		tag := xrayRelayOutboundTag(link.Name)
		for _, item := range outbounds {
			if outbound, ok := item.(map[string]any); ok && outbound["tag"] == tag {
				return nil, fmt.Errorf("Xray 出站 tag 冲突: %s", tag)
			}
		}
		peer := link.Upstream
		streamSettings := map[string]any{}
		if domain.NormalizeLandingSecurity(peer.Security) == domain.LandingSecurityTLS {
			streamSettings = map[string]any{
				"network": "raw", "security": "tls",
				"tlsSettings": map[string]any{"serverName": peer.SNI, "fingerprint": "chrome", "minVersion": "1.3"},
			}
		} else {
			streamSettings = map[string]any{
				"network": "raw", "security": "reality",
				"realitySettings": map[string]any{"serverName": peer.SNI, "fingerprint": "chrome", "password": peer.PublicKey, "shortId": peer.ShortID, "spiderX": "/"},
			}
		}
		outbounds = append(outbounds, map[string]any{
			"protocol": "vless",
			"settings": map[string]any{"vnext": []any{map[string]any{
				"address": peer.Server, "port": peer.Port,
				"users": []any{map[string]any{"id": peer.UUID, "encryption": "none", "flow": domain.VisionFlow}},
			}}},
			"tag":            tag,
			"streamSettings": streamSettings,
		})
	}
	root["outbounds"] = outbounds

	routing, err := xrayChildObject(root, "routing", "Xray routing")
	if err != nil {
		return nil, err
	}
	rawRules, ok := routing["rules"].([]any)
	if !ok {
		return nil, fmt.Errorf("Xray routing.rules 不存在或格式无效")
	}
	for _, link := range old.RelayLinks {
		if link.Enabled && countXrayRelayRules(rawRules, link.UserName, xrayRelayOutboundTag(link.Name)) != 1 {
			return nil, fmt.Errorf("现有 Xray 配置中中转线路 %q 的路由不唯一或不存在，拒绝修改", link.Name)
		}
	}
	rules := make([]any, 0, len(rawRules)+len(next.RelayLinks))
	for _, item := range rawRules {
		rule, ok := item.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("Xray routing rule 不是对象")
		}
		outbound, _ := rule["outboundTag"].(string)
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
			"inboundTag": []string{next.InboundTag}, "user": []string{link.UserName},
			"outboundTag": xrayRelayOutboundTag(link.Name),
		})
	}
	routing["rules"] = rules
	return marshalXray(root)
}

func xrayRelayOutboundTag(name string) string     { return name + "-out" }
func xrayLandingTLSInboundTag(name string) string { return name + "-in" }

func xrayLinkIdentity(n domain.NodeSpec) (map[string]bool, map[string]bool, map[string]bool) {
	names, uuids, tags := map[string]bool{}, map[string]bool{}, map[string]bool{}
	for _, access := range n.LandingAccesses {
		if domain.NormalizeLandingSecurity(access.Security) == domain.LandingSecurityReality {
			names[access.UserName], uuids[access.UUID] = true, true
		}
	}
	for _, link := range n.RelayLinks {
		names[link.UserName], uuids[link.UUID], tags[xrayRelayOutboundTag(link.Name)] = true, true, true
	}
	return names, uuids, tags
}

func countXrayLandingTLSInbounds(items []any, tag, user, uuid string) int {
	count := 0
	for _, item := range items {
		inbound, ok := item.(map[string]any)
		if !ok || inbound["tag"] != tag {
			continue
		}
		settings, ok := inbound["settings"].(map[string]any)
		if !ok {
			continue
		}
		clients, ok := settings["clients"].([]any)
		if ok && countXrayClients(clients, user, uuid) == 1 {
			count++
		}
	}
	return count
}

func countXrayClients(items []any, name, uuid string) int {
	count := 0
	for _, item := range items {
		client, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if client["email"] == name || client["id"] == uuid {
			count++
		}
	}
	return count
}

func countXrayTaggedObjects(items []any, tag string) int {
	count := 0
	for _, item := range items {
		if object, ok := item.(map[string]any); ok && object["tag"] == tag {
			count++
		}
	}
	return count
}

func countXrayRelayRules(items []any, user, outbound string) int {
	count := 0
	for _, item := range items {
		rule, ok := item.(map[string]any)
		if ok && rule["outboundTag"] == outbound && xrayStringListContains(rule["user"], user) {
			count++
		}
	}
	return count
}
