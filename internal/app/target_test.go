package app

import (
	"crypto/tls"
	"errors"
	"net"
	"testing"
	"time"
)

func TestIPFamiliesSplitsResolvedAddresses(t *testing.T) {
	has4, has6 := ipFamilies([]net.IPAddr{
		{IP: net.ParseIP("1.1.1.1")},
		{IP: net.ParseIP("2606:4700:4700::1111")},
	})
	if !has4 || !has6 {
		t.Fatalf("dual stack has4=%v has6=%v", has4, has6)
	}
	has4, has6 = ipFamilies([]net.IPAddr{{IP: net.ParseIP("8.8.8.8")}})
	if !has4 || has6 {
		t.Fatalf("ipv4 only has4=%v has6=%v", has4, has6)
	}
	has4, has6 = ipFamilies([]net.IPAddr{{IP: net.ParseIP("2001:4860:4860::8888")}})
	if has4 || !has6 {
		t.Fatalf("ipv6 only has4=%v has6=%v", has4, has6)
	}
}

func TestCombineFamilyProbeErrors(t *testing.T) {
	err := combineFamilyProbeErrors(true, errors.New("v4 down"), true, errors.New("v6 down"))
	if err == nil || err.Error() != "REALITY target IPv4：v4 down；IPv6：v6 down" {
		t.Fatalf("combined=%v", err)
	}
	only4 := combineFamilyProbeErrors(true, errors.New("v4 down"), false, nil)
	if only4 == nil || only4.Error() != "v4 down" {
		t.Fatalf("ipv4 only=%v", only4)
	}
}

func TestPickFamilyTLSStatePrefersFasterHandshake(t *testing.T) {
	v4 := &tls.ConnectionState{Version: tls.VersionTLS13}
	v6 := &tls.ConnectionState{Version: tls.VersionTLS12}
	if got := pickFamilyTLSState(v4, 5*time.Millisecond, v6, 20*time.Millisecond); got != v4 {
		t.Fatalf("faster ipv4=%v", got)
	}
	if got := pickFamilyTLSState(v4, 30*time.Millisecond, v6, 10*time.Millisecond); got != v6 {
		t.Fatalf("faster ipv6=%v", got)
	}
	if got := pickFamilyTLSState(v4, 5*time.Millisecond, nil, 0); got != v4 {
		t.Fatalf("ipv4 only=%v", got)
	}
	if pickFamilyTLSState(nil, 0, nil, 0) != nil {
		t.Fatal("expected no state")
	}
}

func TestForbiddenTargetIP(t *testing.T) {
	for _, raw := range []string{"127.0.0.1", "10.0.0.1", "100.64.0.1", "192.0.2.1", "198.51.100.1", "203.0.113.1", "2001:db8::1"} {
		if !forbiddenTargetIP(net.ParseIP(raw)) {
			t.Errorf("expected %s forbidden", raw)
		}
	}
	for _, raw := range []string{"1.1.1.1", "2606:4700:4700::1111"} {
		if forbiddenTargetIP(net.ParseIP(raw)) {
			t.Errorf("expected %s public", raw)
		}
	}
}
