// Package accesspolicy compiles the Docker instance's destination rules.
package accesspolicy

import (
	"encoding/json"
	"fmt"
	"strings"

	"proxyforge/internal/docker/model"
)

const Prefix = "access-"
const directTag = Prefix + "direct"
const denyTag = Prefix + "deny"

func object(v any) map[string]any { m, _ := v.(map[string]any); return m }
func list(v any) []any {
	switch a := v.(type) {
	case []any:
		return a
	case []string:
		r := make([]any, len(a))
		for i, s := range a {
			r[i] = s
		}
		return r
	}
	return nil
}
func str(v any) string { s, _ := v.(string); return s }
func owned(r map[string]any) bool {
	t := str(r["ruleTag"])
	return t == "white-domain" || t == "white-ip" || t == "black-domain" || t == "black-ip" || t == Prefix+"final" || strings.HasPrefix(t, Prefix+"white-relay-")
}
func proxyTags(n model.Node) []string {
	r := []string{n.InboundTag}
	for _, a := range n.LandingAccesses {
		if a.Enabled && !a.UsesPrimaryInbound() {
			r = append(r, model.LandingTLSInboundTag(a.Name))
		}
	}
	return r
}
func values(entries []model.AccessEntry) map[string]any {
	domains, ips := []string{}, []string{}
	for _, e := range entries {
		if e.Type == model.AccessEntryDomain {
			prefix := "full:"
			if e.IncludeSubdomains {
				prefix = "domain:"
			}
			domains = append(domains, prefix+e.Value)
		} else {
			ips = append(ips, e.Value)
		}
	}
	r := map[string]any{}
	if len(domains) > 0 {
		r["domain"] = domains
	}
	if len(ips) > 0 {
		r["ip"] = ips
	}
	return r
}
func ValidateRoot(root map[string]any, _ string) error {
	if root == nil || object(root["routing"]) == nil {
		return fmt.Errorf("routing 必须是对象")
	}
	for _, f := range []struct {
		parent   map[string]any
		key      string
		required bool
	}{{root, "outbounds", true}, {object(root["routing"]), "rules", false}, {root, "inbounds", true}} {
		v, exists := f.parent[f.key]
		if !exists && !f.required {
			continue
		}
		items, ok := v.([]any)
		if !ok {
			return fmt.Errorf("%s 必须是对象数组", f.key)
		}
		for _, item := range items {
			if object(item) == nil {
				return fmt.Errorf("%s 包含无效条目", f.key)
			}
		}
	}
	return nil
}
func Strip(root map[string]any, _ string) {
	route := object(root["routing"])
	if route == nil {
		return
	}
	rules := []any{}
	for _, r := range list(route["rules"]) {
		if !owned(object(r)) {
			rules = append(rules, r)
		}
	}
	route["rules"] = rules
	outs := []any{}
	for _, o := range list(root["outbounds"]) {
		tag := object(o)["tag"]
		if tag != directTag && tag != denyTag {
			outs = append(outs, o)
		}
	}
	root["outbounds"] = outs
}
func HasComponents(root map[string]any, _ string) bool {
	for _, r := range list(object(root["routing"])["rules"]) {
		if owned(object(r)) {
			return true
		}
	}
	for _, o := range list(root["outbounds"]) {
		if tag := object(o)["tag"]; tag == directTag || tag == denyTag {
			return true
		}
	}
	return false
}
func Apply(root map[string]any, n model.Node) error {
	if err := ValidateRoot(root, n.Core); err != nil {
		return err
	}
	Strip(root, n.Core)
	p, err := n.AccessPolicy.Normalize()
	if err != nil {
		return err
	}
	if !p.Active() {
		return nil
	}
	tags := proxyTags(n)
	scoped := map[string]bool{}
	for _, tag := range tags {
		scoped[tag] = true
	}
	for _, item := range list(root["inbounds"]) {
		in := object(item)
		if scoped[str(in["tag"])] {
			s := object(in["sniffing"])
			if s == nil {
				s = map[string]any{}
			}
			s["enabled"] = false
			in["sniffing"] = s
		}
	}
	outs := list(root["outbounds"])
	if len(p.Whitelist) > 0 {
		var direct map[string]any
		for _, item := range outs {
			o := object(item)
			if o["tag"] == "direct" && o["protocol"] == "freedom" {
				direct = o
			}
		}
		if direct == nil {
			return fmt.Errorf("白名单需要 direct Freedom 出站")
		}
		b, err := json.Marshal(direct)
		if err != nil {
			return err
		}
		var copied map[string]any
		if err = json.Unmarshal(b, &copied); err != nil {
			return err
		}
		copied["tag"] = directTag
		s := object(copied["settings"])
		if s == nil {
			s = map[string]any{}
			copied["settings"] = s
		}
		s["finalRules"] = []any{map[string]any{"action": "allow"}}
		outs = append(outs, copied)
	}
	outs = append(outs, map[string]any{"protocol": "blackhole", "settings": map[string]any{}, "tag": denyTag})
	root["outbounds"] = outs
	front := []any{}
	add := func(entries []model.AccessEntry, out, user, label string) {
		matches := values(entries)
		for _, field := range []string{"domain", "ip"} {
			v, ok := matches[field]
			if !ok {
				continue
			}
			ruleTag := label + "-" + field
			if strings.HasPrefix(label, "white-relay-") {
				ruleTag = Prefix + ruleTag
			}
			r := map[string]any{"inboundTag": tags, field: v, "outboundTag": out, "ruleTag": ruleTag}
			if user != "" {
				r["inboundTag"] = []string{n.InboundTag}
				r["user"] = []string{user}
			}
			front = append(front, r)
		}
	}
	if len(p.Whitelist) > 0 {
		for _, link := range n.RelayLinks {
			if link.Enabled {
				add(p.Whitelist, model.RelayOutboundTag(link.Name), link.UserName, "white-relay-"+link.Name)
			}
		}
		add(p.Whitelist, directTag, "", "white")
	}
	if p.BlacklistEnabled() {
		add(p.Blacklist, denyTag, "", "black")
	}
	route := object(root["routing"])
	tail := list(route["rules"])
	if p.EffectiveMode() == model.AccessModeWhitelist {
		blocked := map[string]bool{}
		for _, o := range outs {
			m := object(o)
			if m["protocol"] == "blackhole" {
				blocked[str(m["tag"])] = true
			}
		}
		at := len(tail)
		for i, r := range tail {
			m := object(r)
			ins := list(m["inboundTag"])
			relevant := len(ins) == 0
			for _, in := range ins {
				if scoped[str(in)] {
					relevant = true
				}
			}
			if relevant && !blocked[str(m["outboundTag"])] {
				at = i
				break
			}
		}
		deny := map[string]any{"inboundTag": tags, "outboundTag": denyTag, "ruleTag": Prefix + "final"}
		combined := append([]any{}, tail[:at]...)
		combined = append(combined, deny)
		tail = append(combined, tail[at:]...)
	}
	route["rules"] = append(front, tail...)
	return nil
}
func AllowClient(root map[string]any, n model.Node) {
	if !n.AccessPolicy.Active() {
		return
	}
	route := object(root["routing"])
	if route == nil {
		return
	}
	rules := []any{}
	matches := values(n.AccessPolicy.Whitelist)
	for _, field := range []string{"domain", "ip"} {
		v, ok := matches[field]
		if !ok {
			continue
		}
		rules = append(rules, map[string]any{field: v, "outboundTag": "proxy"})
	}
	route["rules"] = append(rules, list(route["rules"])...)
}
func SnapshotSniffing(config []byte, n model.Node) (map[string]json.RawMessage, error) {
	var root map[string]any
	if err := json.Unmarshal(config, &root); err != nil {
		return nil, err
	}
	scoped := map[string]bool{}
	for _, tag := range proxyTags(n) {
		scoped[tag] = true
	}
	result := map[string]json.RawMessage{}
	for _, v := range list(root["inbounds"]) {
		in := object(v)
		tag := str(in["tag"])
		if scoped[tag] {
			b, err := json.Marshal(in["sniffing"])
			if err != nil {
				return nil, err
			}
			result[tag] = b
		}
	}
	return result, nil
}
func RestoreSniffing(root map[string]any, old model.Node) error {
	scoped := map[string]bool{}
	for _, tag := range proxyTags(old) {
		scoped[tag] = true
	}
	for _, v := range list(root["inbounds"]) {
		in := object(v)
		tag := str(in["tag"])
		if !scoped[tag] {
			continue
		}
		raw := old.AccessPolicySniffing[tag]
		if len(raw) == 0 || string(raw) == "null" {
			delete(in, "sniffing")
			continue
		}
		var s map[string]any
		if err := json.Unmarshal(raw, &s); err != nil {
			return err
		}
		in["sniffing"] = s
	}
	return nil
}
