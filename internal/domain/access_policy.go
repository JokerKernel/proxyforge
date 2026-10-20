package domain

import (
	"fmt"
	"net/netip"
	"sort"
	"strconv"
	"strings"
	"unicode"
)

const (
	AccessModeAuto           = "auto"
	AccessModeDefault        = "default"
	AccessModeWhitelist      = "whitelist"
	AccessEntryDomain        = "domain"
	AccessEntryIP            = "ip"
	AccessEntryCIDR          = "cidr"
	DefaultAccessDomainPorts = "80,443"
)

type AccessEntry struct {
	Type              string `json:"type"`
	Value             string `json:"value"`
	IncludeSubdomains bool   `json:"include_subdomains,omitempty"`
	Ports             string `json:"ports,omitempty"`
}

type AccessPolicy struct {
	Disabled  bool          `json:"disabled,omitempty"`
	Mode      string        `json:"mode,omitempty"`
	Whitelist []AccessEntry `json:"whitelist,omitempty"`
	Blacklist []AccessEntry `json:"blacklist,omitempty"`
}

// NormalizeAccessPorts canonicalizes destination ports; empty means all ports.
func NormalizeAccessPorts(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" || strings.EqualFold(value, "all") {
		return "", nil
	}
	type interval struct{ first, last int }
	var ranges []interval
	parse := func(raw string) (int, error) {
		raw = strings.TrimSpace(raw)
		if raw == "" || strings.IndexFunc(raw, func(r rune) bool { return r < '0' || r > '9' }) >= 0 {
			return 0, fmt.Errorf("端口必须为 1–65535 的整数")
		}
		port, err := strconv.Atoi(raw)
		if err != nil || port < 1 || port > 65535 {
			return 0, fmt.Errorf("端口必须为 1–65535 的整数")
		}
		return port, nil
	}
	for _, part := range strings.Split(strings.NewReplacer("，", ",", ".", ",").Replace(value), ",") {
		bounds := strings.Split(part, "-")
		if len(bounds) > 2 {
			return "", fmt.Errorf("端口格式无效，请使用例如 80,443,8000-8100")
		}
		first, err := parse(bounds[0])
		if err != nil {
			return "", err
		}
		last := first
		if len(bounds) == 2 {
			last, err = parse(bounds[1])
			if err != nil {
				return "", err
			}
			if last < first {
				return "", fmt.Errorf("端口范围起点不能大于终点")
			}
		}
		ranges = append(ranges, interval{first, last})
	}
	sort.Slice(ranges, func(i, j int) bool { return ranges[i].first < ranges[j].first })
	merged := ranges[:0]
	for _, current := range ranges {
		if len(merged) == 0 || current.first > merged[len(merged)-1].last+1 {
			merged = append(merged, current)
		} else if current.last > merged[len(merged)-1].last {
			merged[len(merged)-1].last = current.last
		}
	}
	parts := make([]string, 0, len(merged))
	for _, span := range merged {
		part := strconv.Itoa(span.first)
		if span.first != span.last {
			part += "-" + strconv.Itoa(span.last)
		}
		parts = append(parts, part)
	}
	return strings.Join(parts, ","), nil
}

func (p AccessPolicy) EffectiveMode() string {
	if p.Mode == "" {
		return AccessModeDefault
	}
	return p.Mode
}

func (p AccessPolicy) Active() bool {
	if p.Disabled {
		return false
	}
	return len(p.Whitelist) > 0 || p.EffectiveMode() == AccessModeWhitelist || (p.BlacklistEnabled() && len(p.Blacklist) > 0)
}

func (p AccessPolicy) BlacklistEnabled() bool {
	return !p.Disabled && p.EffectiveMode() == AccessModeDefault
}

// Auto is a command, not a persisted mode. With no whitelist it restores the
// unset state; otherwise it selects whitelist mode, which stays closed on clear.
func (p AccessPolicy) SelectMode(mode string) (AccessPolicy, error) {
	switch mode {
	case AccessModeAuto:
		p.Mode = ""
		if len(p.Whitelist) > 0 {
			p.Mode = AccessModeWhitelist
		}
	case AccessModeDefault, AccessModeWhitelist:
		p.Mode = mode
	default:
		return p, fmt.Errorf("访问模式无效（可选 auto、default 或 whitelist）")
	}
	return p, nil
}

func (p AccessPolicy) AddEntries(listName string, entries []AccessEntry) (AccessPolicy, error) {
	switch listName {
	case "white":
		// Only a new first whitelist chooses the mode automatically. Explicit
		// modes and existing lists keep their behavior until the user changes it.
		if p.Mode == "" && len(p.Whitelist) == 0 && len(entries) > 0 {
			p.Mode = AccessModeWhitelist
		}
		p.Whitelist = append(p.Whitelist, entries...)
	case "black":
		p.Blacklist = append(p.Blacklist, entries...)
	default:
		return p, fmt.Errorf("名单类型无效（可选 white 或 black）")
	}
	return p, nil
}

func (p AccessPolicy) ClearList(listName string) (AccessPolicy, error) {
	switch listName {
	case "white":
		p.Whitelist = nil
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

// ParseAccessEntries validates a whole batch before returning any entries.
// Exact matching applies to domains; IPs and networks retain their own semantics.
func ParseAccessEntries(value string, exact bool) ([]AccessEntry, error) {
	values := strings.FieldsFunc(value, func(r rune) bool {
		return unicode.IsSpace(r) || r == ',' || r == '，'
	})
	if len(values) == 0 {
		return nil, fmt.Errorf("请输入至少一个域名、IP 或 CIDR 网段")
	}
	var entries []AccessEntry
	seen := map[AccessEntry]bool{}
	for i, value := range values {
		entry, err := ParseAccessEntry(value, false)
		if err != nil {
			return nil, fmt.Errorf("第 %d 个目标 %q 无效: %w", i+1, value, err)
		}
		if entry.Type == AccessEntryDomain {
			entry.IncludeSubdomains = !exact
		}
		if !seen[entry] {
			entries = append(entries, entry)
			seen[entry] = true
		}
	}
	return entries, nil
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
			parsed.Ports, err = NormalizeAccessPorts(entry.Ports)
			if err != nil {
				return nil, fmt.Errorf("%s 的端口无效: %w", entry.Value, err)
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
