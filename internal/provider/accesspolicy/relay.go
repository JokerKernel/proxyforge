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
			rule := scopeRelayRule(original, n, link)
			if rule == nil {
				continue
			}
			if n.Core == domain.CoreXray {
				rule["ruleTag"] = label + "-original-" + strconv.Itoa(i+1)
			}
			tail = append(tail, rule)
			if isRelayCatchAll(original, n, link) {
				found = true
				break
			}
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
	route["rules"] = append(front, remaining...)
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
	if rule[outbound] != domain.RelayOutboundTag(link.Name) || !includes(rule[user], link.UserName) || !includes(rule[inbound], n.InboundTag) {
		return false
	}
	for field := range rule {
		if field != inbound && field != user && field != outbound && field != "action" && field != "type" && field != "ruleTag" {
			return false
		}
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
	condition := clone(original)
	rule := map[string]any{"type": "logical", "mode": "and"}
	for _, field := range []string{
		"action", "outbound", "override_address", "override_port", "network_strategy", "fallback_delay",
		"bind_interface", "inet4_bind_address", "inet6_bind_address", "routing_mark", "reuse_addr", "connect_timeout",
		"tcp_fast_open", "tcp_multi_path", "udp_fragment", "udp_timeout", "tls_fragment", "tls_fragment_fallback_delay",
		"tls_record_fragment", "method", "no_drop", "sniffer", "timeout", "server", "strategy", "disable_cache",
		"rewrite_ttl", "client_subnet", "interrupt_exist_connections",
	} {
		if value, ok := condition[field]; ok {
			rule[field] = value
			delete(condition, field)
		}
	}
	scope := map[string]any{"inbound": []string{n.InboundTag}, "auth_user": []string{link.UserName}, "rule_set": []string{scopeSet}}
	parts := []any{scope}
	if len(condition) > 0 {
		parts = append(parts, condition)
	}
	rule["rules"] = parts
	return rule
}
