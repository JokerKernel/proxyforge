// Package accesspolicy compiles destination access lists without changing relay exits.
package accesspolicy

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"proxyforge/internal/domain"
)

const (
	Prefix           = "access-"
	scopeSet         = Prefix + "scope"
	whiteSet         = Prefix + "white"
	blackSet         = Prefix + "black"
	directTag        = Prefix + "direct"
	denyTag          = Prefix + "deny"
	dnsTag           = Prefix + "dns"
	relayRoutePrefix = Prefix + "relay-route-"
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
		for _, base := range []string{"white-domain", "white-ip", "black-domain", "black-ip"} {
			if tag == base || strings.HasPrefix(tag, base+"-ports-") {
				return true
			}
		}
		return tag == Prefix+"final" || strings.HasPrefix(tag, Prefix+"white-relay-") || strings.HasPrefix(tag, Prefix+"relay-")
	}
	for _, tag := range list(rule["rule_set"]) {
		if policySet(str(tag)) {
			return true
		}
	}
	if policySet(str(rule["rule_set"])) {
		return true
	}
	for _, child := range list(rule["rules"]) {
		if ownedRule(object(child), core) {
			return true
		}
	}
	return false
}

func policySet(tag string) bool {
	return tag == scopeSet || tag == whiteSet || tag == blackSet || strings.HasPrefix(tag, relayRoutePrefix)
}

// IsPolicyRule identifies rules that are rebuilt when access settings change.
// DNS and IP settings must also update a persistent resolve rule so they survive
// that rebuild or disabling access control.
func IsPolicyRule(rule map[string]any, core string) bool {
	return ownedRule(rule, core)
}

// Strip removes only generated policy components; ordinary routes stay intact.
func Strip(root map[string]any, core string) error {
	if root == nil {
		return nil
	}
	key := "route"
	if core == domain.CoreXray {
		key = "routing"
	}
	route := object(root[key])
	if route != nil {
		rules, err := restoreRelayRoutes(list(route["rules"]), core)
		if err != nil {
			return err
		}
		route["rules"] = rules
		if core == domain.CoreSingBox {
			sets := []any{}
			for _, item := range list(route["rule_set"]) {
				if !policySet(str(object(item)["tag"])) {
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
	return nil
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

// Merge ports for each exact destination before grouping destinations by ports.
// Work on copies so the management list keeps independently removable entries.
func mergeDestinationPorts(entries []domain.AccessEntry) ([]domain.AccessEntry, error) {
	var merged []domain.AccessEntry
	positions := map[domain.AccessEntry]int{}
	for _, entry := range entries {
		ports, err := domain.NormalizeAccessPorts(entry.Ports)
		if err != nil {
			return nil, err
		}
		entry.Ports = ""
		if index, ok := positions[entry]; ok {
			previous := &merged[index]
			if previous.Ports == "" || ports == "" {
				previous.Ports = ""
			} else {
				previous.Ports, err = domain.NormalizeAccessPorts(previous.Ports + "," + ports)
				if err != nil {
					return nil, err
				}
			}
		} else {
			positions[entry] = len(merged)
			entry.Ports = ports
			merged = append(merged, entry)
		}
	}
	return merged, nil
}

// destinationGroups unions ports per destination, then groups equal port sets.
// Kept separate from persisted records so deleting a record remains exact.
func destinationGroups(entries []domain.AccessEntry) ([][]domain.AccessEntry, error) {
	merged, err := mergeDestinationPorts(entries)
	if err != nil {
		return nil, err
	}
	var groups [][]domain.AccessEntry
	positions := map[string]int{}
	for _, entry := range merged {
		index, ok := positions[entry.Ports]
		if !ok {
			index = len(groups)
			positions[entry.Ports] = index
			groups = append(groups, nil)
		}
		groups[index] = append(groups[index], entry)
	}
	return groups, nil
}

func portMatch(ports, core string) map[string]any {
	result := map[string]any{}
	if ports == "" {
		return result
	}
	if core == domain.CoreXray {
		result["port"] = ports
		return result
	}
	var singles []int
	var ranges []string
	for _, part := range strings.Split(ports, ",") {
		if strings.Contains(part, "-") {
			ranges = append(ranges, strings.ReplaceAll(part, "-", ":"))
		} else {
			port, _ := strconv.Atoi(part)
			singles = append(singles, port)
		}
	}
	if len(singles) > 0 {
		result["port"] = singles
	}
	if len(ranges) > 0 {
		result["port_range"] = ranges
	}
	return result
}

func groupedMatches(entries []domain.AccessEntry, core string) ([]map[string]any, error) {
	groups, err := destinationGroups(entries)
	if err != nil {
		return nil, err
	}
	var result []map[string]any
	for _, group := range groups {
		match := values(group, core)
		for key, value := range portMatch(group[0].Ports, core) {
			match[key] = value
		}
		result = append(result, match)
	}
	return result, nil
}

func Apply(root map[string]any, n domain.NodeSpec) error {
	if err := ValidateRoot(root, n.Core); err != nil {
		return err
	}
	policy, err := n.AccessPolicy.Normalize()
	if err != nil {
		return err
	}
	n.AccessPolicy = policy
	n.RelayLinks = append([]domain.RelayLink(nil), n.RelayLinks...)
	for i, link := range n.RelayLinks {
		if link.AccessPolicy != nil {
			p, err := link.AccessPolicy.Normalize()
			if err != nil {
				return fmt.Errorf("中转线路 %q: %w", link.Name, err)
			}
			n.RelayLinks[i].AccessPolicy = &p
		}
	}
	if n.Core == domain.CoreXray && object(root["routing"])["domainStrategy"] != "IPOnDemand" {
		requiresResolution := func(p domain.AccessPolicy) bool {
			if !p.Active() {
				return false
			}
			entries := append([]domain.AccessEntry(nil), p.Whitelist...)
			if p.BlacklistEnabled() {
				entries = append(entries, p.Blacklist...)
			}
			for _, entry := range entries {
				if entry.Type == domain.AccessEntryIP || entry.Type == domain.AccessEntryCIDR {
					return true
				}
			}
			return false
		}
		needsResolution := requiresResolution(n.AccessPolicy)
		for _, link := range n.RelayLinks {
			needsResolution = needsResolution || link.Enabled && requiresResolution(link.EffectiveAccessPolicy(n.AccessPolicy))
		}
		if needsResolution {
			return fmt.Errorf("IP/CIDR 访问策略要求 Xray routing.domainStrategy 为 IPOnDemand，请调整后重试")
		}
	}
	if err := Strip(root, n.Core); err != nil {
		return err
	}
	if err := validateRelayRoutes(root, n); err != nil {
		return err
	}
	if !n.HasActiveAccessPolicy() {
		return nil
	}
	if n.InboundTag == "" {
		return fmt.Errorf("访问策略需要受管主入站标签")
	}
	key := "route"
	if n.Core == domain.CoreXray {
		key = "routing"
	}
	originalRules := append([]any(nil), list(object(root[key])["rules"])...)
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
		if err := applyXray(root, n, policy); err != nil {
			return err
		}
		return applyRelayPolicies(root, n, originalRules)
	}
	if n.Core == domain.CoreSingBox {
		if err := applySingBox(root, n, policy); err != nil {
			return err
		}
		return applyRelayPolicies(root, n, originalRules)
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
	if p.Active() && len(p.Whitelist) > 0 {
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
	white, err := groupedMatches(p.Whitelist, n.Core)
	if err != nil {
		return err
	}
	black, err := groupedMatches(p.Blacklist, n.Core)
	if err != nil {
		return err
	}
	add := func(matches []map[string]any, outbound, user, label string) {
		for i, match := range matches {
			// Xray combines domain and IP with AND; each kind needs its own rule.
			for _, field := range []string{"domain", "ip"} {
				if value, ok := match[field]; ok {
					tag := label + "-" + field
					if strings.HasPrefix(label, "white-relay-") {
						tag = Prefix + tag
					}
					if match["port"] != nil {
						tag += "-ports-" + strconv.Itoa(i+1)
					}
					rule := map[string]any{"inboundTag": tags, field: value, "outboundTag": outbound, "ruleTag": tag}
					if match["port"] != nil {
						rule["port"] = match["port"]
					}
					if user != "" {
						rule["inboundTag"] = []string{n.InboundTag}
						rule["user"] = []string{user}
					}
					front = append(front, rule)
				}
			}
		}
	}
	if p.Active() && len(p.Whitelist) > 0 {
		for _, link := range n.RelayLinks {
			if link.Enabled && link.AccessPolicy == nil {
				add(white, domain.RelayOutboundTag(link.Name), link.UserName, "white-relay-"+link.Name)
			}
		}
		add(white, directTag, "", "white")
	}
	if p.BlacklistEnabled() {
		add(black, denyTag, "", "black")
	}

	tail := list(route["rules"])
	if p.Active() && p.EffectiveMode() == domain.AccessModeWhitelist {
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
			matches, err := groupedMatches(entries, n.Core)
			if err != nil {
				return err
			}
			sets = append(sets, map[string]any{"type": "inline", "tag": tag, "rules": matches})
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
			for _, key := range []string{"server", "timeout", "strategy", "disable_cache", "disable_optimistic_cache", "rewrite_ttl", "client_subnet"} {
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
	if p.Active() && len(p.Whitelist) > 0 {
		for _, link := range n.RelayLinks {
			if link.Enabled && link.AccessPolicy == nil {
				white(domain.RelayOutboundTag(link.Name), link.UserName)
			}
		}
		white("direct", "")
	}
	if p.BlacklistEnabled() && len(p.Blacklist) > 0 {
		front = append(front, logical([]any{scope(), map[string]any{"rule_set": []string{blackSet}}}, "reject"))
	}
	tail := list(route["rules"])
	if p.Active() && p.EffectiveMode() == domain.AccessModeWhitelist {
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
			if rule["action"] == "route" || rule["action"] == "direct" || rule["action"] == "bypass" || (rule["outbound"] != nil && rule["action"] == nil) {
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
		if policySet(tag) {
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
func AllowClient(root map[string]any, n domain.NodeSpec) error {
	policy, err := n.AccessPolicy.Normalize()
	if err != nil {
		return err
	}
	if !policy.Active() || len(policy.Whitelist) == 0 {
		return nil
	}
	matches, err := groupedMatches(policy.Whitelist, n.Core)
	if err != nil {
		return err
	}
	key := "route"
	if n.Core == domain.CoreXray {
		key = "routing"
	}
	route := object(root[key])
	if route == nil {
		return nil
	}
	rules := []any{}
	for _, match := range matches {
		if n.Core == domain.CoreXray {
			for _, field := range []string{"domain", "ip"} {
				if value, ok := match[field]; ok {
					rule := map[string]any{field: value, "outboundTag": "proxy"}
					if match["port"] != nil {
						rule["port"] = match["port"]
					}
					rules = append(rules, rule)
				}
			}
		} else {
			match["action"], match["outbound"] = "route", "proxy"
			rules = append(rules, match)
		}
	}
	route["rules"] = append(rules, list(route["rules"])...)
	return nil
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
