package domain

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestNormalizeAccessPorts(t *testing.T) {
	for _, tc := range []struct{ input, want string }{
		{"", ""}, {"  ", ""}, {"all", ""}, {" ALL ", ""}, {"1", "1"}, {"65535", "65535"},
		{"443,80,443", "80,443"},
		{"80.443.8443", "80,443,8443"},
		{" 443 . 80 ， 8443,443 ", "80,443,8443"},
		{"80.443.8000-8100.8050", "80,443,8000-8100"},
		{" 0443 ， 80 , 8000 - 8100 ", "80,443,8000-8100"},
		{"8001-8100,8000-8050,8101,80-80", "80,8000-8101"},
		{"1-65535", "1-65535"}, {"443-443", "443"},
	} {
		t.Run(tc.input, func(t *testing.T) {
			got, err := NormalizeAccessPorts(tc.input)
			if err != nil || got != tc.want {
				t.Fatalf("ports %q = %q, %v; want %q", tc.input, got, err, tc.want)
			}
		})
	}
	for _, input := range []string{"0", "65536", "-1", "+443", "https", "*", "443,", ",443", "80,,443", "443.", ".443", "80..443", "80.,443", "80.65536", "443 80", "443-80", "1-2-3", "443-", "-443", "99999999999999999999999999", "１"} {
		t.Run("invalid-"+input, func(t *testing.T) {
			if _, err := NormalizeAccessPorts(input); err == nil {
				t.Fatalf("invalid ports accepted: %q", input)
			}
		})
	}
}

func TestAccessPolicyPortsRoundTripAndIdentity(t *testing.T) {
	var legacy AccessPolicy
	if err := json.Unmarshal([]byte(`{"whitelist":[{"type":"domain","value":"example.com","include_subdomains":true}]}`), &legacy); err != nil {
		t.Fatal(err)
	}
	old := legacy.Whitelist[0]
	policy, err := (AccessPolicy{Whitelist: []AccessEntry{
		old,
		{Type: AccessEntryDomain, Value: "Example.COM.", IncludeSubdomains: true, Ports: "443,80,443"},
		{Type: AccessEntryDomain, Value: "example.com", IncludeSubdomains: true, Ports: "80,443"},
		{Type: AccessEntryDomain, Value: "example.com", IncludeSubdomains: true, Ports: "8443"},
	}}).Normalize()
	if err != nil {
		t.Fatal(err)
	}
	if len(policy.Whitelist) != 3 || policy.Whitelist[0] != old || policy.Whitelist[1].Ports != "80,443" || policy.Whitelist[2].Ports != "8443" {
		t.Fatalf("incorrect port identity or legacy behavior: %+v", policy)
	}
	data, err := json.Marshal(policy)
	if err != nil {
		t.Fatal(err)
	}
	var restored AccessPolicy
	if err := json.Unmarshal(data, &restored); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(policy, restored) {
		t.Fatalf("ports lost after serialization: %s", data)
	}
	for _, kind := range []string{AccessEntryDomain, AccessEntryIP, AccessEntryCIDR} {
		value := map[string]string{AccessEntryDomain: "example.com", AccessEntryIP: "::1", AccessEntryCIDR: "10.0.0.0/8"}[kind]
		if _, err := (AccessPolicy{Blacklist: []AccessEntry{{Type: kind, Value: value, Ports: "0"}}}).Normalize(); err == nil {
			t.Fatalf("invalid ports accepted for %s", kind)
		}
	}
}

func TestClearAccessListPreservesModeAndSwitch(t *testing.T) {
	white := []AccessEntry{{Type: AccessEntryDomain, Value: "example.com", Ports: "80,443"}}
	black := []AccessEntry{{Type: AccessEntryIP, Value: "1.2.3.4", Ports: "553"}}
	for _, mode := range []string{"", AccessModeDefault, AccessModeWhitelist} {
		for _, disabled := range []bool{false, true} {
			for _, listName := range []string{"white", "black"} {
				policy := AccessPolicy{Mode: mode, Disabled: disabled, Whitelist: white, Blacklist: black}
				got, err := policy.ClearList(listName)
				if err != nil {
					t.Fatal(err)
				}
				want := policy
				if listName == "white" {
					want.Whitelist = nil
				} else {
					want.Blacklist = nil
				}
				if !reflect.DeepEqual(got, want) {
					t.Fatalf("clear %s changed policy beyond its list: got %+v, want %+v", listName, got, want)
				}
				if mode == AccessModeWhitelist && !disabled && !got.Active() {
					t.Fatal("clearing a list disabled whitelist-only access control")
				}
			}
		}
	}
}

func TestAccessModeSelectionAndFirstWhitelist(t *testing.T) {
	entry := AccessEntry{Type: AccessEntryDomain, Value: "example.com", Ports: "80,443"}
	for _, tc := range []struct {
		name, mode, list, wantMode string
		existing, empty, disabled  bool
	}{
		{name: "first-white", list: "white"},
		{name: "first-white-disabled", list: "white", disabled: true},
		{name: "only-black", list: "black"},
		{name: "empty-batch", list: "white", empty: true},
		{name: "manual-default", mode: AccessModeDefault, list: "white", wantMode: AccessModeDefault},
		{name: "manual-whitelist", mode: AccessModeWhitelist, list: "white", wantMode: AccessModeWhitelist},
		{name: "legacy-whitelist", list: "white", existing: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			policy := AccessPolicy{Mode: tc.mode, Disabled: tc.disabled}
			if tc.existing {
				policy.Whitelist = []AccessEntry{entry}
			}
			entries := []AccessEntry{entry}
			if tc.empty {
				entries = nil
			}
			got, err := policy.AddEntries(tc.list, entries)
			if err != nil {
				t.Fatal(err)
			}
			if got.Mode != tc.wantMode || got.Disabled != tc.disabled {
				t.Fatalf("unexpected automatic selection: %+v", got)
			}
		})
	}
	for _, mode := range []string{"", AccessModeDefault, AccessModeWhitelist} {
		for _, hasWhite := range []bool{false, true} {
			for _, disabled := range []bool{false, true} {
				policy := AccessPolicy{Mode: mode, Disabled: disabled, Blacklist: []AccessEntry{entry}}
				if hasWhite {
					policy.Whitelist = []AccessEntry{entry}
				}
				got, err := policy.SelectMode(AccessModeAuto)
				if err != nil {
					t.Fatal(err)
				}
				want := policy
				want.Mode = ""
				if !reflect.DeepEqual(got, want) {
					t.Fatalf("restoring auto changed the wrong fields: got %+v, want %+v", got, want)
				}
			}
		}
	}
	for _, mode := range []string{AccessModeDefault, AccessModeWhitelist} {
		got, err := (AccessPolicy{}).SelectMode(mode)
		if err != nil || got.Mode != mode {
			t.Fatalf("manual mode not recorded: %+v, %v", got, err)
		}
		data, err := json.Marshal(got)
		if err != nil {
			t.Fatal(err)
		}
		var restored AccessPolicy
		if err := json.Unmarshal(data, &restored); err != nil || restored.Mode != mode {
			t.Fatalf("manual mode lost on reload: %s, %v", data, err)
		}
	}
}

func TestAutomaticSelectionFollowsCurrentWhitelistAndPreservesManualChoice(t *testing.T) {
	entry := AccessEntry{Type: AccessEntryDomain, Value: "example.com", Ports: "80,443"}
	for _, selectWithExistingWhitelist := range []bool{false, true} {
		policy := AccessPolicy{}
		var err error
		if selectWithExistingWhitelist {
			policy = AccessPolicy{Mode: AccessModeDefault, Whitelist: []AccessEntry{entry}}
			policy, err = policy.SelectMode(AccessModeAuto)
		} else {
			policy, err = policy.AddEntries("white", []AccessEntry{entry})
		}
		if err != nil || policy.Mode != "" || policy.EffectiveMode() != AccessModeWhitelist {
			t.Fatalf("automatic restriction recorded a manual choice: %+v, %v", policy, err)
		}
		policy, err = policy.ClearList("white")
		if err != nil {
			t.Fatal(err)
		}
		data, err := json.Marshal(policy)
		if err != nil {
			t.Fatal(err)
		}
		var reloaded AccessPolicy
		if err := json.Unmarshal(data, &reloaded); err != nil {
			t.Fatal(err)
		}
		if reloaded.Mode != "" || reloaded.EffectiveMode() != AccessModeDefault || reloaded.Active() {
			t.Fatalf("cleared automatic whitelist did not return to default after reload: %s", data)
		}
		manual := AccessPolicy{Mode: AccessModeWhitelist, Whitelist: []AccessEntry{entry}}
		manual, err = manual.ClearList("white")
		if err != nil || manual.EffectiveMode() != AccessModeWhitelist || !manual.Active() {
			t.Fatalf("clearing manual whitelist removed its restriction: %+v, %v", manual, err)
		}
	}
	policy := AccessPolicy{Whitelist: []AccessEntry{entry}}
	if policy.EffectiveMode() != AccessModeWhitelist || policy.BlacklistEnabled() {
		t.Fatal("existing automatic whitelist was not restricted")
	}
	policy.Mode = AccessModeDefault
	if policy.EffectiveMode() != AccessModeDefault || !policy.BlacklistEnabled() {
		t.Fatal("whitelist overrode manual default mode")
	}
}
