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
					if mode == "" {
						want.AutoWhitelist = true
					}
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
		name, mode, list, wantMode           string
		existing, empty, disabled, autoWhite bool
	}{
		{name: "first-white", list: "white", autoWhite: true},
		{name: "first-white-disabled", list: "white", autoWhite: true, disabled: true},
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
			if got.Mode != tc.wantMode || got.Disabled != tc.disabled || got.AutoWhitelist != tc.autoWhite {
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
				want.AutoWhitelist = hasWhite
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

func TestAutomaticSelectionKeepsManualChoiceEmptyAndRemembersRestriction(t *testing.T) {
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
		if reloaded.Mode != "" || reloaded.EffectiveMode() != AccessModeWhitelist || !reloaded.Active() {
			t.Fatalf("reloading a cleared automatic whitelist reopened access: %s", data)
		}
		manual, err := reloaded.SelectMode(AccessModeDefault)
		if err != nil || manual.Mode != AccessModeDefault || manual.AutoWhitelist || manual.EffectiveMode() != AccessModeDefault {
			t.Fatalf("manual selection did not override automatic result: %+v, %v", manual, err)
		}
		auto, err := reloaded.SelectMode(AccessModeAuto)
		if err != nil || auto.Mode != "" || auto.EffectiveMode() != AccessModeDefault {
			t.Fatalf("reselecting auto with no whitelist did not reset its result: %+v, %v", auto, err)
		}
	}
}

func TestEffectiveModeChecksExistingWhitelistBeforeAutomaticMarker(t *testing.T) {
	entry := AccessEntry{Type: AccessEntryDomain, Value: "example.com", Ports: "80,443"}
	auto := AccessPolicy{Whitelist: []AccessEntry{entry}}
	if auto.Mode != "" || auto.AutoWhitelist || auto.EffectiveMode() != AccessModeWhitelist || auto.BlacklistEnabled() {
		t.Fatalf("unset choice with existing whitelist was not restricted: %+v", auto)
	}
	normalized, err := auto.Normalize()
	if err != nil || normalized.Mode != "" || !normalized.AutoWhitelist {
		t.Fatalf("normalization did not remember automatic restriction: %+v, %v", normalized, err)
	}
	cleared, err := auto.ClearList("white")
	if err != nil || cleared.EffectiveMode() != AccessModeWhitelist || !cleared.Active() {
		t.Fatalf("clearing an existing automatic whitelist reopened access: %+v, %v", cleared, err)
	}
	manual := auto
	manual.Mode = AccessModeDefault
	if manual.EffectiveMode() != AccessModeDefault || !manual.BlacklistEnabled() {
		t.Fatal("existing whitelist overrode an explicit default choice")
	}
}
