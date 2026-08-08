package singbox

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"proxyforge/internal/provider"
	"proxyforge/internal/provider/jsonutil"
)

var supportedDNSProfiles = []string{
	provider.DNSProfileSystem,
	provider.DNSProfilePublicCloudflare,
	provider.DNSProfilePublicGoogle,
}

func (*Provider) DNSProfiles() []string {
	return append([]string(nil), supportedDNSProfiles...)
}

func (*Provider) CurrentDNSProfile(config []byte) (string, error) {
	root, err := parseDNSRoot(config)
	if err != nil {
		return "", err
	}
	dns, ok := root["dns"].(map[string]any)
	if !ok {
		if _, exists := root["dns"]; exists {
			return "", fmt.Errorf("现有 sing-box dns 不是对象")
		}
		return "none", nil
	}
	servers, ok := dns["servers"].([]any)
	if !ok {
		return "custom", nil
	}
	profile, expectedTag := singDNSProfile(servers)
	if profile == "custom" {
		return "custom", nil
	}
	if rawFinal, exists := dns["final"]; exists {
		final, ok := rawFinal.(string)
		if !ok || (final != "" && final != expectedTag) {
			return "custom", nil
		}
	}
	route, ok := root["route"].(map[string]any)
	if !ok {
		return "custom", nil
	}
	if resolver, _ := route["default_domain_resolver"].(string); resolver != expectedTag {
		return "custom", nil
	}
	rules, ok := route["rules"].([]any)
	if !ok {
		return "custom", nil
	}
	foundResolve := false
	for _, rawRule := range rules {
		rule, ok := rawRule.(map[string]any)
		if !ok {
			continue
		}
		if action, _ := rule["action"].(string); action == "resolve" {
			foundResolve = true
			if resolver, _ := rule["server"].(string); resolver != expectedTag {
				return "custom", nil
			}
		}
	}
	if !foundResolve {
		return "custom", nil
	}
	return profile, nil
}

func (*Provider) PatchDNSProfile(config []byte, profile string) ([]byte, error) {
	profile = strings.ToLower(strings.TrimSpace(profile))
	if !slices.Contains(supportedDNSProfiles, profile) {
		return nil, fmt.Errorf("sing-box DNS 配置 %q 无效", profile)
	}
	root, err := parseDNSRoot(config)
	if err != nil {
		return nil, err
	}
	dns, exists := root["dns"].(map[string]any)
	if !exists {
		if raw, present := root["dns"]; present && raw != nil {
			return nil, fmt.Errorf("现有 sing-box dns 不是对象")
		}
		dns = make(map[string]any)
		root["dns"] = dns
	}

	tag := "local"
	servers := []any{map[string]any{"type": "local", "tag": tag}}
	if profile == provider.DNSProfilePublicCloudflare {
		tag = "cloudflare"
		servers = []any{
			map[string]any{"type": "udp", "tag": "cloudflare", "server": "1.1.1.1", "server_port": 53},
			map[string]any{"type": "udp", "tag": "google", "server": "8.8.8.8", "server_port": 53},
		}
	} else if profile == provider.DNSProfilePublicGoogle {
		tag = "google"
		servers = []any{
			map[string]any{"type": "udp", "tag": "google", "server": "8.8.8.8", "server_port": 53},
			map[string]any{"type": "udp", "tag": "cloudflare", "server": "1.1.1.1", "server_port": 53},
		}
	}
	dns["servers"] = servers
	dns["final"] = tag

	route, ok := root["route"].(map[string]any)
	if !ok {
		if _, exists := root["route"]; exists {
			return nil, fmt.Errorf("现有 sing-box route 不是对象")
		}
		route = make(map[string]any)
		root["route"] = route
	}
	route["default_domain_resolver"] = tag
	rules, exists := route["rules"].([]any)
	if !exists {
		if _, present := route["rules"]; present {
			return nil, fmt.Errorf("现有 sing-box route.rules 不是数组")
		}
		rules = []any{}
	}
	updatedResolve := false
	for _, rawRule := range rules {
		rule, ok := rawRule.(map[string]any)
		if !ok {
			continue
		}
		if action, _ := rule["action"].(string); action == "resolve" {
			rule["server"] = tag
			updatedResolve = true
		}
	}
	if !updatedResolve {
		rules = append([]any{map[string]any{"action": "resolve", "server": tag}}, rules...)
	}
	route["rules"] = rules
	return jsonutil.Marshal(root)
}

func singDNSProfile(servers []any) (string, string) {
	if len(servers) == 1 {
		server, ok := servers[0].(map[string]any)
		if !ok {
			return "custom", ""
		}
		serverType, _ := server["type"].(string)
		tag, _ := server["tag"].(string)
		address, _ := server["server"].(string)
		switch {
		case serverType == "local" && tag == "local":
			return provider.DNSProfileSystem, "local"
		case serverType == "udp" && tag == "cloudflare" && address == "1.1.1.1":
			return provider.DNSProfileCloudflare, "cloudflare"
		case serverType == "udp" && tag == "google" && address == "8.8.8.8":
			return provider.DNSProfileGoogle, "google"
		}
	}
	if len(servers) == 2 && singDNSServerMatches(servers[0], "cloudflare", "1.1.1.1") && singDNSServerMatches(servers[1], "google", "8.8.8.8") {
		return provider.DNSProfilePublicCloudflare, "cloudflare"
	}
	if len(servers) == 2 && singDNSServerMatches(servers[0], "google", "8.8.8.8") && singDNSServerMatches(servers[1], "cloudflare", "1.1.1.1") {
		return provider.DNSProfilePublicGoogle, "google"
	}
	return "custom", ""
}

func singDNSServerMatches(raw any, tag, address string) bool {
	server, ok := raw.(map[string]any)
	if !ok {
		return false
	}
	serverType, _ := server["type"].(string)
	serverTag, _ := server["tag"].(string)
	serverAddress, _ := server["server"].(string)
	return serverType == "udp" && serverTag == tag && serverAddress == address
}

func parseDNSRoot(config []byte) (map[string]any, error) {
	var root map[string]any
	if err := json.Unmarshal(config, &root); err != nil {
		return nil, fmt.Errorf("解析现有 sing-box 配置: %w", err)
	}
	if root == nil {
		return nil, fmt.Errorf("解析现有 sing-box 配置: 顶层不是 JSON 对象")
	}
	return root, nil
}
