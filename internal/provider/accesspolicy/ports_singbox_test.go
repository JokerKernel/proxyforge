package accesspolicy

import (
	"encoding/json"
	"reflect"
	"testing"

	"proxyforge/internal/domain"
)

func TestSingBoxPortGroupsAndClientExceptions(t *testing.T) {
	n := domain.NodeSpec{Core: domain.CoreSingBox, InboundTag: "main", AccessPolicy: domain.AccessPolicy{
		Mode: domain.AccessModeWhitelist,
		Whitelist: []domain.AccessEntry{
			{Type: "domain", Value: "a.example.com", IncludeSubdomains: true, Ports: "80"},
			{Type: "domain", Value: "b.example.com", Ports: "80"},
			{Type: "domain", Value: "a.example.com", IncludeSubdomains: true, Ports: "553,8000-8050"},
			{Type: "domain", Value: "a.example.com", IncludeSubdomains: true, Ports: "8040-8100"},
			{Type: "ip", Value: "::1", Ports: "443"},
			{Type: "cidr", Value: "10.0.0.0/8"},
		},
		Blacklist: []domain.AccessEntry{{Type: "domain", Value: "blocked.test", Ports: "25"}},
	}}
	root := map[string]any{
		"inbounds":  []any{map[string]any{"tag": "main"}},
		"outbounds": []any{map[string]any{"tag": "direct", "type": "direct"}},
		"route":     map[string]any{"rules": []any{map[string]any{"inbound": []string{"main"}, "action": "route", "outbound": "direct"}}},
	}
	if err := Apply(root, n); err != nil {
		t.Fatal(err)
	}
	sets := list(object(root["route"])["rule_set"])
	// Decode like a real config consumer so map/list types are consistent.
	data, _ := json.Marshal(object(sets[1])["rules"])
	var got []map[string]any
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	wantJSON := `[
		{"domain":["a.example.com"],"domain_suffix":[".a.example.com"],"port":[80,553],"port_range":["8000:8100"]},
		{"domain":["b.example.com"],"port":[80]},
		{"ip_cidr":["::1"],"port":[443]},
		{"ip_cidr":["10.0.0.0/8"]}
	]`
	var want []map[string]any
	_ = json.Unmarshal([]byte(wantJSON), &want)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %s, want %s", data, wantJSON)
	}
	rules := list(object(root["route"])["rules"])
	for _, item := range rules {
		rule := object(item)
		if rule["action"] == "reject" && len(list(rule["rules"])) > 1 {
			t.Fatal("whitelist mode activated blacklist")
		}
	}
	if object(rules[len(rules)-2])["action"] != "reject" {
		t.Fatal("final deny missing")
	}
	client := map[string]any{"route": map[string]any{"rules": []any{}}}
	if err := AllowClient(client, n); err != nil {
		t.Fatal(err)
	}
	clientRules := list(object(client["route"])["rules"])
	if len(clientRules) != len(want) {
		t.Fatal("client port groups differ from server")
	}
	for i, item := range clientRules {
		rule := object(item)
		if rule["outbound"] != "proxy" {
			t.Fatal("client exception does not use proxy")
		}
		delete(rule, "action")
		delete(rule, "outbound")
		data, _ := json.Marshal(rule)
		var normalized map[string]any
		_ = json.Unmarshal(data, &normalized)
		if !reflect.DeepEqual(normalized, want[i]) {
			t.Fatalf("client exception changed matching: %s", data)
		}
	}
	before, _ := json.Marshal(root)
	if err := Apply(root, n); err != nil {
		t.Fatal(err)
	}
	after, _ := json.Marshal(root)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("reapplying left stale groups")
	}
	n.AccessPolicy.Disabled = true
	if err := Apply(root, n); err != nil {
		t.Fatal(err)
	}
	if HasComponents(root, n.Core) {
		t.Fatal("disabled policy left port rules")
	}
}
