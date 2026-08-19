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
	"proxyforge/internal/provider/xray"
	"proxyforge/internal/system"
)

func TestOutboundIPDisplayAndModifyMenu(t *testing.T) {
	if got := outboundIPDisplay(provider.OutboundIPPreferIPv4); got != "优先 IPv4" {
		t.Fatalf("display=%q", got)
	}
	if got := outboundIPDisplay(provider.OutboundIPIPv6Only); got != "仅 IPv6" {
		t.Fatalf("display=%q", got)
	}
	if got := outboundIPChoiceLabel(domain.CoreXray, provider.OutboundIPUnset); got != "默认（先 IPv4，300ms 后竞速 IPv6）" {
		t.Fatalf("choice label=%q", got)
	}
	if got := outboundIPChoiceHint(domain.CoreXray, provider.OutboundIPPreferIPv4); got != "有 IPv4 后连不上也不改走 IPv6" {
		t.Fatalf("xray prefer hint=%q", got)
	}
	if got := outboundIPChoiceTitle(provider.OutboundIPUnset); got != "默认" {
		t.Fatalf("unset title=%q", got)
	}

	var plain bytes.Buffer
	(&commandSet{out: &plain}).printMenuChoice("1", outboundIPChoiceLabel(domain.CoreXray, provider.OutboundIPPreferIPv4))
	(&commandSet{out: &plain}).printMenuChoice("5", "[当前] "+outboundIPChoiceLabel(domain.CoreXray, provider.OutboundIPUnset))
	plainLines := strings.Split(strings.TrimSuffix(plain.String(), "\n"), "\n")
	if len(plainLines) != 2 {
		t.Fatalf("choice columns not aligned:\n%s", plain.String())
	}
	left0 := strings.Index(plainLines[0], "-- ")
	left1 := strings.Index(plainLines[1], "-- ")
	if left0 < 0 || left1 < 0 ||
		menuDisplayWidth(plainLines[0][:left0]) != menuDisplayWidth(plainLines[1][:left1]) ||
		strings.Index(plainLines[1], "[当前]") > left1 {
		t.Fatalf("choice columns not aligned:\n%s", plain.String())
	}

	var hintOut bytes.Buffer
	c := &commandSet{out: system.NewColorWriter(&hintOut, true)}
	c.printMenuChoice("1", outboundIPChoiceLabel(domain.CoreXray, provider.OutboundIPPreferIPv4))
	if !strings.Contains(hintOut.String(), "\x1b[90m-- 有 IPv4 后连不上也不改走 IPv6") {
		t.Fatalf("prefer hint is not gray: %q", hintOut.String())
	}

	var badgeOut bytes.Buffer
	(&commandSet{out: &badgeOut}).printMenuBadgeChoice("1", "优先 IPv4", "[当前]")
	if !strings.Contains(badgeOut.String(), "优先 IPv4") || !strings.Contains(badgeOut.String(), "[当前]") {
		t.Fatalf("badge line=%q", badgeOut.String())
	}

	var out bytes.Buffer
	c = &commandSet{reader: bufio.NewReader(strings.NewReader("0\n")), out: &out}
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
		ManagedBy: "proxyforge", Core: domain.CoreXray, XrayFallbackGuard: true, XrayFallbackPort: 61431,
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

func TestModifyConfigMenuShowsStatusCard(t *testing.T) {
	layout := system.Layout{Root: t.TempDir()}
	store := system.StateStore{Layout: layout}
	p := xray.New()
	node := domain.NodeSpec{
		ManagedBy: "proxyforge", Core: domain.CoreXray, InboundTag: "xray-one", UserName: "one",
		Server: "203.0.113.10", Port: 443, SNI: "www.example.com", Target: "www.example.com:443",
		UUID: "123e4567-e89b-42d3-a456-426614174000", PrivateKey: "private", PublicKey: "public", ShortID: "abcd",
		XrayFallbackGuard: true, XrayFallbackPort: 61431,
	}
	config, err := p.RenderServer(node)
	if err != nil {
		t.Fatal(err)
	}
	configPath := layout.Resolve(p.ConfigPath())
	if err := system.AtomicWrite(configPath, config, 0600); err != nil {
		t.Fatal(err)
	}
	if err := store.Save(node); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	c := &commandSet{
		app:    &app.App{Registry: provider.NewRegistry(p), Layout: layout, Store: store},
		reader: bufio.NewReader(strings.NewReader("0\n")),
		out:    &out,
	}
	if err := c.modifyConfigMenu(context.Background(), domain.CoreXray); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	for _, text := range []string{
		"╭─ 当前配置",
		"DNS 设置", "系统 DNS",
		"出站 IP", "默认（先 IPv4，300ms 后竞速 IPv6）",
		"回落 IP",
		"SNI", "www.example.com",
	} {
		if !strings.Contains(got, text) {
			t.Fatalf("status card missing %q: %q", text, got)
		}
	}

	patched, err := p.PatchOutboundIPStrategy(config, provider.OutboundIPPreferIPv4)
	if err != nil {
		t.Fatal(err)
	}
	if err := system.AtomicWrite(configPath, patched, 0600); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	c.reader = bufio.NewReader(strings.NewReader("0\n"))
	if err := c.modifyConfigMenu(context.Background(), domain.CoreXray); err != nil {
		t.Fatal(err)
	}
	got = out.String()
	if !strings.Contains(got, "优先 IPv4") || !strings.Contains(got, "默认（先 IPv4，300ms 后竞速 IPv6）") {
		t.Fatalf("patched status card=%q", got)
	}
}
