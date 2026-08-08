package domain

import (
	"net/netip"
	"testing"
)

func TestBlockedDestinationCIDRsCoverInternalNetworks(t *testing.T) {
	prefixes := make([]netip.Prefix, 0, len(blockedDestinationCIDRs))
	for _, raw := range BlockedDestinationCIDRs() {
		prefix, err := netip.ParsePrefix(raw)
		if err != nil {
			t.Fatalf("invalid blocked CIDR %q: %v", raw, err)
		}
		prefixes = append(prefixes, prefix)
	}
	for _, raw := range []string{
		"10.0.0.1", "100.100.100.200", "127.0.0.1", "169.254.169.254",
		"172.31.255.255", "192.168.1.1", "198.18.0.1", "::1", "fd00:ec2::254", "fe80::1",
	} {
		address := netip.MustParseAddr(raw)
		blocked := false
		for _, prefix := range prefixes {
			if prefix.Contains(address) {
				blocked = true
				break
			}
		}
		if !blocked {
			t.Errorf("internal address %s is not blocked", address)
		}
	}
	for _, raw := range []string{"1.1.1.1", "8.8.8.8", "2606:4700:4700::1111"} {
		address := netip.MustParseAddr(raw)
		for _, prefix := range prefixes {
			if prefix.Contains(address) {
				t.Errorf("public address %s unexpectedly blocked by %s", address, prefix)
			}
		}
	}
}

func TestBlockedDestinationCIDRsReturnsCopy(t *testing.T) {
	first := BlockedDestinationCIDRs()
	first[0] = "1.1.1.1/32"
	if BlockedDestinationCIDRs()[0] == first[0] {
		t.Fatal("caller mutated global network policy")
	}
}
