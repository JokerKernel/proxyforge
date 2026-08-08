package xray

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"proxyforge/internal/provider"
	"proxyforge/internal/provider/jsonutil"
)

var supportedDNSProfiles = []string{provider.DNSProfileSystem, provider.DNSProfileCloudflare, provider.DNSProfileGoogle}

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
			return "", fmt.Errorf("现有 Xray dns 不是对象")
		}
		return "implicit-system", nil
	}
	servers, ok := dns["servers"].([]any)
	if !ok || len(servers) != 1 {
		return "custom", nil
	}
	address, ok := dnsServerAddress(servers[0])
	if !ok {
		return "custom", nil
	}
	if strategy, _ := dns["queryStrategy"].(string); strategy != "UseIP" {
		return "custom", nil
	}
	switch address {
	case "localhost":
		return provider.DNSProfileSystem, nil
	case "1.1.1.1":
		return provider.DNSProfileCloudflare, nil
	case "8.8.8.8":
		return provider.DNSProfileGoogle, nil
	default:
		return "custom", nil
	}
}

func (*Provider) PatchDNSProfile(config []byte, profile string) ([]byte, error) {
	profile = strings.ToLower(strings.TrimSpace(profile))
	if !slices.Contains(supportedDNSProfiles, profile) {
		return nil, fmt.Errorf("Xray DNS 配置 %q 无效", profile)
	}
	root, err := parseDNSRoot(config)
	if err != nil {
		return nil, err
	}
	dns, exists := root["dns"].(map[string]any)
	if !exists {
		if raw, present := root["dns"]; present && raw != nil {
			return nil, fmt.Errorf("现有 Xray dns 不是对象")
		}
		dns = make(map[string]any)
		root["dns"] = dns
	}
	address := "localhost"
	if profile == provider.DNSProfileCloudflare {
		address = "1.1.1.1"
	} else if profile == provider.DNSProfileGoogle {
		address = "8.8.8.8"
	}
	dns["servers"] = []any{address}
	dns["queryStrategy"] = "UseIP"
	return jsonutil.Marshal(root)
}

func dnsServerAddress(value any) (string, bool) {
	if address, ok := value.(string); ok {
		return address, true
	}
	server, ok := value.(map[string]any)
	if !ok {
		return "", false
	}
	address, ok := server["address"].(string)
	return address, ok
}

func parseDNSRoot(config []byte) (map[string]any, error) {
	var root map[string]any
	if err := json.Unmarshal(config, &root); err != nil {
		return nil, fmt.Errorf("解析现有 Xray 配置: %w", err)
	}
	if root == nil {
		return nil, fmt.Errorf("解析现有 Xray 配置: 顶层不是 JSON 对象")
	}
	return root, nil
}
