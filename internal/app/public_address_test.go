package app

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"reflect"
	"strings"
	"testing"
)

type addressTransport func(*http.Request) (*http.Response, error)

func (f addressTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

type addressBody struct {
	io.Reader
	closed bool
}

func (b *addressBody) Close() error {
	b.closed = true
	return nil
}

func TestDiscoverPublicAddressValidatesCompleteResponse(t *testing.T) {
	for _, tt := range []struct {
		name, body, want string
		status           int
	}{
		{"IPv4", " 1.1.1.1\n", "1.1.1.1", http.StatusOK},
		{"IPv6", "2606:4700:4700::1111\n", "2606:4700:4700::1111", http.StatusOK},
		{"hostname", "unexpected.example.com", "", http.StatusOK},
		{"empty", "", "", http.StatusOK},
		{"invalid IP", "999.1.1.1", "", http.StatusOK},
		{"truncated suffix", "1.1.1.1" + strings.Repeat(" ", 128) + "invalid", "", http.StatusOK},
		{"HTTP failure", "1.1.1.1", "", http.StatusBadGateway},
	} {
		t.Run(tt.name, func(t *testing.T) {
			body := &addressBody{Reader: strings.NewReader(tt.body)}
			client := &http.Client{Transport: addressTransport(func(req *http.Request) (*http.Response, error) {
				if req.URL.String() != "https://api.ipify.org" || req.Method != http.MethodGet {
					t.Fatalf("unexpected request: %s %s", req.Method, req.URL)
				}
				return &http.Response{StatusCode: tt.status, Status: http.StatusText(tt.status), Body: body}, nil
			})}
			got, err := discoverPublicAddress(context.Background(), client)
			if got != tt.want || (err != nil) != (tt.want == "") {
				t.Fatalf("address = %q, error = %v; want %q", got, err, tt.want)
			}
			if !body.closed {
				t.Fatal("response body was not closed")
			}
		})
	}
}

func TestDiscoverPublicAddressPreservesRequestErrors(t *testing.T) {
	client := &http.Client{Transport: addressTransport(func(req *http.Request) (*http.Response, error) {
		return nil, req.Context().Err()
	})}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := discoverPublicAddress(ctx, client); !errors.Is(err, context.Canceled) {
		t.Fatalf("request error = %v; want canceled context", err)
	}
	if _, err := discoverPublicAddress(nil, client); err == nil {
		t.Fatal("expected an error for a request with nil context")
	}
}

func TestPhysicalInterfaceAddressesFiltersAndOrdersAddresses(t *testing.T) {
	candidates := []interfaceAddressCandidate{
		{name: "lo", up: true, loopback: true, physical: true, addresses: []net.IP{net.ParseIP("1.1.1.1")}},
		{name: "docker0", up: true, physical: false, addresses: []net.IP{net.ParseIP("8.8.8.8")}},
		{name: "eth0", up: true, physical: true, addresses: []net.IP{net.ParseIP("10.0.0.2"), net.ParseIP("2606:4700:4700::1111")}},
		{name: "eth1", up: true, physical: true, addresses: []net.IP{net.ParseIP("203.0.113.10"), net.ParseIP("9.9.9.9"), net.ParseIP("2606:4700:4700::1111")}},
	}
	want := []PublicInterfaceAddress{
		{Interface: "eth1", Address: "9.9.9.9"},
		{Interface: "eth0", Address: "2606:4700:4700::1111"},
		{Interface: "eth0", Address: "10.0.0.2", Private: true},
		{Interface: "eth1", Address: "203.0.113.10", Private: true},
	}
	if got := physicalInterfaceAddresses(candidates); !reflect.DeepEqual(got, want) {
		t.Fatalf("physical addresses=%#v, want %#v", got, want)
	}
}

func TestPhysicalInterfaceAddressesKeepsPrivateWhenOnlyPublicIsIPv6(t *testing.T) {
	candidates := []interfaceAddressCandidate{{
		name: "eth0", up: true, physical: true,
		addresses: []net.IP{net.ParseIP("192.168.1.2"), net.ParseIP("2606:4700:4700::1111")},
	}}
	want := []PublicInterfaceAddress{
		{Interface: "eth0", Address: "2606:4700:4700::1111"},
		{Interface: "eth0", Address: "192.168.1.2", Private: true},
	}
	if got := physicalInterfaceAddresses(candidates); !reflect.DeepEqual(got, want) {
		t.Fatalf("physical addresses=%#v, want %#v", got, want)
	}
}
