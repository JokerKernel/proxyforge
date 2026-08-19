package system

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestResolverAddressesFollowsResolvedStub(t *testing.T) {
	dir := t.TempDir()
	stub := filepath.Join(dir, "resolv.conf")
	upstream := filepath.Join(dir, "resolved.conf")
	if err := os.WriteFile(stub, []byte("nameserver 127.0.0.53\noptions edns0\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(upstream, []byte("# resolved\nnameserver 1.1.1.1\nnameserver 8.8.8.8\nnameserver 1.1.1.1\n"), 0600); err != nil {
		t.Fatal(err)
	}
	origResolv, origResolved := resolvConfFile, resolvedConfFile
	resolvConfFile, resolvedConfFile = stub, upstream
	t.Cleanup(func() {
		resolvConfFile, resolvedConfFile = origResolv, origResolved
	})
	got := ResolverAddresses()
	want := []string{"1.1.1.1", "8.8.8.8"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("resolvers=%v, want %v", got, want)
	}
}

func TestResolverAddressesKeepsDirectNameservers(t *testing.T) {
	dir := t.TempDir()
	stub := filepath.Join(dir, "resolv.conf")
	if err := os.WriteFile(stub, []byte("search example.com\nnameserver 223.5.5.5\nnameserver 2400:3200::1\n"), 0600); err != nil {
		t.Fatal(err)
	}
	origResolv, origResolved := resolvConfFile, resolvedConfFile
	resolvConfFile, resolvedConfFile = stub, filepath.Join(dir, "missing")
	t.Cleanup(func() {
		resolvConfFile, resolvedConfFile = origResolv, origResolved
	})
	got := ResolverAddresses()
	want := []string{"223.5.5.5", "2400:3200::1"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("resolvers=%v, want %v", got, want)
	}
}
