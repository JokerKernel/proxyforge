package app

import (
	"context"
	"os"
	"testing"
)

func TestNativeGlobalCleanupPreservesIndependentDockerNamespace(t *testing.T) {
	r := &fakeRunner{missingBinary: true, serviceStopped: true, unitRemoved: true}
	a, _ := testApp(t, r)
	path := a.Layout.Resolve("/var/lib/proxyforge/docker/xray/state.json")
	if err := os.MkdirAll(a.Layout.Resolve("/var/lib/proxyforge/docker/xray"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("docker-state"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := a.Cleanup(context.Background(), "all"); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil || string(b) != "docker-state" {
		t.Fatal("native cleanup erased independent Docker state")
	}
}
