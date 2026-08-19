package cli

import (
	"strings"
	"testing"

	"proxyforge/internal/domain"
	"proxyforge/internal/provider"
)

func TestDNSCardDisplayIncludesConcreteAddresses(t *testing.T) {
	got := dnsCardDisplay(domain.CoreXray, provider.DNSProfileSystem, []string{"1.1.1.1", "8.8.8.8"})
	if got != "系统 DNS（推荐） · 1.1.1.1, 8.8.8.8" {
		t.Fatalf("system card=%q", got)
	}
	got = dnsCardDisplay(domain.CoreXray, provider.DNSProfilePublicCloudflare, []string{"1.1.1.1", "8.8.8.8"})
	if got != "公共 DNS（Cloudflare） · 1.1.1.1, 8.8.8.8" {
		t.Fatalf("public card=%q", got)
	}
	if strings.Contains(got, "同时写入") {
		t.Fatalf("card repeated profile details: %q", got)
	}
}

func TestDNSProfileDisplayIncludesEncryptedOptions(t *testing.T) {
	for _, profile := range []string{provider.DNSProfileDoHCloudflare, provider.DNSProfileDoHGoogle} {
		got := dnsProfileDisplay(domain.CoreSingBox, profile)
		if !strings.Contains(got, "加密 DNS/DoH") {
			t.Fatalf("profile %q display=%q", profile, got)
		}
	}
}
