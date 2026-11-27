package accesspolicy

import (
	"encoding/json"
	"strings"
	"testing"

	"proxyforge/internal/domain"
)

func relayRouteFixture(core string) (map[string]any, domain.NodeSpec, string) {
	n := domain.NodeSpec{Core: core, InboundTag: "main", AccessPolicy: domain.AccessPolicy{Mode: domain.AccessModeWhitelist},
		RelayLinks: []domain.RelayLink{
			{Name: "first", UserName: "first", UUID: "first-uuid", Enabled: true, AccessPolicy: &domain.AccessPolicy{Disabled: true}},
			{Name: "second", UserName: "second", UUID: "second-uuid", Enabled: true, AccessPolicy: &domain.AccessPolicy{Mode: domain.AccessModeWhitelist}},
		}}
	root := map[string]any{"inbounds": []any{map[string]any{"tag": "main"}}}
	key := "routing"
	if core == domain.CoreXray {
		object(list(root["inbounds"])[0])["settings"] = map[string]any{"clients": []any{map[string]any{"email": "first", "id": "first-uuid"}, map[string]any{"email": "second", "id": "second-uuid"}}}
		root["outbounds"] = []any{map[string]any{"tag": "direct", "protocol": "freedom"}, map[string]any{"tag": "private-block", "protocol": "blackhole"}}
		root[key] = map[string]any{"domainStrategy": "IPOnDemand", "rules": []any{
			map[string]any{"ip": []string{"192.168.0.0/16"}, "outboundTag": "private-block"},
			map[string]any{"inboundTag": []string{"main"}, "user": []string{"first"}, "outboundTag": "first-out", "ruleTag": "custom-original-tag"},
			map[string]any{"inboundTag": []string{"main"}, "user": []string{"second"}, "outboundTag": "second-out"},
			map[string]any{"domain": []string{"full:manual.test"}, "outboundTag": "direct"},
		}}
	} else {
		object(list(root["inbounds"])[0])["users"] = []any{map[string]any{"name": "first", "uuid": "first-uuid"}, map[string]any{"name": "second", "uuid": "second-uuid"}}
		key = "route"
		root["outbounds"] = []any{map[string]any{"tag": "direct", "type": "direct"}}
		root[key] = map[string]any{"rules": []any{
			map[string]any{"ip_is_private": true, "action": "reject"},
			map[string]any{"inbound": []string{"main"}, "auth_user": []string{"first"}, "action": "route", "outbound": "first-out"},
			map[string]any{"inbound": []string{"main"}, "auth_user": []string{"second"}, "action": "route", "outbound": "second-out"},
			map[string]any{"domain": []string{"manual.test"}, "action": "route", "outbound": "direct"},
		}}
	}
	return root, n, key
}

func TestRelaysHaveOneCatchAllAndRestoreOriginalOrder(t *testing.T) {
	for _, core := range []string{domain.CoreXray, domain.CoreSingBox} {
		t.Run(core, func(t *testing.T) {
			root, n, key := relayRouteFixture(core)
			original := clone(object(root[key]))["rules"]
			if err := Apply(root, n); err != nil {
				t.Fatal(err)
			}
			counts := map[string]int{}
			for _, item := range list(object(root[key])["rules"]) {
				rule := clone(object(item))
				if core == domain.CoreSingBox {
					delete(rule, "rule_set")
				}
				if identity, valid := relayRouteIdentity(rule, core); valid {
					counts[identity]++
				}
			}
			for _, item := range list(original) {
				if identity, valid := relayRouteIdentity(object(item), core); valid && counts[identity] != 1 {
					t.Fatalf("relay catch-all count=%d, want 1", counts[identity])
				}
			}
			before, _ := json.Marshal(root)
			if err := Apply(root, n); err != nil {
				t.Fatal(err)
			}
			after, _ := json.Marshal(root)
			if string(before) != string(after) {
				t.Fatal("reapplying the policy moved or duplicated routes")
			}
			// DNS/IP settings can prepend a persistent resolve action.
			prefix := map[string]any{"action": "resolve", "server": "local"}
			if core == domain.CoreXray {
				prefix = map[string]any{"domain": []string{"full:prefix.test"}, "outboundTag": "private-block"}
			}
			object(root[key])["rules"] = append([]any{prefix}, list(object(root[key])["rules"])...)
			if err := Strip(root, core); err != nil {
				t.Fatal(err)
			}
			want := append([]any{prefix}, list(original)...)
			gotJSON, _ := json.Marshal(object(root[key])["rules"])
			wantJSON, _ := json.Marshal(want)
			if string(gotJSON) != string(wantJSON) {
				t.Fatalf("original route order/tags changed: got=%+v, want=%+v", object(root[key])["rules"], want)
			}
		})
	}
}

func TestPolicyDisableRejectsMissingRelayRoute(t *testing.T) {
	for _, core := range []string{domain.CoreXray, domain.CoreSingBox} {
		t.Run(core, func(t *testing.T) {
			root, n, key := relayRouteFixture(core)
			if err := Apply(root, n); err != nil {
				t.Fatal(err)
			}
			var rules []any
			for _, item := range list(object(root[key])["rules"]) {
				rule := object(item)
				if core == domain.CoreXray && strings.HasPrefix(str(rule["ruleTag"]), relayRoutePrefix) || core == domain.CoreSingBox && containsRelayRouteMarker(rule) {
					continue
				}
				rules = append(rules, item)
			}
			object(root[key])["rules"] = rules
			if err := Apply(root, n.WithoutAccessPolicies()); err == nil {
				t.Fatal("disabling a damaged policy silently removed relay routing")
			}
		})
	}
}

func TestRelayRestoreRejectsMalformedOrDuplicateMarkers(t *testing.T) {
	for _, core := range []string{domain.CoreXray, domain.CoreSingBox} {
		for _, damage := range []string{"duplicate", "plain duplicate", "invalid position", "out of bounds", "changed scope", "multiple sets", "nested marker"} {
			if core == domain.CoreXray && (damage == "multiple sets" || damage == "nested marker") {
				continue
			}
			t.Run(core+"/"+damage, func(t *testing.T) {
				root, n, key := relayRouteFixture(core)
				original := clone(object(list(object(root[key])["rules"])[1]))
				if err := Apply(root, n); err != nil {
					t.Fatal(err)
				}
				rules := list(object(root[key])["rules"])
				for i, item := range rules {
					rule := object(item)
					marker := str(rule["ruleTag"])
					if core == domain.CoreSingBox && len(list(rule["rule_set"])) == 1 {
						marker = str(list(rule["rule_set"])[0])
					}
					if !strings.HasPrefix(marker, relayRoutePrefix) {
						continue
					}
					switch damage {
					case "duplicate":
						rules = append(rules, clone(rule))
					case "plain duplicate":
						rules = append(rules, original)
					case "invalid position", "out of bounds":
						marker = relayRoutePrefix + "broken"
						if damage == "out of bounds" {
							marker = relayRoutePrefix + "999999"
						}
						if core == domain.CoreXray {
							rule["ruleTag"] = marker
						} else {
							rule["rule_set"] = []string{marker}
						}
					case "changed scope":
						field := "user"
						if core == domain.CoreSingBox {
							field = "auth_user"
						}
						rule[field] = []string{"first", "another"}
					case "multiple sets":
						rule["rule_set"] = []string{marker, scopeSet}
					case "nested marker":
						rules[i] = map[string]any{"type": "logical", "mode": "and", "rules": []any{rule}, "action": "route", "outbound": "first-out"}
					}
					break
				}
				object(root[key])["rules"] = rules
				if err := Strip(root, core); err == nil {
					t.Fatal("damaged/duplicate relay metadata was accepted")
				}
			})
		}
	}
}

func TestWhitelistDenyPrecedesSingBoxDirectAndBypass(t *testing.T) {
	for _, action := range []string{"route", "direct", "bypass"} {
		for _, relay := range []bool{false, true} {
			t.Run(action+map[bool]string{false: "/node", true: "/relay"}[relay], func(t *testing.T) {
				root, n, key := relayRouteFixture(domain.CoreSingBox)
				if relay {
					n.AccessPolicy.Disabled = true
					n.RelayLinks = n.RelayLinks[:1]
					n.RelayLinks[0].AccessPolicy = &domain.AccessPolicy{Mode: domain.AccessModeWhitelist}
				} else {
					n.RelayLinks = nil
				}
				rule := map[string]any{"action": action, "domain": []string{"outside.test"}, "outbound": "direct"}
				if action == "direct" {
					delete(rule, "outbound")
				}
				object(root[key])["rules"] = append([]any{rule}, list(object(root[key])["rules"])...)
				if err := Apply(root, n); err != nil {
					t.Fatal(err)
				}
				denied := false
				for _, item := range list(object(root[key])["rules"]) {
					current := object(item)
					if current["action"] == "reject" {
						denied = true
					}
					if current["action"] == action && (current["domain"] != nil || len(list(current["rules"])) > 1) && !denied {
						t.Fatalf("%s can bypass the empty whitelist: %+v", action, current)
					}
				}
			})
		}
	}
}

func TestXrayIPPoliciesRequireOnDemandResolution(t *testing.T) {
	for _, strategy := range []string{"", "AsIs", "IPIfNonMatch", "IPOnDemand"} {
		for _, relay := range []bool{false, true} {
			root, n, key := relayRouteFixture(domain.CoreXray)
			object(root[key])["domainStrategy"] = strategy
			p := domain.AccessPolicy{Mode: domain.AccessModeDefault, Blacklist: []domain.AccessEntry{{Type: "cidr", Value: "192.168.0.0/16", Ports: "443"}}}
			if relay {
				n.AccessPolicy.Disabled = true
				n.RelayLinks[0].AccessPolicy = &p
			} else {
				n.AccessPolicy = p
			}
			if err := Apply(root, n); (err == nil) != (strategy == "IPOnDemand") {
				t.Fatalf("strategy=%q relay=%v error=%v", strategy, relay, err)
			}
		}
	}
}

func TestRelayPolicyRejectsUserIdentityMismatch(t *testing.T) {
	for _, core := range []string{domain.CoreXray, domain.CoreSingBox} {
		for _, damage := range []string{"missing user", "wrong UUID", "duplicate client", "duplicate relay user", "ordinary user conflict"} {
			t.Run(core+"/"+damage, func(t *testing.T) {
				root, n, key := relayRouteFixture(core)
				inbound := object(list(root["inbounds"])[0])
				users, nameField, uuidField := list(inbound["users"]), "name", "uuid"
				if core == domain.CoreXray {
					users, nameField, uuidField = list(object(inbound["settings"])["clients"]), "email", "id"
				}
				switch damage {
				case "missing user":
					object(users[0])[nameField] = "renamed"
				case "wrong UUID":
					object(users[0])[uuidField] = "wrong-uuid"
				case "duplicate client":
					users = append(users, clone(object(users[0])))
				case "duplicate relay user":
					n.RelayLinks[1].UserName = "first"
					field := "auth_user"
					if core == domain.CoreXray {
						field = "user"
					}
					object(list(object(root[key])["rules"])[2])[field] = []string{"first"}
				case "ordinary user conflict":
					n.UserName = "first"
				}
				if core == domain.CoreXray {
					object(inbound["settings"])["clients"] = users
				} else {
					inbound["users"] = users
				}
				if err := Apply(root, n); err == nil {
					t.Fatal("policy accepted a relay user that could not be isolated")
				}
			})
		}
	}
}
