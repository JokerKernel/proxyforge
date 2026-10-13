package domain

import (
	"fmt"
	"net/netip"
	"strings"
)

const (
	AccessModeDefault   = "default"
	AccessModeWhitelist = "whitelist"
	AccessEntryDomain   = "domain"
	AccessEntryIP       = "ip"
	AccessEntryCIDR     = "cidr"
)

type AccessEntry struct {
	Type              string `json:"type"`
	Value             string `json:"value"`
	IncludeSubdomains bool   `json:"include_subdomains,omitempty"`
}

type AccessPolicy struct {
	Mode      string        `json:"mode,omitempty"`
	Whitelist []AccessEntry `json:"whitelist,omitempty"`
	Blacklist []AccessEntry `json:"blacklist,omitempty"`
}

func (p AccessPolicy) EffectiveMode() string {
	if p.Mode == "" {
		return AccessModeDefault
	}
	return p.Mode
}

func (p AccessPolicy) Active() bool {
	return len(p.Whitelist) > 0 || p.EffectiveMode() == AccessModeWhitelist || (p.BlacklistEnabled() && len(p.Blacklist) > 0)
}

func (p AccessPolicy) BlacklistEnabled() bool {
	return p.EffectiveMode() == AccessModeDefault
}

func (p AccessPolicy) ClearList(listName string) (AccessPolicy, error) {
	switch listName {
	case "white":
		p.Whitelist = nil
		if p.EffectiveMode() == AccessModeWhitelist {
			p.Mode = AccessModeDefault
		}
	case "black":
		p.Blacklist = nil
	default:
		return p, fmt.Errorf("名单类型无效（可选 white 或 black）")
	}
	return p, nil
}

// ParseAccessEntry accepts a destination, without URL scheme, path or port.
func ParseAccessEntry(value string, exact bool) (AccessEntry, error) {
	value = strings.TrimSpace(value)
	if ip, err := netip.ParseAddr(value); err == nil {
		if ip.Zone() != "" {
			return AccessEntry{}, fmt.Errorf("IP 不能包含网卡范围")
		}
		if exact {
			return AccessEntry{}, fmt.Errorf("--exact 仅适用于域名")
		}
		return AccessEntry{Type: AccessEntryIP, Value: ip.Unmap().String()}, nil
	}
	if strings.Contains(value, "/") {
		prefix, err := netip.ParsePrefix(value)
		if err != nil || prefix.Addr().Is4In6() {
			return AccessEntry{}, fmt.Errorf("网段格式无效，请使用例如 192.168.1.0/24")
		}
		if exact {
			return AccessEntry{}, fmt.Errorf("--exact 仅适用于域名")
		}
		return AccessEntry{Type: AccessEntryCIDR, Value: prefix.Masked().String()}, nil
	}
	value = strings.ToLower(strings.TrimSuffix(value, "."))
	if value == "" || len(value) > 253 || !strings.Contains(value, ".") {
		return AccessEntry{}, fmt.Errorf("请输入有效域名、IP 或 CIDR 网段")
	}
	for _, label := range strings.Split(value, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return AccessEntry{}, fmt.Errorf("域名格式无效")
		}
		for _, c := range label {
			if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
				return AccessEntry{}, fmt.Errorf("域名不能包含协议、端口、路径或通配符；国际域名请使用 Punycode")
			}
		}
	}
	// Dotted numeric strings that failed IP parsing must not silently become domains.
	numeric := true
	for _, c := range value {
		if !(c >= '0' && c <= '9' || c == '.') {
			numeric = false
		}
	}
	if numeric {
		return AccessEntry{}, fmt.Errorf("IP 地址格式无效")
	}
	return AccessEntry{Type: AccessEntryDomain, Value: value, IncludeSubdomains: !exact}, nil
}

func (p AccessPolicy) Normalize() (AccessPolicy, error) {
	p.Mode = strings.ToLower(strings.TrimSpace(p.Mode))
	if p.Mode != "" && p.Mode != AccessModeDefault && p.Mode != AccessModeWhitelist {
		return p, fmt.Errorf("访问模式无效（可选 default 或 whitelist）")
	}
	normalize := func(entries []AccessEntry) ([]AccessEntry, error) {
		var result []AccessEntry
		seen := map[AccessEntry]bool{}
		for _, entry := range entries {
			parsed, err := ParseAccessEntry(entry.Value, entry.Type == AccessEntryDomain && !entry.IncludeSubdomains)
			if err != nil {
				return nil, err
			}
			if parsed.Type != entry.Type || (parsed.Type != AccessEntryDomain && entry.IncludeSubdomains) {
				return nil, fmt.Errorf("访问规则类型与目标不一致: %s", entry.Value)
			}
			if !seen[parsed] {
				seen[parsed] = true
				result = append(result, parsed)
			}
		}
		return result, nil
	}
	var err error
	p.Whitelist, err = normalize(p.Whitelist)
	if err != nil {
		return p, err
	}
	p.Blacklist, err = normalize(p.Blacklist)
	return p, err
}
