package xray

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"proxyforge/internal/docker/model"
)

var supportedDNSProfiles = []string{
	model.DNSProfileSystem,
	model.DNSProfilePublicCloudflare,
	model.DNSProfilePublicGoogle,
	model.DNSProfileDoHCloudflare,
	model.DNSProfileDoHGoogle,
}

func (*Codec) DNSProfiles() []string {
	return append([]string(nil), supportedDNSProfiles...)
}

func (*Codec) CurrentDNSProfile(config []byte) (string, error) {
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
	if !ok {
		return "custom", nil
	}
	if strategy, _ := dns["queryStrategy"].(string); strategy != "UseIP" {
		return "custom", nil
	}
	if len(servers) == 2 {
		first, firstOK := dnsServerAddress(servers[0])
		second, secondOK := dnsServerAddress(servers[1])
		if firstOK && secondOK && first == "1.1.1.1" && second == "8.8.8.8" {
			return model.DNSProfilePublicCloudflare, nil
		}
		if firstOK && secondOK && first == "8.8.8.8" && second == "1.1.1.1" {
			return model.DNSProfilePublicGoogle, nil
		}
		if firstOK && secondOK && first == "https+local://1.1.1.1/dns-query" && second == "https+local://8.8.8.8/dns-query" {
			return model.DNSProfileDoHCloudflare, nil
		}
		if firstOK && secondOK && first == "https+local://8.8.8.8/dns-query" && second == "https+local://1.1.1.1/dns-query" {
			return model.DNSProfileDoHGoogle, nil
		}
	}
	if len(servers) == 1 {
		address, ok := dnsServerAddress(servers[0])
		if !ok {
			return "custom", nil
		}
		switch address {
		case "localhost":
			return model.DNSProfileSystem, nil
		case "1.1.1.1":
			return model.DNSProfileCloudflare, nil
		case "8.8.8.8":
			return model.DNSProfileGoogle, nil
		}
	}
	return "custom", nil
}

func (*Codec) CurrentDNSServers(config []byte) ([]string, error) {
	root, err := parseDNSRoot(config)
	if err != nil {
		return nil, err
	}
	dns, ok := root["dns"].(map[string]any)
	if !ok {
		if _, exists := root["dns"]; exists {
			return nil, fmt.Errorf("现有 Xray dns 不是对象")
		}
		return []string{"localhost"}, nil
	}
	servers, ok := dns["servers"].([]any)
	if !ok {
		return nil, nil
	}
	var addrs []string
	seen := make(map[string]struct{})
	for _, raw := range servers {
		address, ok := dnsServerAddress(raw)
		if !ok {
			continue
		}
		address = dnsDisplayAddress(address)
		if address == "" {
			continue
		}
		if _, exists := seen[address]; exists {
			continue
		}
		seen[address] = struct{}{}
		addrs = append(addrs, address)
	}
	return addrs, nil
}

func dnsDisplayAddress(address string) string {
	address = strings.TrimSpace(address)
	if i := strings.Index(address, "://"); i >= 0 {
		address = address[i+3:]
	}
	if i := strings.IndexAny(address, "/"); i >= 0 {
		address = address[:i]
	}
	return strings.TrimSpace(address)
}

func (*Codec) PatchDNSProfile(config []byte, profile string) ([]byte, error) {
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
	servers := []any{"localhost"}
	if profile == model.DNSProfilePublicCloudflare {
		servers = []any{"1.1.1.1", "8.8.8.8"}
	} else if profile == model.DNSProfilePublicGoogle {
		servers = []any{"8.8.8.8", "1.1.1.1"}
	} else if profile == model.DNSProfileDoHCloudflare {
		servers = []any{"https+local://1.1.1.1/dns-query", "https+local://8.8.8.8/dns-query"}
	} else if profile == model.DNSProfileDoHGoogle {
		servers = []any{"https+local://8.8.8.8/dns-query", "https+local://1.1.1.1/dns-query"}
	}
	dns["servers"] = servers
	dns["queryStrategy"] = "UseIP"
	return marshalXray(root)
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
