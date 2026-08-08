package app

import (
	"net"
	"testing"
)

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
