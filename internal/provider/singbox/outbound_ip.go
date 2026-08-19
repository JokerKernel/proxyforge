package singbox

import (
	"fmt"
	"slices"
	"strings"

	"proxyforge/internal/provider"
)

var supportedOutboundIPStrategies = []string{
	provider.OutboundIPPreferIPv4,
	provider.OutboundIPPreferIPv6,
	provider.OutboundIPIPv4Only,
	provider.OutboundIPIPv6Only,
	provider.OutboundIPUnset,
}

var singBoxOutboundIPStrategy = map[string]string{
	provider.OutboundIPPreferIPv4: "prefer_ipv4",
	provider.OutboundIPPreferIPv6: "prefer_ipv6",
	provider.OutboundIPIPv4Only:   "ipv4_only",
	provider.OutboundIPIPv6Only:   "ipv6_only",
}

var singBoxOutboundIPByValue = map[string]string{
	"prefer_ipv4": provider.OutboundIPPreferIPv4,
	"prefer_ipv6": provider.OutboundIPPreferIPv6,
	"ipv4_only":   provider.OutboundIPIPv4Only,
	"ipv6_only":   provider.OutboundIPIPv6Only,
}

func (*Provider) OutboundIPStrategies() []string {
	return append([]string(nil), supportedOutboundIPStrategies...)
}

func (*Provider) CurrentOutboundIPStrategy(config []byte) (string, error) {
	root, outbound, err := parseDirectOutbound(config, "读取")
	if err != nil {
		return "", err
	}
	seen := make([]string, 0, 4)
	if route, ok := root["route"].(map[string]any); ok {
		if rules, ok := route["rules"].([]any); ok {
			for _, raw := range rules {
				rule, ok := raw.(map[string]any)
				if !ok {
					continue
				}
				if action, _ := rule["action"].(string); action != "resolve" {
					continue
				}
				if value, _ := rule["strategy"].(string); strings.TrimSpace(value) != "" {
					seen = append(seen, strings.TrimSpace(value))
				}
			}
		}
	}
	if len(seen) == 0 {
		if value := singBoxStrategyValue(outbound["domain_resolver"]); value != "" {
			seen = append(seen, value)
		} else if value, _ := outbound["domain_strategy"].(string); strings.TrimSpace(value) != "" {
			seen = append(seen, strings.TrimSpace(value))
		}
	}
	if len(seen) == 0 {
		return provider.OutboundIPUnset, nil
	}
	first := seen[0]
	for _, value := range seen[1:] {
		if value != first {
			return "custom", nil
		}
	}
	if mapped, ok := singBoxOutboundIPByValue[first]; ok {
		return mapped, nil
	}
	return "custom", nil
}

func (*Provider) PatchOutboundIPStrategy(config []byte, strategy string) ([]byte, error) {
	strategy = strings.ToLower(strings.TrimSpace(strategy))
	if !slices.Contains(supportedOutboundIPStrategies, strategy) {
		return nil, fmt.Errorf("sing-box 出站 IP 策略 %q 无效", strategy)
	}
	root, outbound, err := parseDirectOutbound(config, "修改")
	if err != nil {
		return nil, err
	}
	delete(outbound, "domain_strategy")
	clearLeftoverResolverStrategy(root, outbound)
	if strategy == provider.OutboundIPUnset {
		restoreSimplifiedRoute(root)
		if err := ensureFallbackDirectIsolation(root); err != nil {
			return nil, err
		}
		return marshalSingBox(root)
	}

	value := singBoxOutboundIPStrategy[strategy]
	if !setResolveRuleStrategy(root, value) {
		ensureLocalDNSServer(root)
		prependResolveRule(root, value)
	}
	if err := ensureFallbackDirectIsolation(root); err != nil {
		return nil, err
	}
	return marshalSingBox(root)
}

func (*Provider) CurrentFallbackIPStrategy(config []byte) (string, error) {
	_, outbound, err := parseFallbackDirectOutbound(config, "读取")
	if err != nil {
		return "", err
	}
	if outbound == nil {
		return provider.OutboundIPUnset, nil
	}
	value := singBoxStrategyValue(outbound["domain_resolver"])
	if value == "" {
		value, _ = outbound["domain_strategy"].(string)
		value = strings.TrimSpace(value)
	}
	if value == "" {
		return provider.OutboundIPUnset, nil
	}
	if mapped, ok := singBoxOutboundIPByValue[value]; ok {
		return mapped, nil
	}
	return "custom", nil
}

func (*Provider) PatchFallbackIPStrategy(config []byte, strategy string) ([]byte, error) {
	strategy = strings.ToLower(strings.TrimSpace(strategy))
	if !slices.Contains(supportedOutboundIPStrategies, strategy) {
		return nil, fmt.Errorf("sing-box 回落 IP 策略 %q 无效", strategy)
	}
	root, _, err := parseFallbackDirectOutbound(config, "修改")
	if err != nil {
		return nil, err
	}
	if err := ensureFallbackDirectIsolation(root); err != nil {
		return nil, err
	}
	if err := ensureFallbackDirectOutbound(root); err != nil {
		return nil, err
	}
	outbound, err := findTaggedOutbound(root, fallbackDirectOutboundTag)
	if err != nil {
		return nil, err
	}
	if outbound == nil {
		return nil, fmt.Errorf("现有 sing-box 配置中找不到 tag=%s 的出站", fallbackDirectOutboundTag)
	}
	if strategy == provider.OutboundIPUnset {
		delete(outbound, "domain_strategy")
		delete(outbound, "domain_resolver")
		return marshalSingBox(root)
	}
	ensureLocalDNSServer(root)
	value := singBoxOutboundIPStrategy[strategy]
	outbound["domain_resolver"] = map[string]any{"server": "local", "strategy": value}
	delete(outbound, "domain_strategy")
	return marshalSingBox(root)
}

func parseFallbackDirectOutbound(config []byte, _ string) (map[string]any, map[string]any, error) {
	root, err := parseDNSRoot(config)
	if err != nil {
		return nil, nil, err
	}
	if !hasTaggedObject(root, "inbounds", fallbackGuardInboundTag) {
		return nil, nil, fmt.Errorf("现有 sing-box 配置未启用回落防偷跑，无法修改回落 IP")
	}
	outbound, err := findTaggedOutbound(root, fallbackDirectOutboundTag)
	if err != nil {
		return nil, nil, err
	}
	if outbound == nil {
		return root, nil, nil
	}
	if typeName, _ := outbound["type"].(string); typeName != "" && typeName != "direct" {
		return nil, nil, fmt.Errorf("现有 sing-box 配置中 tag=%s 的出站不是 direct", fallbackDirectOutboundTag)
	}
	return root, outbound, nil
}

func findTaggedOutbound(root map[string]any, tag string) (map[string]any, error) {
	outbounds, err := objectList(root, "outbounds", "sing-box outbounds")
	if err != nil {
		return nil, err
	}
	var matches []map[string]any
	for _, outbound := range outbounds {
		if value, _ := outbound["tag"].(string); value == tag {
			matches = append(matches, outbound)
		}
	}
	if len(matches) == 0 {
		return nil, nil
	}
	if len(matches) != 1 {
		return nil, fmt.Errorf("现有 sing-box 配置中 tag=%s 的出站数量为 %d，无法安全修改", tag, len(matches))
	}
	return matches[0], nil
}

func parseDirectOutbound(config []byte, _ string) (map[string]any, map[string]any, error) {
	root, err := parseDNSRoot(config)
	if err != nil {
		return nil, nil, err
	}
	outbounds, err := objectList(root, "outbounds", "sing-box outbounds")
	if err != nil {
		return nil, nil, err
	}
	var matches []map[string]any
	for _, outbound := range outbounds {
		if tag, _ := outbound["tag"].(string); tag == "direct" {
			matches = append(matches, outbound)
		}
	}
	if len(matches) != 1 {
		return nil, nil, fmt.Errorf("现有 sing-box 配置中 tag=direct 的出站数量为 %d，无法安全修改", len(matches))
	}
	if typeName, _ := matches[0]["type"].(string); typeName != "" && typeName != "direct" {
		return nil, nil, fmt.Errorf("现有 sing-box 配置中 tag=direct 的出站不是 direct")
	}
	return root, matches[0], nil
}

func singBoxStrategyValue(raw any) string {
	switch typed := raw.(type) {
	case map[string]any:
		value, _ := typed["strategy"].(string)
		return strings.TrimSpace(value)
	default:
		return ""
	}
}

func clearLeftoverResolverStrategy(root, outbound map[string]any) {
	clearOutboundDomainResolver(outbound)
	route, ok := root["route"].(map[string]any)
	if !ok {
		return
	}
	if _, exists := route["default_domain_resolver"]; exists {
		route["default_domain_resolver"] = clearDomainResolverStrategy(route["default_domain_resolver"])
	}
	setResolveRuleStrategy(root, "")
}

func setResolveRuleStrategy(root map[string]any, strategy string) bool {
	route, ok := root["route"].(map[string]any)
	if !ok {
		return false
	}
	rules, ok := route["rules"].([]any)
	if !ok {
		return false
	}
	updated := false
	for _, raw := range rules {
		rule, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if action, _ := rule["action"].(string); action != "resolve" {
			continue
		}
		if strategy == "" {
			delete(rule, "strategy")
		} else {
			rule["strategy"] = strategy
		}
		updated = true
	}
	return updated
}

func prependResolveRule(root map[string]any, strategy string) {
	route, ok := root["route"].(map[string]any)
	if !ok || route == nil {
		route = map[string]any{}
		root["route"] = route
	}
	rules, _ := route["rules"].([]any)
	route["rules"] = append([]any{map[string]any{
		"action": "resolve", "server": "local", "strategy": strategy,
	}}, rules...)
}

func ensureLocalDNSServer(root map[string]any) {
	dns, ok := root["dns"].(map[string]any)
	if !ok || dns == nil {
		root["dns"] = map[string]any{
			"servers": []any{map[string]any{"type": "local", "tag": "local"}},
		}
		return
	}
	servers, _ := dns["servers"].([]any)
	for _, raw := range servers {
		server, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if tag, _ := server["tag"].(string); tag == "local" {
			return
		}
	}
	dns["servers"] = append([]any{map[string]any{"type": "local", "tag": "local"}}, servers...)
}

func restoreSimplifiedRoute(root map[string]any) {
	route, ok := root["route"].(map[string]any)
	if !ok {
		return
	}
	if _, exists := route["default_domain_resolver"]; exists {
		return
	}
	rules, ok := route["rules"].([]any)
	if !ok {
		return
	}
	kept := make([]any, 0, len(rules))
	for _, raw := range rules {
		rule, ok := raw.(map[string]any)
		if !ok {
			kept = append(kept, raw)
			continue
		}
		if isBareResolveRule(rule) {
			continue
		}
		kept = append(kept, rule)
	}
	route["rules"] = kept
	if dnsIsOnlyLocal(root) {
		delete(root, "dns")
	}
}

func isBareResolveRule(rule map[string]any) bool {
	if action, _ := rule["action"].(string); action != "resolve" {
		return false
	}
	for key := range rule {
		if key != "action" && key != "server" && key != "strategy" {
			return false
		}
	}
	return true
}

func dnsIsOnlyLocal(root map[string]any) bool {
	dns, ok := root["dns"].(map[string]any)
	if !ok {
		return false
	}
	servers, ok := dns["servers"].([]any)
	if !ok || len(servers) != 1 {
		return false
	}
	server, ok := servers[0].(map[string]any)
	if !ok {
		return false
	}
	if tag, _ := server["tag"].(string); tag != "local" {
		return false
	}
	if serverType, _ := server["type"].(string); serverType != "local" {
		return false
	}
	for key := range dns {
		if key != "servers" {
			return false
		}
	}
	return true
}

func clearOutboundDomainResolver(outbound map[string]any) {
	raw, exists := outbound["domain_resolver"]
	if !exists {
		return
	}
	cleared := clearDomainResolverStrategy(raw)
	if cleared == nil {
		delete(outbound, "domain_resolver")
		return
	}
	if server, ok := cleared.(string); ok && server != "" {
		delete(outbound, "domain_resolver")
		return
	}
	outbound["domain_resolver"] = cleared
}

func clearDomainResolverStrategy(raw any) any {
	object, ok := raw.(map[string]any)
	if !ok {
		return raw
	}
	delete(object, "strategy")
	if len(object) == 0 {
		return nil
	}
	if len(object) == 1 {
		if server, ok := object["server"].(string); ok {
			return server
		}
	}
	return object
}

func domainResolverServer(raw any) string {
	switch typed := raw.(type) {
	case string:
		return strings.TrimSpace(typed)
	case map[string]any:
		server, _ := typed["server"].(string)
		return strings.TrimSpace(server)
	default:
		return ""
	}
}

func ensureFallbackDirectIsolation(root map[string]any) error {
	if !hasTaggedObject(root, "inbounds", fallbackGuardInboundTag) {
		return nil
	}
	route, err := childObject(root, "route", "sing-box route")
	if err != nil {
		return err
	}
	rules, err := objectList(route, "rules", "sing-box route rules")
	if err != nil {
		return err
	}
	var toMove []map[string]any
	for _, rule := range rules {
		if action, _ := rule["action"].(string); action != "route" {
			continue
		}
		if !stringListContains(rule["inbound"], fallbackGuardInboundTag) {
			continue
		}
		if outbound, _ := rule["outbound"].(string); outbound == "direct" {
			toMove = append(toMove, rule)
		}
	}
	if len(toMove) == 0 {
		return nil
	}
	if err := ensureFallbackDirectOutbound(root); err != nil {
		return err
	}
	for _, rule := range toMove {
		rule["outbound"] = fallbackDirectOutboundTag
	}
	return nil
}

func ensureFallbackDirectOutbound(root map[string]any) error {
	outbounds, err := objectList(root, "outbounds", "sing-box outbounds")
	if err != nil {
		return err
	}
	var existing []map[string]any
	for _, outbound := range outbounds {
		if tag, _ := outbound["tag"].(string); tag == fallbackDirectOutboundTag {
			existing = append(existing, outbound)
		}
	}
	if len(existing) > 1 {
		return fmt.Errorf("现有 sing-box 配置中 tag=%s 的出站数量为 %d，无法安全修改", fallbackDirectOutboundTag, len(existing))
	}
	if len(existing) == 1 {
		if typeName, _ := existing[0]["type"].(string); typeName != "" && typeName != "direct" {
			return fmt.Errorf("现有 sing-box 配置中 tag=%s 的出站不是 direct", fallbackDirectOutboundTag)
		}
		return nil
	}
	raw, _ := root["outbounds"].([]any)
	inserted := map[string]any{"type": "direct", "tag": fallbackDirectOutboundTag}
	index := len(raw)
	for i, item := range raw {
		object, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if tag, _ := object["tag"].(string); tag == "direct" {
			index = i + 1
			break
		}
	}
	root["outbounds"] = slices.Insert(raw, index, any(inserted))
	return nil
}

func hasTaggedObject(root map[string]any, key, tag string) bool {
	objects, err := objectList(root, key, "sing-box "+key)
	if err != nil {
		return false
	}
	count := 0
	for _, object := range objects {
		if value, _ := object["tag"].(string); value == tag {
			count++
		}
	}
	return count == 1
}

func isFallbackAllowOutbound(value any) bool {
	tag, _ := value.(string)
	return tag == "direct" || tag == fallbackDirectOutboundTag
}
