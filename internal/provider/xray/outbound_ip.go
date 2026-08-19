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
	provider.OutboundIPUnset,
}

var xrayOutboundIPStrategy = map[string]string{
	provider.OutboundIPPreferIPv4: "UseIPv4v6",
	provider.OutboundIPPreferIPv6: "UseIPv6v4",
	provider.OutboundIPIPv4Only:   "ForceIPv4",
	provider.OutboundIPIPv6Only:   "ForceIPv6",
	provider.OutboundIPUnset:      "UseIP",
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
	if strategy == provider.OutboundIPUnset {
		applyFreedomHappyEyeballs(outbound)
	} else {
		settings["domainStrategy"] = xrayOutboundIPStrategy[strategy]
	}
	if err := ensureFallbackDirectIsolation(root); err != nil {
		return nil, err
	}
	return marshalXray(root)
}

func (*Provider) CurrentFallbackIPStrategy(config []byte) (string, error) {
	_, outbound, err := parseFallbackFreedomOutbound(config, "读取")
	if err != nil {
		return "", err
	}
	if outbound == nil {
		return provider.OutboundIPUnset, nil
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

func (*Provider) PatchFallbackIPStrategy(config []byte, strategy string) ([]byte, error) {
	strategy = strings.ToLower(strings.TrimSpace(strategy))
	if !slices.Contains(supportedOutboundIPStrategies, strategy) {
		return nil, fmt.Errorf("Xray 回落 IP 策略 %q 无效", strategy)
	}
	root, _, err := parseFallbackFreedomOutbound(config, "修改")
	if err != nil {
		return nil, err
	}
	if err := ensureFallbackDirectIsolation(root); err != nil {
		return nil, err
	}
	if err := ensureFallbackDirectOutbound(root); err != nil {
		return nil, err
	}
	outbound, err := xrayFindTaggedOutbound(root, fallbackDirectOutboundTag)
	if err != nil {
		return nil, err
	}
	if outbound == nil {
		return nil, fmt.Errorf("现有 Xray 配置中找不到 tag=%s 的 Freedom 出站", fallbackDirectOutboundTag)
	}
	settings, ok := outbound["settings"].(map[string]any)
	if !ok || settings == nil {
		settings = map[string]any{}
		outbound["settings"] = settings
	}
	if strategy == provider.OutboundIPUnset {
		applyFreedomHappyEyeballs(outbound)
	} else {
		settings["domainStrategy"] = xrayOutboundIPStrategy[strategy]
	}
	return marshalXray(root)
}

func parseFallbackFreedomOutbound(config []byte, operation string) (map[string]any, map[string]any, error) {
	root, err := parseXrayRoot(config, operation)
	if err != nil {
		return nil, nil, err
	}
	if !xrayHasTaggedObject(root, "inbounds", fallbackGuardInboundTag) {
		return nil, nil, fmt.Errorf("现有 Xray 配置未启用回落防偷跑，无法修改回落 IP")
	}
	outbound, err := xrayFindTaggedOutbound(root, fallbackDirectOutboundTag)
	if err != nil {
		return nil, nil, err
	}
	if outbound == nil {
		return root, nil, nil
	}
	if protocol, _ := outbound["protocol"].(string); protocol != "" && protocol != "freedom" {
		return nil, nil, fmt.Errorf("现有 Xray 配置中 tag=%s 的出站不是 freedom", fallbackDirectOutboundTag)
	}
	return root, outbound, nil
}

func parseXrayRoot(config []byte, operation string) (map[string]any, error) {
	var root map[string]any
	if err := json.Unmarshal(config, &root); err != nil {
		return nil, fmt.Errorf("%s现有 Xray 配置: %w", operation, err)
	}
	if root == nil {
		return nil, fmt.Errorf("%s现有 Xray 配置: 顶层不是 JSON 对象", operation)
	}
	return root, nil
}

func xrayFindTaggedOutbound(root map[string]any, tag string) (map[string]any, error) {
	outbounds, err := xrayObjectList(root, "outbounds", "Xray outbounds")
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
		return nil, fmt.Errorf("现有 Xray 配置中 tag=%s 的出站数量为 %d，无法安全修改", tag, len(matches))
	}
	return matches[0], nil
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

func ensureFallbackDirectIsolation(root map[string]any) error {
	if !xrayHasTaggedObject(root, "inbounds", fallbackGuardInboundTag) {
		return nil
	}
	routing, err := xrayChildObject(root, "routing", "Xray routing")
	if err != nil {
		return err
	}
	rules, err := xrayObjectList(routing, "rules", "Xray routing rules")
	if err != nil {
		return err
	}
	var toMove []map[string]any
	for _, rule := range rules {
		if !xrayStringListContains(rule["inboundTag"], fallbackGuardInboundTag) {
			continue
		}
		if _, hasDomain := rule["domain"]; !hasDomain {
			continue
		}
		if tag, _ := rule["outboundTag"].(string); tag == "direct" {
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
		rule["outboundTag"] = fallbackDirectOutboundTag
	}
	return nil
}

func ensureFallbackDirectOutbound(root map[string]any) error {
	outbounds, err := xrayObjectList(root, "outbounds", "Xray outbounds")
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
		return fmt.Errorf("现有 Xray 配置中 tag=%s 的出站数量为 %d，无法安全修改", fallbackDirectOutboundTag, len(existing))
	}
	if len(existing) == 1 {
		if protocol, _ := existing[0]["protocol"].(string); protocol != "" && protocol != "freedom" {
			return fmt.Errorf("现有 Xray 配置中 tag=%s 的出站不是 freedom", fallbackDirectOutboundTag)
		}
		return nil
	}
	raw, _ := root["outbounds"].([]any)
	inserted := map[string]any{
		"protocol":       "freedom",
		"settings":       map[string]any{"domainStrategy": "AsIs"},
		"tag":            fallbackDirectOutboundTag,
		"streamSettings": freedomHappyEyeballsStream(),
	}
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

func xrayHasTaggedObject(root map[string]any, key, tag string) bool {
	objects, err := xrayObjectList(root, key, "Xray "+key)
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

const xrayHappyEyeballsDelayMs = 300

func freedomHappyEyeballsStream() map[string]any {
	return map[string]any{
		"sockopt": map[string]any{
			"domainStrategy": "UseIP",
			"happyEyeballs": map[string]any{
				"tryDelayMs":     xrayHappyEyeballsDelayMs,
				"prioritizeIPv6": false,
			},
		},
	}
}

func applyFreedomHappyEyeballs(outbound map[string]any) {
	settings, ok := outbound["settings"].(map[string]any)
	if !ok || settings == nil {
		settings = map[string]any{}
		outbound["settings"] = settings
	}
	settings["domainStrategy"] = "AsIs"
	stream, _ := outbound["streamSettings"].(map[string]any)
	if stream == nil {
		stream = map[string]any{}
		outbound["streamSettings"] = stream
	}
	sockopt, _ := stream["sockopt"].(map[string]any)
	if sockopt == nil {
		sockopt = map[string]any{}
		stream["sockopt"] = sockopt
	}
	sockopt["domainStrategy"] = "UseIP"
	happy, _ := sockopt["happyEyeballs"].(map[string]any)
	if happy == nil {
		happy = map[string]any{}
		sockopt["happyEyeballs"] = happy
	}
	happy["tryDelayMs"] = xrayHappyEyeballsDelayMs
	happy["prioritizeIPv6"] = false
}

func xrayIsFallbackAllowOutbound(value any) bool {
	tag, _ := value.(string)
	return tag == "direct" || tag == fallbackDirectOutboundTag
}
