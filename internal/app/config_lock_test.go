package app

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"sync"
	"testing"
	"time"

	"proxyforge/internal/domain"
	"proxyforge/internal/provider"
)

func TestManagedConfigWritersHonorPolicyLock(t *testing.T) {
	for _, core := range []string{domain.CoreXray, domain.CoreSingBox} {
		t.Run(core, func(t *testing.T) {
			runner := &fakeRunner{port: freePort(t)}
			a, _ := testApp(t, runner)
			ctx := context.Background()
			opts := domain.GenerateOptions{Server: "server.example.com", Port: runner.port, SNI: "example.com", StandardConfig: true}
			if _, err := a.Generate(ctx, core, opts); err != nil {
				t.Fatal(err)
			}
			p, _ := a.Registry.Get(core)
			paths := a.accessTransactionFiles(p, core)
			before := make([][]byte, len(paths))
			for i, path := range paths {
				before[i], _ = os.ReadFile(path)
			}
			unlock, err := a.lockAccessPolicy(ctx, core)
			if err != nil {
				t.Fatal(err)
			}
			defer unlock()
			for name, update := range map[string]func(context.Context) error{
				"DNS": func(ctx context.Context) error {
					_, err := a.SetDNSProfile(ctx, core, provider.DNSProfilePublicGoogle)
					return err
				},
				"outbound IP": func(ctx context.Context) error {
					_, err := a.SetOutboundIPStrategy(ctx, core, provider.OutboundIPPreferIPv4)
					return err
				},
				"fallback IP": func(ctx context.Context) error {
					_, err := a.SetFallbackIPStrategy(ctx, core, provider.OutboundIPPreferIPv4)
					return err
				},
				"log":      func(ctx context.Context) error { _, err := a.SetLogLevel(ctx, core, "info"); return err },
				"generate": func(ctx context.Context) error { _, err := a.Generate(ctx, core, opts); return err },
				"reset": func(ctx context.Context) error {
					_, err := a.ResetCredentials(ctx, core, domain.ResetOptions{Credentials: true})
					return err
				},
			} {
				t.Run(name, func(t *testing.T) {
					blocked, cancel := context.WithTimeout(ctx, 10*time.Millisecond)
					defer cancel()
					if err := update(blocked); !errors.Is(err, context.DeadlineExceeded) {
						t.Fatalf("writer bypassed policy lock: %v", err)
					}
					for i, path := range paths {
						after, err := os.ReadFile(path)
						if err != nil || !bytes.Equal(before[i], after) {
							t.Fatalf("cancelled writer changed %s: %v", path, err)
						}
					}
				})
			}
		})
	}
}

func TestConcurrentDNSAndRelayPolicyUpdatesKeepBothChanges(t *testing.T) {
	for _, core := range []string{domain.CoreXray, domain.CoreSingBox} {
		t.Run(core, func(t *testing.T) {
			runner := &fakeRunner{port: freePort(t)}
			a, _ := testApp(t, runner)
			a.Out, a.Progress = io.Discard, io.Discard
			ctx := context.Background()
			n, err := a.Generate(ctx, core, domain.GenerateOptions{Server: "server.example.com", Port: runner.port, SNI: "example.com", StandardConfig: true})
			if err != nil {
				t.Fatal(err)
			}
			peer := domain.LandingPeer{Core: core, Server: "exit.example.com", Port: 443, SNI: n.SNI, UUID: n.UUID, PublicKey: n.PublicKey, ShortID: n.ShortID, Flow: domain.VisionFlow}
			if _, err := a.AddRelayLink(ctx, core, "relay", peer, RelayAddOptions{}); err != nil {
				t.Fatal(err)
			}
			policy := domain.AccessPolicy{Mode: domain.AccessModeWhitelist, Whitelist: []domain.AccessEntry{{Type: "domain", Value: "allowed.test", Ports: "443"}}}
			var wg sync.WaitGroup
			results := make(chan error, 2)
			start := make(chan struct{})
			for _, update := range []func() error{
				func() error { _, err := a.SetDNSProfile(ctx, core, provider.DNSProfilePublicGoogle); return err },
				func() error { return a.SetRelayAccessPolicy(ctx, core, "relay", policy) },
			} {
				wg.Add(1)
				go func() { defer wg.Done(); <-start; results <- update() }()
			}
			close(start)
			wg.Wait()
			close(results)
			for err := range results {
				if err != nil {
					t.Fatal(err)
				}
			}
			got, err := a.RelayAccessPolicy(core, "relay")
			if err != nil || got.Disabled || len(got.Whitelist) != 1 || got.Whitelist[0].Value != "allowed.test" {
				t.Fatalf("DNS update lost relay policy: %+v, %v", got, err)
			}
			dns, err := a.DNSSettings(ctx, core)
			if err != nil || dns.Current != provider.DNSProfilePublicGoogle {
				t.Fatalf("relay policy update lost DNS setting: %+v, %v", dns, err)
			}
		})
	}
}
