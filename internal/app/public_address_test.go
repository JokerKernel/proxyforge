package app

import (
	"net"
	"reflect"
	"testing"
)

func TestPhysicalPublicAddressesFiltersAndOrdersAddresses(t *testing.T) {
	candidates := []interfaceAddressCandidate{
		{name: "lo", up: true, loopback: true, physical: true, addresses: []net.IP{net.ParseIP("1.1.1.1")}},
		{name: "docker0", up: true, physical: false, addresses: []net.IP{net.ParseIP("8.8.8.8")}},
		{name: "eth0", up: true, physical: true, addresses: []net.IP{net.ParseIP("10.0.0.2"), net.ParseIP("2606:4700:4700::1111")}},
		{name: "eth1", up: true, physical: true, addresses: []net.IP{net.ParseIP("203.0.113.10"), net.ParseIP("9.9.9.9"), net.ParseIP("2606:4700:4700::1111")}},
	}
	want := []PublicInterfaceAddress{
		{Interface: "eth1", Address: "9.9.9.9"},
		{Interface: "eth0", Address: "2606:4700:4700::1111"},
	}
	if got := physicalPublicAddresses(candidates); !reflect.DeepEqual(got, want) {
		t.Fatalf("physical addresses=%#v, want %#v", got, want)
	}
}

func TestPhysicalPublicAddressesCanReturnOnlyIPv6(t *testing.T) {
	candidates := []interfaceAddressCandidate{{
		name: "eth0", up: true, physical: true,
		addresses: []net.IP{net.ParseIP("192.168.1.2"), net.ParseIP("2606:4700:4700::1111")},
	}}
	want := []PublicInterfaceAddress{{Interface: "eth0", Address: "2606:4700:4700::1111"}}
	if got := physicalPublicAddresses(candidates); !reflect.DeepEqual(got, want) {
		t.Fatalf("physical addresses=%#v, want %#v", got, want)
	}
}
