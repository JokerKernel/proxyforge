package cli

import (
	"strings"
	"testing"

	"proxyforge/internal/domain"
	"proxyforge/internal/provider"
)

func TestDNSCardDisplayIncludesConcreteAddresses(t *testing.T) {
	got := dnsCardDisplay(domain.CoreXray, provider.DNSProfileSystem, []string{"1.1.1.1", "8.8.8.8"})
	if !strings.Contains(got, "系统 DNS") || !strings.Contains(got, "1.1.1.1") || !strings.Contains(got, "8.8.8.8") {
		t.Fatalf("card=%q", got)
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
