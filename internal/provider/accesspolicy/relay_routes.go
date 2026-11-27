package accesspolicy

import (
	"encoding/base64"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"proxyforge/internal/domain"
)

// Relocate the existing relay route instead of retaining an unreachable copy.
// The marker records its distance from the end so removing/rebuilding a policy
// restores ordering, including routes after it. DNS/IP updates can safely add
// resolve actions at the start without moving a relay ahead of existing blocks.
func relocatedRelayRoute(original map[string]any, core string, position int) map[string]any {
	rule := clone(original)
	marker := relayRoutePrefix + strconv.Itoa(position)
	if core == domain.CoreXray {
		if previous, ok := rule["ruleTag"]; ok {
			marker += "." + base64.RawURLEncoding.EncodeToString([]byte(str(previous)))
		}
		rule["ruleTag"] = marker
	} else {
		rule["rule_set"] = []string{marker}
	}
	return rule
}

func restoreRelayRoutes(rules []any, core string) ([]any, error) {
	type located struct {
		position int
		rule     map[string]any
	}
	var restore []located
	remaining := []any{}
	positions, identities := map[int]bool{}, map[string]bool{}
	for _, item := range rules {
		rule := object(item)
		marker := str(rule["ruleTag"])
		if core == domain.CoreSingBox {
			marker = str(rule["rule_set"])
			values := list(rule["rule_set"])
			for _, value := range values {
				if strings.HasPrefix(str(value), relayRoutePrefix) {
					if len(values) != 1 {
						return nil, fmt.Errorf("中转分流规则的位置标记不能与其他规则集合并")
					}
					marker = str(value)
				}
			}
			for _, child := range list(rule["rules"]) {
				if containsRelayRouteMarker(object(child)) {
					return nil, fmt.Errorf("中转分流规则的位置标记不能嵌入逻辑规则")
				}
			}
		}
		if !strings.HasPrefix(marker, relayRoutePrefix) {
			if !ownedRule(rule, core) {
				remaining = append(remaining, item)
			}
			continue
		}
		parts := strings.SplitN(strings.TrimPrefix(marker, relayRoutePrefix), ".", 2)
		position, err := strconv.Atoi(parts[0])
		if err != nil || position < 1 || positions[position] {
			return nil, fmt.Errorf("中转路由位置标记无效或重复: %s", marker)
		}
		original := clone(rule)
		if core == domain.CoreXray {
			delete(original, "ruleTag")
			if len(parts) == 2 {
				previous, err := base64.RawURLEncoding.DecodeString(parts[1])
				if err != nil {
					return nil, fmt.Errorf("中转原始路由标签损坏: %s", marker)
				}
				original["ruleTag"] = string(previous)
			}
		} else {
			delete(original, "rule_set")
			if len(parts) != 1 {
				return nil, fmt.Errorf("中转路由位置标记无效: %s", marker)
			}
		}
		identity, valid := relayRouteIdentity(original, core)
		if !valid || identities[identity] {
			return nil, fmt.Errorf("中转分流规则损坏或重复: %s", marker)
		}
		positions[position], identities[identity] = true, true
		restore = append(restore, located{position, original})
	}
	for _, item := range remaining {
		identity, valid := relayRouteIdentity(object(item), core)
		if valid && identities[identity] {
			return nil, fmt.Errorf("中转分流规则存在重复副本，拒绝重建")
		}
	}
	sort.Slice(restore, func(i, j int) bool { return restore[i].position < restore[j].position })
	for _, entry := range restore {
		index := len(remaining) - entry.position + 1
		if index < 0 {
			return nil, fmt.Errorf("中转原始路由位置超出范围，请检查手动修改的规则")
		}
		remaining = append(remaining, nil)
		copy(remaining[index+1:], remaining[index:])
		remaining[index] = entry.rule
	}
	return remaining, nil
}

func containsRelayRouteMarker(rule map[string]any) bool {
	if strings.HasPrefix(str(rule["rule_set"]), relayRoutePrefix) {
		return true
	}
	for _, value := range list(rule["rule_set"]) {
		if strings.HasPrefix(str(value), relayRoutePrefix) {
			return true
		}
	}
	for _, child := range list(rule["rules"]) {
		if containsRelayRouteMarker(object(child)) {
			return true
		}
	}
	return false
}

func relayRouteIdentity(rule map[string]any, core string) (string, bool) {
	inbound, user, outbound := "inbound", "auth_user", "outbound"
	if core == domain.CoreXray {
		inbound, user, outbound = "inboundTag", "user", "outboundTag"
	}
	inbounds, users := list(rule[inbound]), list(rule[user])
	if len(inbounds) != 1 || len(users) != 1 || str(inbounds[0]) == "" || str(users[0]) == "" || str(rule[outbound]) == "" {
		return "", false
	}
	for field := range rule {
		if field != inbound && field != user && field != outbound && field != "action" && field != "type" && field != "ruleTag" {
			return "", false
		}
	}
	if core == domain.CoreSingBox && rule["action"] != nil && rule["action"] != "route" {
		return "", false
	}
	return str(inbounds[0]) + "\x00" + str(users[0]) + "\x00" + str(rule[outbound]), true
}
