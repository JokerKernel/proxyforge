package cli

import (
	"strings"
	"testing"

	"proxyforge/internal/domain"
	"proxyforge/internal/provider"
)

func TestDNSProfileDisplayIncludesEncryptedOptions(t *testing.T) {
	for _, profile := range []string{provider.DNSProfileDoHCloudflare, provider.DNSProfileDoHGoogle} {
		got := dnsProfileDisplay(domain.CoreSingBox, profile)
		if !strings.Contains(got, "加密 DNS/DoH") {
			t.Fatalf("profile %q display=%q", profile, got)
		}
	}
}
