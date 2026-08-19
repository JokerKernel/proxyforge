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
		return marshalSingBox(root)
	}

	value := singBoxOutboundIPStrategy[strategy]
	if setResolveRuleStrategy(root, value) {
		return marshalSingBox(root)
	}
	outbound["domain_resolver"] = setDomainResolverStrategy(outbound["domain_resolver"], "local", value)
	return marshalSingBox(root)
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

func setDomainResolverStrategy(raw any, fallbackServer, strategy string) map[string]any {
	server := domainResolverServer(raw)
	if server == "" {
		server = fallbackServer
	}
	resolver := map[string]any{"server": server, "strategy": strategy}
	if object, ok := raw.(map[string]any); ok {
		for key, value := range object {
			if key == "server" || key == "strategy" {
				continue
			}
			resolver[key] = value
		}
	}
	return resolver
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
