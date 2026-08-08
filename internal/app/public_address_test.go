package app

import (
	"net"
	"testing"
)

func TestPreferredPhysicalPublicAddress(t *testing.T) {
	candidates := []interfaceAddressCandidate{
		{name: "lo", up: true, loopback: true, physical: true, addresses: []net.IP{net.ParseIP("1.1.1.1")}},
		{name: "docker0", up: true, physical: false, addresses: []net.IP{net.ParseIP("8.8.8.8")}},
		{name: "eth0", up: true, physical: true, addresses: []net.IP{net.ParseIP("10.0.0.2"), net.ParseIP("2606:4700:4700::1111")}},
		{name: "eth1", up: true, physical: true, addresses: []net.IP{net.ParseIP("203.0.113.10"), net.ParseIP("9.9.9.9")}},
	}
	if got := preferredPhysicalPublicAddress(candidates); got != "9.9.9.9" {
		t.Fatalf("preferred address=%q", got)
	}
}

func TestPreferredPhysicalPublicAddressFallsBackToIPv6(t *testing.T) {
	candidates := []interfaceAddressCandidate{{
		name: "eth0", up: true, physical: true,
		addresses: []net.IP{net.ParseIP("192.168.1.2"), net.ParseIP("2606:4700:4700::1111")},
	}}
	if got := preferredPhysicalPublicAddress(candidates); got != "2606:4700:4700::1111" {
		t.Fatalf("preferred address=%q", got)
	}
}
