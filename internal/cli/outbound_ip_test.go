package cli

import (
	"bufio"
	"bytes"
	"context"
	"strings"
	"testing"

	"proxyforge/internal/app"
	"proxyforge/internal/domain"
	"proxyforge/internal/provider"
	"proxyforge/internal/system"
)

func TestOutboundIPDisplayAndModifyMenu(t *testing.T) {
	if got := outboundIPDisplay(provider.OutboundIPPreferIPv4); got != "优先 IPv4" {
		t.Fatalf("display=%q", got)
	}
	if got := outboundIPDisplay(provider.OutboundIPIPv6Only); got != "仅 IPv6" {
		t.Fatalf("display=%q", got)
	}
	if got := outboundIPChoiceDisplay(provider.OutboundIPUnset); got != "恢复默认（双栈）" {
		t.Fatalf("choice display=%q", got)
	}
	if got := outboundIPChoiceTitle(provider.OutboundIPUnset); got != "恢复默认" {
		t.Fatalf("unset title=%q", got)
	}

	var badgeOut bytes.Buffer
	(&commandSet{out: &badgeOut}).printMenuBadgeChoice("1", "优先 IPv4", "[当前]")
	if !strings.Contains(badgeOut.String(), "优先 IPv4") || !strings.Contains(badgeOut.String(), "[当前]") {
		t.Fatalf("badge line=%q", badgeOut.String())
	}

	var out bytes.Buffer
	c := &commandSet{reader: bufio.NewReader(strings.NewReader("0\n")), out: &out}
	if err := c.modifyConfigMenu(context.Background(), domain.CoreSingBox); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "2   出站 IP") || !strings.Contains(out.String(), "优先或仅使用 IPv4") {
		t.Fatalf("modify menu=%q", out.String())
	}
	if strings.Contains(out.String(), "回落 IP") {
		t.Fatalf("fallback IP shown without fallback: %q", out.String())
	}
}

func TestModifyConfigMenuShowsFallbackIPWhenEnabled(t *testing.T) {
	store := system.StateStore{Layout: system.Layout{Root: t.TempDir()}}
	if err := store.Save(domain.NodeSpec{
		ManagedBy: "proxyforge", Core: domain.CoreXray, XrayFallbackGuard: true, XrayFallbackPort: domain.DefaultXrayFallbackPort,
	}); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	c := &commandSet{
		app:    &app.App{Store: store},
		reader: bufio.NewReader(strings.NewReader("0\n")),
		out:    &out,
	}
	if err := c.modifyConfigMenu(context.Background(), domain.CoreXray); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "2   出站 IP") || !strings.Contains(out.String(), "3   回落 IP") ||
		!strings.Contains(out.String(), "4   重置 SNI") || !strings.Contains(out.String(), "6   REALITY SNI") {
		t.Fatalf("fallback modify menu=%q", out.String())
	}
}
