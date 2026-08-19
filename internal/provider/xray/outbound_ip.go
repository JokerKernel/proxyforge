package xray

import (
	"encoding/json"
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
}

var xrayOutboundIPStrategy = map[string]string{
	provider.OutboundIPPreferIPv4: "UseIPv4v6",
	provider.OutboundIPPreferIPv6: "UseIPv6v4",
	provider.OutboundIPIPv4Only:   "ForceIPv4",
	provider.OutboundIPIPv6Only:   "ForceIPv6",
}

func (*Provider) OutboundIPStrategies() []string {
	return append([]string(nil), supportedOutboundIPStrategies...)
}

func (*Provider) CurrentOutboundIPStrategy(config []byte) (string, error) {
	_, outbound, err := parseDirectFreedomOutbound(config, "读取")
	if err != nil {
		return "", err
	}
	settings, _ := outbound["settings"].(map[string]any)
	strategy, _ := settings["domainStrategy"].(string)
	switch strings.TrimSpace(strategy) {
	case "", "AsIs", "UseIP":
		return provider.OutboundIPUnset, nil
	case "UseIPv4v6":
		return provider.OutboundIPPreferIPv4, nil
	case "UseIPv6v4":
		return provider.OutboundIPPreferIPv6, nil
	case "ForceIPv4":
		return provider.OutboundIPIPv4Only, nil
	case "ForceIPv6":
		return provider.OutboundIPIPv6Only, nil
	default:
		return "custom", nil
	}
}

func (*Provider) PatchOutboundIPStrategy(config []byte, strategy string) ([]byte, error) {
	strategy = strings.ToLower(strings.TrimSpace(strategy))
	if !slices.Contains(supportedOutboundIPStrategies, strategy) {
		return nil, fmt.Errorf("Xray 出站 IP 策略 %q 无效", strategy)
	}
	root, outbound, err := parseDirectFreedomOutbound(config, "修改")
	if err != nil {
		return nil, err
	}
	settings, ok := outbound["settings"].(map[string]any)
	if !ok || settings == nil {
		settings = map[string]any{}
		outbound["settings"] = settings
	}
	settings["domainStrategy"] = xrayOutboundIPStrategy[strategy]
	return marshalXray(root)
}

func parseDirectFreedomOutbound(config []byte, operation string) (map[string]any, map[string]any, error) {
	var root map[string]any
	if err := json.Unmarshal(config, &root); err != nil {
		return nil, nil, fmt.Errorf("%s现有 Xray 配置: %w", operation, err)
	}
	if root == nil {
		return nil, nil, fmt.Errorf("%s现有 Xray 配置: 顶层不是 JSON 对象", operation)
	}
	outbounds, err := xrayObjectList(root, "outbounds", "Xray outbounds")
	if err != nil {
		return nil, nil, err
	}
	var matches []map[string]any
	for _, outbound := range outbounds {
		if tag, _ := outbound["tag"].(string); tag != "direct" {
			continue
		}
		protocol, _ := outbound["protocol"].(string)
		if protocol != "" && protocol != "freedom" {
			return nil, nil, fmt.Errorf("现有 Xray 配置中 tag=direct 的出站不是 freedom")
		}
		matches = append(matches, outbound)
	}
	if len(matches) != 1 {
		return nil, nil, fmt.Errorf("现有 Xray 配置中 tag=direct 的 Freedom 出站数量为 %d，无法安全修改", len(matches))
	}
	return root, matches[0], nil
}
