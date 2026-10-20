package accesspolicy

import (
	"fmt"
	"strconv"

	"proxyforge/internal/domain"
)

// Each relay finishes routing before reaching the node policy. Replaying the
// original routes inside its user scope preserves existing blocks and custom
// routing, including when this relay's access control is disabled.
func applyRelayPolicies(root map[string]any, n domain.NodeSpec, originals []any) error {
	key := "route"
	if n.Core == domain.CoreXray {
		key = "routing"
	}
	var front []any
	consumed := map[string]bool{}
	route := object(root[key])
	remaining := list(route["rules"])
	// IP/CIDR lists need the generated resolve action before relay matching too.
	if n.Core == domain.CoreSingBox && len(remaining) > 0 && object(remaining[0])["action"] == "resolve" && ownedRule(object(remaining[0]), n.Core) {
		front = append(front, remaining[0])
		remaining = remaining[1:]
	}
	for _, link := range n.RelayLinks {
		if !link.Enabled || link.AccessPolicy == nil {
			continue
		}
		if link.UserName == "" {
			return fmt.Errorf("中转线路 %q 缺少路由用户", link.Name)
		}
		policy := *link.AccessPolicy
		label := Prefix + "relay-" + link.Name
		outbound := domain.RelayOutboundTag(link.Name)
		scope := func() map[string]any {
			if n.Core == domain.CoreXray {
				return map[string]any{"inboundTag": []string{n.InboundTag}, "user": []string{link.UserName}}
			}
			return map[string]any{"inbound": []string{n.InboundTag}, "auth_user": []string{link.UserName}, "rule_set": []string{scopeSet}}
		}
		add := func(entries []domain.AccessEntry, exit, name string) error {
			matches, err := groupedMatches(entries, n.Core)
			if err != nil {
				return err
			}
			for i, match := range matches {
				if n.Core == domain.CoreXray {
					for _, field := range []string{"domain", "ip"} {
						if value, ok := match[field]; ok {
							rule := scope()
							rule[field], rule["outboundTag"] = value, exit
							rule["ruleTag"] = label + "-" + name + "-" + field + "-" + strconv.Itoa(i+1)
							if match["port"] != nil {
								rule["port"] = match["port"]
							}
							front = append(front, rule)
						}
					}
				} else {
					rule := map[string]any{"type": "logical", "mode": "and", "rules": []any{scope(), match}, "action": "route", "outbound": exit}
					if exit == denyTag {
						rule["action"] = "reject"
						delete(rule, "outbound")
					}
					front = append(front, rule)
				}
			}
			return nil
		}
		if policy.Active() {
			if err := add(policy.Whitelist, outbound, "white"); err != nil {
				return err
			}
			if policy.BlacklistEnabled() {
				if err := add(policy.Blacklist, denyTag, "black"); err != nil {
					return err
				}
			}
		}
		var tail []any
		found := false
		for i, item := range originals {
			original := object(item)
			if isRelayCatchAll(original, n, link) {
				rule := relocatedRelayRoute(original, n.Core, len(originals)-i)
				if n.Core == domain.CoreSingBox {
					marker := list(rule["rule_set"])[0]
					route["rule_set"] = append(list(route["rule_set"]), map[string]any{"type": "inline", "tag": marker, "rules": []any{map[string]any{"domain_regex": []string{".+"}, "ip_cidr": []string{"0.0.0.0/0", "::/0"}}}})
				}
				tail = append(tail, rule)
				identity, _ := relayRouteIdentity(original, n.Core)
				consumed[identity] = true
				found = true
				break
			}
			rule := scopeRelayRule(original, n, link)
			if rule == nil {
				continue
			}
			if n.Core == domain.CoreXray {
				rule["ruleTag"] = label + "-original-" + strconv.Itoa(i+1)
			}
			tail = append(tail, rule)
		}
		if !found {
			return fmt.Errorf("中转线路 %q 的原有用户路由不存在，拒绝应用访问策略", link.Name)
		}
		if policy.Active() && policy.EffectiveMode() == domain.AccessModeWhitelist {
			deny := scope()
			if n.Core == domain.CoreXray {
				deny["outboundTag"], deny["ruleTag"] = denyTag, label+"-final"
			} else {
				deny = map[string]any{"type": "logical", "mode": "and", "rules": []any{deny}, "action": "reject"}
			}
			tail = insertDefaultDeny(tail, deny, n.Core, []string{n.InboundTag}, root)
		}
		front = append(front, tail...)
	}
	for _, item := range remaining {
		identity, valid := relayRouteIdentity(object(item), n.Core)
		if !valid || !consumed[identity] {
			front = append(front, item)
		}
	}
	route["rules"] = front
	return nil
}

func validateRelayRoutes(root map[string]any, n domain.NodeSpec) error {
	key := "route"
	if n.Core == domain.CoreXray {
		key = "routing"
	}
	rules := list(object(root[key])["rules"])
	names, users := map[string]bool{}, map[string]bool{}
	for _, link := range n.RelayLinks {
		if !link.Enabled || link.AccessPolicy == nil {
			continue
		}
		if link.Name == "" || link.UserName == "" || names[link.Name] || users[link.UserName] || link.UserName == n.UserName {
			return fmt.Errorf("中转线路 %q 的名称或用户标识为空、重复或与普通用户冲突", link.Name)
		}
		names[link.Name], users[link.UserName] = true, true
		if err := validateRelayUser(root, n, link); err != nil {
			return err
		}
		count := 0
		for _, item := range rules {
			if isRelayCatchAll(object(item), n, link) {
				count++
			}
		}
		if count != 1 {
			return fmt.Errorf("中转线路 %q 的原有用户路由不唯一或不存在，拒绝应用访问策略", link.Name)
		}
	}
	return nil
}

func validateRelayUser(root map[string]any, n domain.NodeSpec, link domain.RelayLink) error {
	var clients []any
	inbounds := 0
	nameField, uuidField := "name", "uuid"
	if n.Core == domain.CoreXray {
		nameField, uuidField = "email", "id"
	}
	for _, item := range list(root["inbounds"]) {
		inbound := object(item)
		if inbound["tag"] == n.InboundTag {
			inbounds++
			if n.Core == domain.CoreXray {
				clients = list(object(inbound["settings"])["clients"])
			} else {
				clients = list(inbound["users"])
			}
		}
	}
	byName, byUUID, matched := 0, 0, 0
	for _, item := range clients {
		client := object(item)
		if client[nameField] == link.UserName {
			byName++
			if client[uuidField] == link.UUID {
				matched++
			}
		}
		if client[uuidField] == link.UUID {
			byUUID++
		}
	}
	if inbounds != 1 || link.UUID == "" || byName != 1 || byUUID != 1 || matched != 1 {
		return fmt.Errorf("中转线路 %q 的入站用户名称或 UUID 不唯一或与状态不一致，拒绝应用访问策略", link.Name)
	}
	return nil
}

func includes(value any, wanted string) bool {
	if str(value) == wanted {
		return true
	}
	for _, item := range list(value) {
		if item == wanted {
			return true
		}
	}
	return false
}

func isRelayCatchAll(rule map[string]any, n domain.NodeSpec, link domain.RelayLink) bool {
	inbound, user, outbound := "inbound", "auth_user", "outbound"
	if n.Core == domain.CoreXray {
		inbound, user, outbound = "inboundTag", "user", "outboundTag"
	}
	if _, valid := relayRouteIdentity(rule, n.Core); !valid || rule[outbound] != domain.RelayOutboundTag(link.Name) || !includes(rule[user], link.UserName) || !includes(rule[inbound], n.InboundTag) {
		return false
	}
	return true
}

func scopeRelayRule(original map[string]any, n domain.NodeSpec, link domain.RelayLink) map[string]any {
	if n.Core == domain.CoreXray {
		if original["inboundTag"] != nil && !includes(original["inboundTag"], n.InboundTag) || original["user"] != nil && !includes(original["user"], link.UserName) {
			return nil
		}
		rule := clone(original)
		rule["inboundTag"], rule["user"] = []string{n.InboundTag}, []string{link.UserName}
		return rule
	}
	// Keep inversion and nested logical matches inside the user constraint.
	// Action fields follow sing-box v1.14.3 option/rule_action.go and outbound.go.
	condition := clone(original)
	rule := map[string]any{"type": "logical", "mode": "and"}
	for _, field := range []string{
		"action", "outbound", "override_address", "override_port", "network_strategy", "fallback_delay",
		"bind_interface", "inet4_bind_address", "inet6_bind_address", "bind_address_no_port", "protect_path", "routing_mark", "reuse_addr", "netns", "connect_timeout",
		"tcp_fast_open", "tcp_multi_path", "disable_tcp_keep_alive", "tcp_keep_alive", "tcp_keep_alive_interval", "udp_fragment", "udp_timeout", "udp_connect", "udp_disable_domain_unmapping",
		"tls_fragment", "tls_fragment_fallback_delay", "tls_record_fragment", "tls_spoof", "tls_spoof_method", "method", "no_drop", "sniffer", "timeout", "server", "strategy", "disable_cache", "disable_optimistic_cache",
		"rewrite_ttl", "client_subnet", "domain_resolver", "domain_strategy", "fallback_network_type", "interrupt_exist_connections",
	} {
		if value, ok := condition[field]; ok {
			rule[field] = value
			delete(condition, field)
		}
	}
	// network_type is a match field on default rules and a dial option on
	// logical direct actions. Preserve which role it had in the original rule.
	if original["type"] == "logical" && condition["network_type"] != nil {
		rule["network_type"] = condition["network_type"]
		delete(condition, "network_type")
	}
	scope := map[string]any{"inbound": []string{n.InboundTag}, "auth_user": []string{link.UserName}, "rule_set": []string{scopeSet}}
	parts := []any{scope}
	if len(condition) > 0 {
		parts = append(parts, condition)
	}
	rule["rules"] = parts
	return rule
}
