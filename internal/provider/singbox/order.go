package singbox

import "proxyforge/internal/provider/jsonutil"

// marshalSingBox follows the field order used by the official sing-box
// configuration documentation. Unknown manual fields are retained and sorted
// after documented fields.
func marshalSingBox(value any) ([]byte, error) {
	return jsonutil.MarshalOrdered(value, singBoxFieldOrder)
}

func singBoxFieldOrder(object map[string]any) []string {
	order := []string(nil)
	typeName, _ := object["type"].(string)
	switch {
	case hasAnySingBoxKey(object, "inbounds", "outbounds") && hasAnySingBoxKey(object, "log", "dns", "route"):
		order = []string{
			"log", "dns", "ntp", "certificate", "certificate_providers", "http_clients", "network_namespaces",
			"endpoints", "inbounds", "outbounds", "route", "services", "experimental",
		}
	case hasSingBoxKeys(object, "level", "timestamp") || hasSingBoxKeys(object, "disabled", "level"):
		order = []string{"disabled", "level", "output", "timestamp"}
	case hasSingBoxKeys(object, "servers") && !hasSingBoxKeys(object, "server", "server_port"):
		order = []string{
			"servers", "rules", "final", "strategy", "disable_cache", "disable_expire", "independent_cache",
			"cache_capacity", "optimistic", "timeout", "reverse_mapping", "client_subnet", "fakeip",
		}
	case typeName == "direct" && hasAnySingBoxKey(object, "listen", "listen_port", "override_address"):
		order = append([]string{"type", "tag"}, singBoxListenFieldOrder()...)
		order = append(order, "network", "override_address", "override_port")
	case typeName == "direct":
		order = []string{"type", "tag", "domain_resolver", "domain_strategy"}
	case typeName == "vless" && hasSingBoxKeys(object, "listen", "users"):
		order = append([]string{"type", "tag"}, singBoxListenFieldOrder()...)
		order = append(order, "users", "tls", "multiplex", "transport")
	case hasSingBoxKeys(object, "type", "tag", "listen"):
		order = append([]string{"type", "tag"}, singBoxListenFieldOrder()...)
	case typeName == "vless" && hasSingBoxKeys(object, "server", "server_port", "uuid"):
		order = []string{"type", "tag", "server", "server_port", "uuid", "flow", "network", "tls", "packet_encoding", "multiplex", "transport"}
	case hasSingBoxKeys(object, "type", "tag", "server"):
		order = []string{"type", "tag", "server", "server_port", "path", "headers", "tls", "domain_resolver", "domain_strategy"}
	case hasSingBoxKeys(object, "type", "tag"):
		order = []string{"type", "tag"}
	case hasSingBoxKeys(object, "name", "uuid"):
		order = []string{"name", "uuid", "flow"}
	case hasSingBoxKeys(object, "enabled", "server_name"):
		order = []string{
			"enabled", "engine", "disable_sni", "server_name", "insecure", "alpn", "min_version", "max_version",
			"cipher_suites", "curve_preferences", "certificate", "certificate_path", "certificate_public_key_sha256",
			"client_certificate", "client_certificate_path", "client_key", "client_key_path", "key", "key_path",
			"kernel_tx", "kernel_rx", "handshake_timeout", "certificate_provider", "ech", "utls", "reality",
		}
	case hasSingBoxKeys(object, "enabled", "handshake", "private_key"):
		order = []string{"enabled", "handshake", "private_key", "short_id", "max_time_difference"}
	case hasSingBoxKeys(object, "enabled", "public_key", "short_id"):
		order = []string{"enabled", "public_key", "short_id"}
	case hasSingBoxKeys(object, "enabled", "fingerprint"):
		order = []string{"enabled", "fingerprint"}
	case hasSingBoxKeys(object, "server", "server_port"):
		order = append([]string{"server", "server_port"}, singBoxDialFieldOrder()...)
	case hasSingBoxKeys(object, "rules") && hasAnySingBoxKey(object, "final", "default_domain_resolver"):
		order = []string{
			"rules", "rule_set", "final", "auto_detect_interface", "override_android_vpn", "default_interface", "default_mark",
			"find_process", "find_neighbor", "dhcp_lease_files", "default_http_client", "default_domain_resolver",
			"default_network_strategy", "default_network_type", "default_fallback_network_type", "default_fallback_delay",
		}
	case hasSingBoxKeys(object, "action"):
		order = []string{
			"inbound", "ip_version", "auth_user", "protocol", "client", "network", "domain", "domain_suffix",
			"domain_keyword", "domain_regex", "source_ip_cidr", "source_ip_is_private", "ip_is_private", "ip_cidr",
			"port", "port_range", "source_port", "source_port_range", "rule_set", "invert", "action", "outbound", "server", "strategy",
		}
	case hasSingBoxKeys(object, "server") && hasAnySingBoxKey(object, "strategy", "rewrite_ttl", "client_subnet") && !hasAnySingBoxKey(object, "server_port", "type"):
		order = []string{"server", "strategy", "rewrite_ttl", "client_subnet"}
	}
	return order
}

func singBoxListenFieldOrder() []string {
	return []string{
		"listen", "listen_port", "bind_interface", "routing_mark", "reuse_addr", "netns", "tcp_fast_open", "tcp_multi_path",
		"disable_tcp_keep_alive", "tcp_keep_alive", "tcp_keep_alive_interval", "udp_fragment", "udp_timeout", "detour",
	}
}

func singBoxDialFieldOrder() []string {
	return []string{
		"detour", "bind_interface", "inet4_bind_address", "inet6_bind_address", "routing_mark", "reuse_addr", "netns",
		"connect_timeout", "tcp_fast_open", "tcp_multi_path", "disable_tcp_keep_alive", "tcp_keep_alive",
		"tcp_keep_alive_interval", "udp_fragment", "domain_resolver", "domain_strategy",
	}
}

func hasSingBoxKeys(object map[string]any, keys ...string) bool {
	for _, key := range keys {
		if _, ok := object[key]; !ok {
			return false
		}
	}
	return true
}

func hasAnySingBoxKey(object map[string]any, keys ...string) bool {
	for _, key := range keys {
		if _, ok := object[key]; ok {
			return true
		}
	}
	return false
}
