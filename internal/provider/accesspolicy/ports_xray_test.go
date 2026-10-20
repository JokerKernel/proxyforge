package accesspolicy

import (
	"encoding/json"
	"reflect"
	"testing"

	"proxyforge/internal/domain"
)

func testRoot() map[string]any {
	return map[string]any{
		"inbounds":  []any{map[string]any{"tag": "main"}, map[string]any{"tag": "office-in"}},
		"outbounds": []any{map[string]any{"tag": "direct", "protocol": "freedom", "settings": map[string]any{}}},
		"routing": map[string]any{"rules": []any{
			map[string]any{"ruleTag": "manual", "port": "25", "outboundTag": "custom"},
		}},
	}
}

type expectedGroup struct {
	field, ports string
	values       []string
}

func checkGroups(t *testing.T, root map[string]any, outbound, user string, tags []string, want []expectedGroup) {
	t.Helper()
	var expectedTags []any
	for _, tag := range tags {
		expectedTags = append(expectedTags, tag)
	}
	var got []map[string]any
	for _, item := range list(object(root["routing"])["rules"]) {
		rule := object(item)
		if rule["outboundTag"] == outbound && reflect.DeepEqual(list(rule["user"]), listForUser(user)) {
			got = append(got, rule)
		}
	}
	if len(got) != len(want) {
		t.Fatalf("%s user %q: got %d groups, want %d: %+v", outbound, user, len(got), len(want), got)
	}
	for _, expected := range want {
		found := false
		for _, rule := range got {
			if str(rule["port"]) != expected.ports || !reflect.DeepEqual(list(rule[expected.field]), list(expected.values)) {
				continue
			}
			if !reflect.DeepEqual(list(rule["inboundTag"]), expectedTags) {
				t.Fatalf("rule applies to wrong inbounds: %+v", rule)
			}
			if expected.ports == "" {
				if _, ok := rule["port"]; ok {
					t.Fatalf("unrestricted rule contains a port condition: %+v", rule)
				}
			}
			other := "ip"
			if expected.field == "ip" {
				other = "domain"
			}
			if _, ok := rule[other]; ok {
				t.Fatalf("domain and IP conditions were combined: %+v", rule)
			}
			found = true
		}
		if !found {
			t.Fatalf("missing %s user %q group %+v; got %+v", outbound, user, expected, got)
		}
	}
}

func listForUser(user string) []any {
	if user == "" {
		return nil
	}
	return []any{user}
}

func TestPortsGroupOnlyCompatibleDestinationsAndKeepRelayScope(t *testing.T) {
	n := domain.NodeSpec{Core: domain.CoreXray,
		InboundTag:      "main",
		LandingAccesses: []domain.LandingAccess{{Name: "office", Security: "tls", Port: 9443, Enabled: true}},
		RelayLinks:      []domain.RelayLink{{Name: "relay", UserName: "relay-user", Enabled: true}},
		AccessPolicy: domain.AccessPolicy{
			Whitelist: []domain.AccessEntry{
				{Type: "domain", Value: "a.example.com", IncludeSubdomains: true, Ports: "443"},
				{Type: "domain", Value: "b.example.com", Ports: "443"},
				{Type: "domain", Value: "c.example.com", Ports: "443,80"},
				{Type: "domain", Value: "d.example.com"},
				{Type: "domain", Value: "a.example.com", IncludeSubdomains: true, Ports: "8443"},
				{Type: "ip", Value: "1.2.3.4", Ports: "443"},
				{Type: "cidr", Value: "10.0.0.0/8", Ports: "80,443"},
				{Type: "ip", Value: "2001:db8::1"},
			},
			Blacklist: []domain.AccessEntry{
				{Type: "domain", Value: "blocked.example.com", Ports: "25"},
				{Type: "ip", Value: "5.6.7.8", Ports: "8000-8100"},
			},
		},
	}
	root := testRoot()
	if err := Apply(root, n); err != nil {
		t.Fatal(err)
	}
	white := []expectedGroup{
		{"domain", "443", []string{"full:b.example.com"}},
		{"domain", "80,443", []string{"full:c.example.com"}},
		{"domain", "", []string{"full:d.example.com"}},
		{"domain", "443,8443", []string{"domain:a.example.com"}},
		{"ip", "443", []string{"1.2.3.4"}},
		{"ip", "80,443", []string{"10.0.0.0/8"}},
		{"ip", "", []string{"2001:db8::1"}},
	}
	checkGroups(t, root, directTag, "", []string{"main", "office-in"}, white)
	checkGroups(t, root, "relay-out", "relay-user", []string{"main"}, white)
	checkGroups(t, root, denyTag, "", []string{"main", "office-in"}, []expectedGroup{
		{"domain", "25", []string{"full:blocked.example.com"}},
		{"ip", "8000-8100", []string{"5.6.7.8"}},
	})
	seenTags := map[string]bool{}
	for _, item := range list(object(root["routing"])["rules"]) {
		tag := str(object(item)["ruleTag"])
		if seenTags[tag] {
			t.Fatalf("duplicate generated rule tag: %s", tag)
		}
		seenTags[tag] = true
	}
	before, _ := json.Marshal(root)
	if err := Apply(root, n); err != nil {
		t.Fatal(err)
	}
	after, _ := json.Marshal(root)
	if string(before) != string(after) {
		t.Fatal("reapplying ports left stale rules or changed their order")
	}
	// The native client's private-address exceptions need the same port limits.
	n.AccessPolicy, _ = n.AccessPolicy.Normalize()
	client := testRoot()
	if err := AllowClient(client, n); err != nil {
		t.Fatal(err)
	}
	checkGroups(t, client, "proxy", "", nil, white)
	n.AccessPolicy.Disabled = true
	if err := Apply(root, n); err != nil {
		t.Fatal(err)
	}
	if HasComponents(root, domain.CoreXray) {
		t.Fatal("disabled policy left generated components")
	}
	rules := list(object(root["routing"])["rules"])
	if len(rules) != 1 || object(rules[0])["ruleTag"] != "manual" || object(rules[0])["port"] != "25" {
		t.Fatalf("disabling policy modified a manual port rule: %+v", rules)
	}
}

func TestWhitelistPortsPrecedeFinalDeny(t *testing.T) {
	n := domain.NodeSpec{Core: domain.CoreXray, InboundTag: "main", AccessPolicy: domain.AccessPolicy{
		Mode:      domain.AccessModeWhitelist,
		Whitelist: []domain.AccessEntry{{Type: "domain", Value: "example.com", Ports: "80,443"}},
		Blacklist: []domain.AccessEntry{{Type: "domain", Value: "blocked.example.com", Ports: "25"}},
	}}
	root := testRoot()
	if err := Apply(root, n); err != nil {
		t.Fatal(err)
	}
	rules := list(object(root["routing"])["rules"])
	if len(rules) != 3 || object(rules[0])["port"] != "80,443" || object(rules[0])["outboundTag"] != directTag || object(rules[1])["ruleTag"] != Prefix+"final" || object(rules[2])["ruleTag"] != "manual" {
		t.Fatalf("whitelist ports bypassed final deny or activated blacklist: %+v", rules)
	}
}

func TestMergedPortsPreserveEachDestinationsScope(t *testing.T) {
	for _, tc := range []struct {
		name    string
		entries []domain.AccessEntry
		want    []expectedGroup
	}{
		{
			name: "same-domain",
			entries: []domain.AccessEntry{
				{Type: "domain", Value: "dantapi.top", IncludeSubdomains: true, Ports: "80,443"},
				{Type: "domain", Value: "dantapi.top", IncludeSubdomains: true, Ports: "553"},
			},
			want: []expectedGroup{{"domain", "80,443,553", []string{"domain:dantapi.top"}}},
		},
		{
			name: "regroup-equal-port-unions",
			entries: []domain.AccessEntry{
				{Type: "domain", Value: "a.example.com", Ports: "80"},
				{Type: "domain", Value: "b.example.com", Ports: "553,80"},
				{Type: "domain", Value: "a.example.com", Ports: "553"},
			},
			want: []expectedGroup{{"domain", "80,553", []string{"full:a.example.com", "full:b.example.com"}}},
		},
		{
			name: "do-not-expand-other-domains",
			entries: []domain.AccessEntry{
				{Type: "domain", Value: "a.example.com", Ports: "80"},
				{Type: "domain", Value: "b.example.com", Ports: "80"},
				{Type: "domain", Value: "a.example.com", Ports: "553"},
			},
			want: []expectedGroup{
				{"domain", "80,553", []string{"full:a.example.com"}},
				{"domain", "80", []string{"full:b.example.com"}},
			},
		},
		{
			name: "keep-exact-and-subdomain-matches-separate",
			entries: []domain.AccessEntry{
				{Type: "domain", Value: "example.com", Ports: "80,443"},
				{Type: "domain", Value: "example.com", IncludeSubdomains: true, Ports: "553"},
			},
			want: []expectedGroup{
				{"domain", "80,443", []string{"full:example.com"}},
				{"domain", "553", []string{"domain:example.com"}},
			},
		},
		{
			name: "unrestricted-before-limited",
			entries: []domain.AccessEntry{
				{Type: "domain", Value: "example.com"},
				{Type: "domain", Value: "example.com", Ports: "443"},
			},
			want: []expectedGroup{{"domain", "", []string{"full:example.com"}}},
		},
		{
			name: "unrestricted-after-limited",
			entries: []domain.AccessEntry{
				{Type: "domain", Value: "example.com", Ports: "443"},
				{Type: "domain", Value: "example.com"},
				{Type: "domain", Value: "example.com", Ports: "553"},
			},
			want: []expectedGroup{{"domain", "", []string{"full:example.com"}}},
		},
		{
			name: "merge-ipv4-ipv6-and-cidr-ranges",
			entries: []domain.AccessEntry{
				{Type: "ip", Value: "1.2.3.4", Ports: "8000-8050"},
				{Type: "ip", Value: "1.2.3.4", Ports: "8030-8100"},
				{Type: "cidr", Value: "10.0.0.0/8", Ports: "80,443"},
				{Type: "cidr", Value: "10.0.0.0/8", Ports: "553"},
				{Type: "ip", Value: "2001:db8::1", Ports: "80.443"},
				{Type: "ip", Value: "2001:db8::1", Ports: "553"},
			},
			want: []expectedGroup{
				{"ip", "8000-8100", []string{"1.2.3.4"}},
				{"ip", "80,443,553", []string{"10.0.0.0/8", "2001:db8::1"}},
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := append([]domain.AccessEntry(nil), tc.entries...)
			n := domain.NodeSpec{Core: domain.CoreXray, InboundTag: "main", AccessPolicy: domain.AccessPolicy{Whitelist: tc.entries, Blacklist: tc.entries}}
			root := testRoot()
			if err := Apply(root, n); err != nil {
				t.Fatal(err)
			}
			checkGroups(t, root, directTag, "", []string{"main"}, tc.want)
			checkGroups(t, root, denyTag, "", []string{"main"}, tc.want)
			client := testRoot()
			if err := AllowClient(client, n); err != nil {
				t.Fatal(err)
			}
			checkGroups(t, client, "proxy", "", nil, tc.want)
			if !reflect.DeepEqual(before, tc.entries) {
				t.Fatalf("compilation modified the management entries: %+v", tc.entries)
			}
		})
	}
}
