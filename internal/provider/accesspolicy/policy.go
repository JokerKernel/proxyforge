// Package accesspolicy compiles destination access lists without changing relay exits.
package accesspolicy

import (
	"encoding/json"
	"fmt"
	"strings"

	"proxyforge/internal/domain"
)

const (
	Prefix    = "access-"
	scopeSet  = Prefix + "scope"
	whiteSet  = Prefix + "white"
	blackSet  = Prefix + "black"
	directTag = Prefix + "direct"
	denyTag   = Prefix + "deny"
	dnsTag    = Prefix + "dns"
)

func object(value any) map[string]any { m, _ := value.(map[string]any); return m }
func list(value any) []any {
	switch a := value.(type) {
	case []any:
		return a
	case []string:
		result := make([]any, len(a))
		for i, v := range a {
			result[i] = v
		}
		return result
	}
	return nil
}
func str(value any) string { s, _ := value.(string); return s }
func clone(m map[string]any) map[string]any {
	b, _ := json.Marshal(m)
	var copy map[string]any
	_ = json.Unmarshal(b, &copy)
	return copy
}

func ownedRule(rule map[string]any, core string) bool {
	if core == domain.CoreXray {
		tag := str(rule["ruleTag"])
		switch tag {
		case "white-domain", "white-ip", "black-domain", "black-ip", Prefix + "final":
			return true
		}
		return strings.HasPrefix(tag, Prefix+"white-relay-")
	}
	for _, tag := range list(rule["rule_set"]) {
		if str(tag) == scopeSet || str(tag) == whiteSet || str(tag) == blackSet {
			return true
		}
	}
	if str(rule["rule_set"]) == scopeSet || str(rule["rule_set"]) == whiteSet || str(rule["rule_set"]) == blackSet {
		return true
	}
	for _, child := range list(rule["rules"]) {
		if ownedRule(object(child), core) {
			return true
		}
	}
	return false
}

// Strip removes only generated policy components; ordinary routes stay intact.
func Strip(root map[string]any, core string) {
	if root == nil {
		return
	}
	key := "route"
	if core == domain.CoreXray {
		key = "routing"
	}
	route := object(root[key])
	if route != nil {
		rules := []any{}
		for _, item := range list(route["rules"]) {
			if !ownedRule(object(item), core) {
				rules = append(rules, item)
			}
		}
		route["rules"] = rules
		if core == domain.CoreSingBox {
			sets := []any{}
			for _, item := range list(route["rule_set"]) {
				if str(object(item)["tag"]) != scopeSet && str(object(item)["tag"]) != whiteSet && str(object(item)["tag"]) != blackSet {
					sets = append(sets, item)
				}
			}
			if len(sets) == 0 {
				delete(route, "rule_set")
			} else {
				route["rule_set"] = sets
			}
		}
	}
	outbounds := []any{}
	for _, item := range list(root["outbounds"]) {
		if str(object(item)["tag"]) != directTag && str(object(item)["tag"]) != denyTag {
			outbounds = append(outbounds, item)
		}
	}
	root["outbounds"] = outbounds
	if core == domain.CoreSingBox {
		dns := object(root["dns"])
		if dns != nil {
			servers := []any{}
			removed := false
			for _, item := range list(dns["servers"]) {
				if str(object(item)["tag"]) == dnsTag {
					removed = true
				} else {
					servers = append(servers, item)
				}
			}
			if removed {
				if len(servers) == 0 {
					delete(root, "dns")
				} else {
					dns["servers"] = servers
				}
			}
		}
	}
}

func proxyTags(n domain.NodeSpec) []string {
	tags := []string{n.InboundTag}
	for _, access := range n.LandingAccesses {
		if access.Enabled && !access.UsesPrimaryInbound() {
			tags = append(tags, domain.LandingTLSInboundTag(access.Name))
		}
	}
	return tags
}

func values(entries []domain.AccessEntry, core string) map[string]any {
	domains, suffixes, ips := []string{}, []string{}, []string{}
	for _, entry := range entries {
		if entry.Type == domain.AccessEntryDomain {
			if core == domain.CoreXray {
				prefix := "full:"
				if entry.IncludeSubdomains {
					prefix = "domain:"
				}
				domains = append(domains, prefix+entry.Value)
			} else {
				domains = append(domains, entry.Value)
				if entry.IncludeSubdomains {
					suffixes = append(suffixes, "."+entry.Value)
				}
			}
		} else {
			ips = append(ips, entry.Value)
		}
	}
	result := map[string]any{}
	if len(domains) > 0 {
		result["domain"] = domains
	}
	if len(suffixes) > 0 {
		result["domain_suffix"] = suffixes
	}
	if len(ips) > 0 {
		key := "ip_cidr"
		if core == domain.CoreXray {
			key = "ip"
		}
		result[key] = ips
	}
	return result
}

func Apply(root map[string]any, n domain.NodeSpec) error {
	if err := ValidateRoot(root, n.Core); err != nil {
		return err
	}
	policy, err := n.AccessPolicy.Normalize()
	if err != nil {
		return err
	}
	Strip(root, n.Core)
	if !policy.Active() {
		return nil
	}
	if n.InboundTag == "" {
		return fmt.Errorf("访问策略需要受管主入站标签")
	}
	if n.Core == domain.CoreXray {
		scoped := map[string]bool{}
		for _, tag := range proxyTags(n) {
			scoped[tag] = true
		}
		for _, item := range list(root["inbounds"]) {
			inbound := object(item)
			if scoped[str(inbound["tag"])] {
				sniffing := object(inbound["sniffing"])
				if sniffing == nil {
					sniffing = map[string]any{}
				}
				sniffing["enabled"] = false
				inbound["sniffing"] = sniffing
			}
		}
		return applyXray(root, n, policy)
	}
	if n.Core == domain.CoreSingBox {
		return applySingBox(root, n, policy)
	}
	return fmt.Errorf("未知内核 %s", n.Core)
}

func applyXray(root map[string]any, n domain.NodeSpec, p domain.AccessPolicy) error {
	route := object(root["routing"])
	if route == nil {
		return fmt.Errorf("Xray routing 不存在或格式无效")
	}
	tags := proxyTags(n)
	front := []any{}
	outbounds := list(root["outbounds"])
	if len(p.Whitelist) > 0 {
		var direct map[string]any
		for _, item := range outbounds {
			if object(item)["tag"] == "direct" {
				direct = object(item)
			}
		}
		if direct == nil || direct["protocol"] != "freedom" {
			return fmt.Errorf("白名单需要现有 direct Freedom 出站")
		}
		copied := clone(direct)
		copied["tag"] = directTag
		settings := object(copied["settings"])
		if settings == nil {
			settings = map[string]any{}
			copied["settings"] = settings
		}
		// A dedicated white-list exit preserves the direct exit's DNS/IP choices,
		// while explicitly permitting private destinations at Freedom's final check.
		settings["finalRules"] = []any{map[string]any{"action": "allow"}}
		outbounds = append(outbounds, copied)
	}
	outbounds = append(outbounds, map[string]any{"protocol": "blackhole", "settings": map[string]any{}, "tag": denyTag})
	root["outbounds"] = outbounds
	add := func(entries []domain.AccessEntry, outbound, user, label string) {
		match := values(entries, n.Core)
		// Xray combines domain and IP fields with AND; each kind gets its own rule.
		for _, field := range []string{"domain", "ip"} {
			if value, ok := match[field]; ok {
				tag := Prefix + label + "-" + field
				if label == "white" || label == "black" {
					tag = label + "-" + field
				}
				rule := map[string]any{"inboundTag": tags, field: value, "outboundTag": outbound, "ruleTag": tag}
				if user != "" {
					rule["inboundTag"] = []string{n.InboundTag}
					rule["user"] = []string{user}
				}
				front = append(front, rule)
			}
		}
	}
	if len(p.Whitelist) > 0 {
		for _, link := range n.RelayLinks {
			if link.Enabled {
				add(p.Whitelist, domain.RelayOutboundTag(link.Name), link.UserName, "white-relay-"+link.Name)
			}
		}
		add(p.Whitelist, directTag, "", "white")
	}
	if p.BlacklistEnabled() {
		add(p.Blacklist, denyTag, "", "black")
	}
	tail := list(route["rules"])
	if p.EffectiveMode() == domain.AccessModeWhitelist {
		tail = insertDefaultDeny(tail, map[string]any{"inboundTag": tags, "outboundTag": denyTag, "ruleTag": Prefix + "final"}, n.Core, tags, root)
	}
	route["rules"] = append(front, tail...)
	return nil
}

func applySingBox(root map[string]any, n domain.NodeSpec, p domain.AccessPolicy) error {
	route := object(root["route"])
	if route == nil {
		return fmt.Errorf("sing-box route 不存在或格式无效")
	}
	tags := proxyTags(n)
	sets := list(route["rule_set"])
	sets = append(sets, map[string]any{"type": "inline", "tag": scopeSet, "rules": []any{map[string]any{"domain_regex": []string{".+"}, "ip_cidr": []string{"0.0.0.0/0", "::/0"}}}})
	for _, set := range []struct {
		tag     string
		entries []domain.AccessEntry
	}{{whiteSet, p.Whitelist}, {blackSet, p.Blacklist}} {
		tag, entries := set.tag, set.entries
		if len(entries) > 0 {
			sets = append(sets, map[string]any{"type": "inline", "tag": tag, "rules": []any{values(entries, n.Core)}})
		}
	}
	route["rule_set"] = sets
	front := []any{}
	scope := func() map[string]any { return map[string]any{"inbound": tags, "rule_set": []string{scopeSet}} }
	logical := func(parts []any, action string) map[string]any {
		return map[string]any{"type": "logical", "mode": "and", "rules": parts, "action": action}
	}
	// Resolve before matching IP lists, retaining the configured resolver/strategy.
	resolver := ""
	resolve := logical([]any{scope()}, "resolve")
	for _, item := range list(route["rules"]) {
		old := object(item)
		if old["action"] == "resolve" {
			for _, key := range []string{"server", "strategy", "disable_cache", "rewrite_ttl", "client_subnet"} {
				if v, ok := old[key]; ok {
					resolve[key] = v
				}
			}
			resolver = str(old["server"])
			break
		}
	}
	if resolver == "" {
		resolver = str(route["default_domain_resolver"])
		if resolver == "" {
			resolver = str(object(route["default_domain_resolver"])["server"])
		}
		dns := object(root["dns"])
		if resolver == "" && dns != nil {
			resolver = str(dns["final"])
			if resolver == "" {
				for _, item := range list(dns["servers"]) {
					if tag := str(object(item)["tag"]); tag != "" {
						resolver = tag
						break
					}
				}
			}
		}
		if resolver == "" {
			if dns == nil {
				dns = map[string]any{}
				root["dns"] = dns
			}
			dns["servers"] = append(list(dns["servers"]), map[string]any{"type": "local", "tag": dnsTag})
			resolver = dnsTag
		}
		resolve["server"] = resolver
	}
	front = append(front, resolve)
	white := func(outbound, user string) {
		scoped := scope()
		if user != "" {
			scoped["inbound"] = []string{n.InboundTag}
			scoped["auth_user"] = []string{user}
		}
		rule := logical([]any{scoped, map[string]any{"rule_set": []string{whiteSet}}}, "route")
		rule["outbound"] = outbound
		front = append(front, rule)
	}
	if len(p.Whitelist) > 0 {
		for _, link := range n.RelayLinks {
			if link.Enabled {
				white(domain.RelayOutboundTag(link.Name), link.UserName)
			}
		}
		white("direct", "")
	}
	if p.BlacklistEnabled() && len(p.Blacklist) > 0 {
		front = append(front, logical([]any{scope(), map[string]any{"rule_set": []string{blackSet}}}, "reject"))
	}
	tail := list(route["rules"])
	if p.EffectiveMode() == domain.AccessModeWhitelist {
		tail = insertDefaultDeny(tail, logical([]any{scope()}, "reject"), n.Core, tags, root)
	}
	route["rules"] = append(front, tail...)
	return nil
}

// Keep existing block/control rules ahead of the whitelist-only default deny,
// while placing the deny before any catch-all direct or relay route.
func insertDefaultDeny(rules []any, deny map[string]any, core string, tags []string, root map[string]any) []any {
	blocked := map[string]bool{}
	for _, item := range list(root["outbounds"]) {
		out := object(item)
		if out["protocol"] == "blackhole" || out["type"] == "block" {
			blocked[str(out["tag"])] = true
		}
	}
	index := len(rules)
	for i, item := range rules {
		rule := object(item)
		key := "inbound"
		if core == domain.CoreXray {
			key = "inboundTag"
		}
		scoped := list(rule[key])
		intersects := len(scoped) == 0
		for _, value := range scoped {
			for _, tag := range tags {
				if value == tag {
					intersects = true
				}
			}
		}
		if !intersects {
			continue
		}
		if core == domain.CoreXray {
			if !blocked[str(rule["outboundTag"])] {
				index = i
				break
			}
		} else {
			if rule["action"] == "route" || (rule["outbound"] != nil && rule["action"] == nil) {
				index = i
				break
			}
		}
	}
	result := append([]any{}, rules[:index]...)
	result = append(result, deny)
	return append(result, rules[index:]...)
}

func HasComponents(root map[string]any, core string) bool {
	key := "route"
	if core == domain.CoreXray {
		key = "routing"
	}
	for _, item := range list(object(root[key])["rules"]) {
		if ownedRule(object(item), core) {
			return true
		}
	}
	for _, item := range list(object(root[key])["rule_set"]) {
		tag := str(object(item)["tag"])
		if tag == scopeSet || tag == whiteSet || tag == blackSet {
			return true
		}
	}
	for _, item := range list(root["outbounds"]) {
		tag := str(object(item)["tag"])
		if tag == directTag || tag == denyTag {
			return true
		}
	}
	return false
}

// Client exceptions let explicitly allowed private targets reach the server,
// which remains responsible for the complete access policy.
func AllowClient(root map[string]any, n domain.NodeSpec) {
	if !n.AccessPolicy.Active() || len(n.AccessPolicy.Whitelist) == 0 {
		return
	}
	match := values(n.AccessPolicy.Whitelist, n.Core)
	if n.Core == domain.CoreXray {
		route := object(root["routing"])
		if route == nil {
			return
		}
		rules := []any{}
		for _, field := range []string{"domain", "ip"} {
			if value, ok := match[field]; ok {
				rules = append(rules, map[string]any{field: value, "outboundTag": "proxy"})
			}
		}
		route["rules"] = append(rules, list(route["rules"])...)
	} else {
		route := object(root["route"])
		if route == nil {
			return
		}
		match["action"], match["outbound"] = "route", "proxy"
		route["rules"] = append([]any{match}, list(route["rules"])...)
	}
}

// SnapshotSniffing preserves managed Xray inbound settings when enabling a policy.
// Policy matching uses the proxy request's actual destination, not an arbitrary
// Host/SNI supplied inside an application payload.
func SnapshotSniffing(config []byte, n domain.NodeSpec) (map[string]json.RawMessage, error) {
	if n.Core != domain.CoreXray {
		return nil, nil
	}
	var root map[string]any
	if err := json.Unmarshal(config, &root); err != nil {
		return nil, err
	}
	scoped := map[string]bool{}
	for _, tag := range proxyTags(n) {
		scoped[tag] = true
	}
	result := map[string]json.RawMessage{}
	for _, item := range list(root["inbounds"]) {
		inbound := object(item)
		tag := str(inbound["tag"])
		if scoped[tag] {
			b, err := json.Marshal(inbound["sniffing"])
			if err != nil {
				return nil, err
			}
			result[tag] = b
		}
	}
	return result, nil
}

func RestoreSniffing(root map[string]any, old domain.NodeSpec) error {
	if old.Core != domain.CoreXray {
		return nil
	}
	scoped := map[string]bool{}
	for _, tag := range proxyTags(old) {
		scoped[tag] = true
	}
	for _, item := range list(root["inbounds"]) {
		inbound := object(item)
		tag := str(inbound["tag"])
		if !scoped[tag] {
			continue
		}
		raw := old.AccessPolicySniffing[tag]
		if len(raw) == 0 || string(raw) == "null" {
			delete(inbound, "sniffing")
			continue
		}
		var original map[string]any
		if err := json.Unmarshal(raw, &original); err != nil {
			return err
		}
		inbound["sniffing"] = original
	}
	return nil
}

// Invalid manually edited containers must be rejected rather than silently replaced.
func ValidateRoot(root map[string]any, core string) error {
	if root == nil {
		return fmt.Errorf("服务端配置必须是 JSON 对象")
	}
	key := "route"
	if core == domain.CoreXray {
		key = "routing"
	}
	route := object(root[key])
	if route == nil {
		return fmt.Errorf("%s 配置不存在或不是对象", key)
	}
	for _, field := range []struct {
		parent   map[string]any
		key      string
		required bool
	}{{route, "rules", false}, {root, "outbounds", true}} {
		value, exists := field.parent[field.key]
		if !exists && !field.required {
			continue
		}
		items, ok := value.([]any)
		if !ok {
			return fmt.Errorf("%s 必须是对象数组", field.key)
		}
		for _, item := range items {
			if object(item) == nil {
				return fmt.Errorf("%s 包含非对象条目", field.key)
			}
		}
	}
	if core == domain.CoreSingBox {
		if value, exists := route["rule_set"]; exists {
			sets, ok := value.([]any)
			if !ok {
				return fmt.Errorf("rule_set 必须是数组")
			}
			for _, set := range sets {
				if object(set) == nil {
					return fmt.Errorf("rule_set 包含非对象条目")
				}
			}
		}
	}
	return nil
}
