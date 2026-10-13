package cli

import (
	"bufio"
	"context"
	"reflect"
	"strings"
	"testing"

	"proxyforge/internal/domain"
)

func TestAccessListMenusAddAndDeleteOnlySelectedList(t *testing.T) {
	for _, core := range []string{domain.CoreXray, domain.CoreSingBox} {
		for _, list := range []string{"white", "black"} {
			t.Run(core+"/"+list, func(t *testing.T) {
				c, out := accessCLI(t, core)
				policy := switchPolicy()
				if err := c.app.SetAccessPolicy(context.Background(), core, policy); err != nil {
					t.Fatal(err)
				}
				choice, name := "2", "白名单"
				if list == "black" {
					choice, name = "3", "黑名单"
				}
				// Add an exact domain and a normalized CIDR, then remove only the domain.
				c.reader = bufio.NewReader(strings.NewReader(choice + "\n1\nadded.example.com\n2\n1\n1\n192.168.3.42/24\n1\n2\n2\n1\n0\n0\n"))
				if err := c.accessPolicyMenu(context.Background(), core); err != nil {
					t.Fatal(err)
				}
				want := policy
				cidr := domain.AccessEntry{Type: domain.AccessEntryCIDR, Value: "192.168.3.0/24"}
				if list == "white" {
					want.Whitelist = append(append([]domain.AccessEntry(nil), policy.Whitelist...), cidr)
				} else {
					want.Blacklist = append(append([]domain.AccessEntry(nil), policy.Blacklist...), cidr)
				}
				got, err := c.app.AccessPolicy(core)
				if err != nil || !reflect.DeepEqual(got, want) {
					t.Fatalf("selected list or other settings changed incorrectly: %#v, want=%#v, err=%v", got, want, err)
				}
				for _, text := range []string{
					"访问控制  ›  " + name + "管理",
					name + "（3 条）", name + "（2 条）", "192.168.3.0/24",
				} {
					if !strings.Contains(out.String(), text) {
						t.Fatalf("list did not refresh or breadcrumb omitted %q: %s", text, out.String())
					}
				}
				if strings.Contains(out.String(), "[错误]") {
					t.Fatalf("list action failed: %s", out.String())
				}
			})
		}
	}
}
