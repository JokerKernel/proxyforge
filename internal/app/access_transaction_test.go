package app

import (
	"bytes"
	"context"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"proxyforge/internal/domain"
	"proxyforge/internal/system"
)

func TestAccessTransactionRecoversInterruptedWrites(t *testing.T) {
	for _, core := range []string{domain.CoreXray, domain.CoreSingBox} {
		t.Run(core, func(t *testing.T) {
			runner := &fakeRunner{port: freePort(t)}
			a, _ := testApp(t, runner)
			ctx := context.Background()
			old, err := a.Generate(ctx, core, domain.GenerateOptions{Server: "server.example.com", Port: runner.port, SNI: "example.com", StandardConfig: true})
			if err != nil {
				t.Fatal(err)
			}
			p, _ := a.Registry.Get(core)
			paths := a.accessTransactionFiles(p, core)
			original := make([][]byte, len(paths))
			for i, path := range paths {
				original[i], err = os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
			}
			if err := a.beginAccessTransaction(ctx, p, core); err != nil {
				t.Fatal(err)
			}
			// Simulate interruption between individual config/state writes, including
			// an unreadable node JSON. Recovery must run before StateStore.Load.
			for _, path := range paths {
				if err := system.AtomicWrite(path, []byte("interrupted"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := a.AccessPolicy(core); err != nil {
				t.Fatal(err)
			}
			for i, path := range paths {
				got, err := os.ReadFile(path)
				if err != nil || !bytes.Equal(got, original[i]) {
					t.Fatalf("not recovered: %s: %v", path, err)
				}
			}
			if _, err := os.Stat(a.accessTransactionPath(core)); !os.IsNotExist(err) {
				t.Fatal("recovery left its journal")
			}
			got, err := a.Store.Load(core)
			if err != nil || !reflect.DeepEqual(old, got) {
				t.Fatalf("state changed: %v", err)
			}
		})
	}
}

func TestAccessUpdatesRejectStalePreviewAndHonorLockCancellation(t *testing.T) {
	runner := &fakeRunner{port: freePort(t)}
	a, _ := testApp(t, runner)
	ctx := context.Background()
	core := domain.CoreXray
	if _, err := a.Generate(ctx, core, domain.GenerateOptions{Server: "server.example.com", Port: runner.port, SNI: "example.com", StandardConfig: true}); err != nil {
		t.Fatal(err)
	}
	previous, err := a.AccessPolicy(core)
	if err != nil {
		t.Fatal(err)
	}
	next := domain.AccessPolicy{Mode: domain.AccessModeDefault, Blacklist: []domain.AccessEntry{{Type: "ip", Value: "1.2.3.4", Ports: "443"}}}
	if err := a.SetAccessPolicy(ctx, core, next, previous); err != nil {
		t.Fatal(err)
	}
	if err := a.SetAccessPolicy(ctx, core, domain.AccessPolicy{Mode: domain.AccessModeWhitelist}, previous); err == nil || !strings.Contains(err.Error(), "其他操作") {
		t.Fatalf("stale preview overwrote state: %v", err)
	}
	unlock, err := a.lockAccessPolicy(ctx, core)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	lockedCtx, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer cancel()
	if _, err := a.lockAccessPolicy(lockedCtx, core); err == nil {
		t.Fatal("second writer ignored the lock")
	}
}
