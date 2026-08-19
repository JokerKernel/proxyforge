package cli

import (
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
	if got != "Cloudflare DNS · 1.1.1.1, 8.8.8.8" {
		t.Fatalf("public card=%q", got)
	}
	got = dnsCardDisplay(domain.CoreXray, provider.DNSProfilePublicGoogle, []string{"8.8.8.8", "1.1.1.1"})
	if got != "Google DNS · 8.8.8.8, 1.1.1.1" {
		t.Fatalf("google card=%q", got)
	}
}

func TestDNSProfileDisplayUsesProviderNames(t *testing.T) {
	if got := dnsProfileDisplay(domain.CoreXray, provider.DNSProfilePublicCloudflare); got != "Cloudflare DNS（1.1.1.1 + 8.8.8.8）" {
		t.Fatalf("cloudflare=%q", got)
	}
	if got := dnsProfileDisplay(domain.CoreXray, provider.DNSProfilePublicGoogle); got != "Google DNS（8.8.8.8 + 1.1.1.1）" {
		t.Fatalf("google=%q", got)
	}
	if got := dnsProfileDisplay(domain.CoreXray, provider.DNSProfileDoHCloudflare); got != "Cloudflare DoH（同时配置 Google）" {
		t.Fatalf("cloudflare doh=%q", got)
	}
	if got := dnsProfileDisplay(domain.CoreSingBox, provider.DNSProfileDoHGoogle); got != "Google DoH" {
		t.Fatalf("sing-box google doh=%q", got)
	}
}
