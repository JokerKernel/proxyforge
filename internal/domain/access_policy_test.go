package domain

import "testing"

func TestDisabledAccessPolicyIgnoresModeAndLists(t *testing.T) {
	for _, mode := range []string{AccessModeDefault, AccessModeWhitelist} {
		for _, entries := range [][]AccessEntry{nil, {{Type: AccessEntryDomain, Value: "example.com"}}} {
			policy := AccessPolicy{Disabled: true, Mode: mode, Whitelist: entries, Blacklist: entries}
			if policy.Active() || policy.BlacklistEnabled() {
				t.Fatal("disabled policy still activates rules or whitelist-only final deny")
			}
			cleared, err := policy.ClearList("white")
			if err != nil || !cleared.Disabled {
				t.Fatal("clearing a list reenabled access control")
			}
		}
	}
}

func TestAccessPolicyActiveWithOnlyBlacklist(t *testing.T) {
	for _, mode := range []string{"", AccessModeDefault, AccessModeWhitelist} {
		t.Run(mode, func(t *testing.T) {
			policy := AccessPolicy{Mode: mode}
			if policy.Active() != (mode == AccessModeWhitelist) {
				t.Fatal("empty policy must only activate in whitelist mode")
			}
			policy.Blacklist = []AccessEntry{{Type: AccessEntryDomain, Value: "denied.com"}}
			if !policy.Active() {
				t.Fatal("blacklist-only policy must activate")
			}
			if policy.BlacklistEnabled() != (mode != AccessModeWhitelist) {
				t.Fatal("blacklist must only apply in default mode")
			}
		})
	}
}

func TestAccessEntryNormalization(t *testing.T) {
	for _, test := range []struct {
		input string
		exact bool
		want  AccessEntry
	}{
		{" Example.COM. ", false, AccessEntry{Type: AccessEntryDomain, Value: "example.com", IncludeSubdomains: true}},
		{"example.com", true, AccessEntry{Type: AccessEntryDomain, Value: "example.com"}},
		{"192.168.1.0", false, AccessEntry{Type: AccessEntryIP, Value: "192.168.1.0"}},
		{"192.168.1.123/24", false, AccessEntry{Type: AccessEntryCIDR, Value: "192.168.1.0/24"}},
		{"2001:db8::1", false, AccessEntry{Type: AccessEntryIP, Value: "2001:db8::1"}},
		{"2001:db8::123/64", false, AccessEntry{Type: AccessEntryCIDR, Value: "2001:db8::/64"}},
	} {
		t.Run(test.input, func(t *testing.T) {
			got, err := ParseAccessEntry(test.input, test.exact)
			if err != nil || got != test.want {
				t.Fatalf("got=%#v err=%v", got, err)
			}
		})
	}
	for _, input := range []string{"", "https://example.com", "example.com/path", "example.com:443", "*.example.com", "a..example.com", "192.168.1.999", "127.0.0.1/99", "fe80::1%eth0", ".example.com"} {
		if _, err := ParseAccessEntry(input, false); err == nil {
			t.Errorf("accepted invalid destination %q", input)
		}
	}
}

func TestAccessPolicyDeduplicatesAndValidatesTypes(t *testing.T) {
	policy := AccessPolicy{Whitelist: []AccessEntry{{Type: AccessEntryDomain, Value: "EXAMPLE.COM", IncludeSubdomains: true}, {Type: AccessEntryDomain, Value: "example.com", IncludeSubdomains: true}, {Type: AccessEntryDomain, Value: "example.com"}}}
	got, err := policy.Normalize()
	if err != nil || len(got.Whitelist) != 2 {
		t.Fatalf("got=%#v err=%v", got, err)
	}
	for _, mode := range []string{"invalid", "blacklist"} {
		if _, err := (AccessPolicy{Mode: mode}).Normalize(); err == nil {
			t.Fatalf("accepted unsupported mode %q", mode)
		}
	}
	if _, err := (AccessPolicy{Whitelist: []AccessEntry{{Type: AccessEntryDomain, Value: "192.168.1.0"}}}).Normalize(); err == nil {
		t.Fatal("accepted mismatched entry type")
	}
	if (AccessPolicy{Mode: AccessModeDefault}).Active() {
		t.Fatal("empty blacklist should retain original behavior")
	}
	if !(AccessPolicy{Mode: AccessModeWhitelist}).Active() {
		t.Fatal("empty whitelist must still deny all")
	}
}
