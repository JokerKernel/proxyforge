package xray

import (
	"bytes"
	"encoding/json"
	"sort"

	"proxyforge/internal/provider/jsonutil"
)

// marshalXray keeps Xray's documented field order for managed objects. Unknown
// fields are retained and sorted after the documented fields, so targeted
// updates remain readable without dropping manual configuration.
func marshalXray(value any) ([]byte, error) {
	normalized, err := normalizeXrayValue(value)
	if err != nil {
		return nil, err
	}
	return jsonutil.Marshal(orderXrayValue(normalized))
}

func normalizeXrayValue(value any) (any, error) {
	if _, ok := value.(map[string]any); ok {
		return value, nil
	}
	if _, ok := value.([]any); ok {
		return value, nil
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	var generic any
	if err := json.Unmarshal(raw, &generic); err != nil {
		return nil, err
	}
	return generic, nil
}

type orderedXrayObject map[string]any

func orderXrayValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		return orderedXrayObject(typed)
	case []any:
		ordered := make([]any, len(typed))
		for index, item := range typed {
			ordered[index] = orderXrayValue(item)
		}
		return ordered
	default:
		return value
	}
}

func (object orderedXrayObject) MarshalJSON() ([]byte, error) {
	keys := make([]string, 0, len(object))
	for key := range object {
		keys = append(keys, key)
	}
	priority := xrayFieldPriority(map[string]any(object))
	sort.Slice(keys, func(i, j int) bool {
		left, leftOK := priority[keys[i]]
		right, rightOK := priority[keys[j]]
		switch {
		case leftOK && rightOK:
			return left < right
		case leftOK:
			return true
		case rightOK:
			return false
		default:
			return keys[i] < keys[j]
		}
	})

	var output bytes.Buffer
	output.WriteByte('{')
	for index, key := range keys {
		if index > 0 {
			output.WriteByte(',')
		}
		encodedKey, err := marshalXrayJSONValue(key)
		if err != nil {
			return nil, err
		}
		encodedValue, err := marshalXrayJSONValue(orderXrayValue(object[key]))
		if err != nil {
			return nil, err
		}
		output.Write(encodedKey)
		output.WriteByte(':')
		output.Write(encodedValue)
	}
	output.WriteByte('}')
	return output.Bytes(), nil
}

func marshalXrayJSONValue(value any) ([]byte, error) {
	var output bytes.Buffer
	encoder := json.NewEncoder(&output)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(output.Bytes(), []byte{'\n'}), nil
}

func xrayFieldPriority(object map[string]any) map[string]int {
	order := []string(nil)
	switch {
	case hasAnyXrayKey(object, "inbounds", "outbounds") && hasAnyXrayKey(object, "log", "dns", "routing"):
		order = []string{"log", "dns", "inbounds", "outbounds", "routing"}
	case hasAnyXrayKey(object, "tryDelayMs", "prioritizeIPv6") && !hasAnyXrayKey(object, "domainStrategy", "rules", "protocol"):
		order = []string{"tryDelayMs", "prioritizeIPv6", "interleave", "maxConcurrentTry"}
	case object["protocol"] == "dokodemo-door":
		order = []string{"listen", "port", "protocol", "settings", "tag", "sniffing"}
	case hasXrayKeys(object, "listen", "protocol", "settings"):
		order = []string{"listen", "port", "protocol", "settings", "streamSettings", "tag", "sniffing"}
	case hasXrayKeys(object, "network", "security", "realitySettings"):
		order = []string{"network", "security", "realitySettings"}
	case hasXrayKeys(object, "show", "target", "serverNames", "privateKey", "shortIds"):
		order = []string{"show", "target", "xver", "serverNames", "privateKey", "shortIds"}
	case hasXrayKeys(object, "serverName", "fingerprint", "password", "shortId"):
		order = []string{"serverName", "fingerprint", "password", "shortId", "spiderX"}
	case hasXrayKeys(object, "enabled", "destOverride"):
		order = []string{"enabled", "destOverride", "metadataOnly", "domainsExcluded", "ipsExcluded", "routeOnly"}
	case hasXrayKeys(object, "address", "port", "network"):
		order = []string{"address", "port", "network"}
	case hasXrayKeys(object, "clients", "decryption"):
		order = []string{"clients", "flow", "decryption", "fallbacks"}
	case hasXrayKeys(object, "id", "flow"):
		order = []string{"id", "level", "email", "encryption", "flow"}
	case hasXrayKeys(object, "address", "port", "users"):
		order = []string{"address", "port", "users"}
	case hasXrayKeys(object, "protocol", "settings", "tag"):
		order = []string{"protocol", "settings", "tag", "streamSettings", "proxySettings", "mux", "targetStrategy"}
	case hasXrayKeys(object, "sockopt") && !hasAnyXrayKey(object, "network", "security"):
		order = []string{"sockopt"}
	case hasXrayKeys(object, "domainStrategy", "happyEyeballs"):
		order = []string{"domainStrategy", "happyEyeballs"}
	case hasXrayKeys(object, "domainStrategy", "finalRules"):
		order = []string{"domainStrategy", "redirect", "userLevel", "fragment", "noises", "proxyProtocol", "targetStrategy", "finalRules"}
	case object["action"] == "allow" || object["action"] == "block":
		order = []string{"action", "network", "port", "ip", "blockDelay"}
	case hasXrayKeys(object, "domainStrategy", "rules"):
		order = []string{"domainStrategy", "rules"}
	case hasXrayKeys(object, "type", "outboundTag"):
		order = []string{"type", "inboundTag", "domain", "ip", "outboundTag"}
	case hasXrayKeys(object, "servers", "queryStrategy"):
		order = []string{"servers", "queryStrategy"}
	}
	priority := make(map[string]int, len(order))
	for index, key := range order {
		priority[key] = index
	}
	return priority
}

func hasXrayKeys(object map[string]any, keys ...string) bool {
	for _, key := range keys {
		if _, ok := object[key]; !ok {
			return false
		}
	}
	return true
}

func hasAnyXrayKey(object map[string]any, keys ...string) bool {
	for _, key := range keys {
		if _, ok := object[key]; ok {
			return true
		}
	}
	return false
}
