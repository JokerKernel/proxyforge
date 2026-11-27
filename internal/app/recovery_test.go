package app

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"proxyforge/internal/domain"
	"proxyforge/internal/provider"
	"proxyforge/internal/provider/singbox"
	"proxyforge/internal/provider/xray"
	"proxyforge/internal/system"
)

func TestConfigRecoveryFailures(t *testing.T) {
	for _, p := range []provider.CoreProvider{xray.New(), singbox.New()} {
		for _, setting := range []bool{false, true} {
			flow := "generate"
			if setting {
				flow = "setting"
			}
			for _, scenario := range []string{"restart-fails", "canceled", "restore-fails", "inactive-apply", "inactive-recovery"} {
				t.Run(p.Name()+"/"+flow+"/"+scenario, func(t *testing.T) {
					layout := system.Layout{Root: t.TempDir()}
					path := layout.Resolve(p.ConfigPath())
					old := []byte(`{"manual":"original"}`)
					if err := system.AtomicWrite(path, old, 0600); err != nil {
						t.Fatal(err)
					}
					ctx, cancel := context.WithCancel(context.Background())
					defer cancel()
					cause, restoreErr := errors.New("apply failed"), errors.New("recovery restart failed")
					restarts := 0
					runner := configValidationRunner(func(commandCtx context.Context, name string, args ...string) ([]byte, error) {
						if name != "systemctl" {
							return nil, nil
						}
						switch args[0] {
						case "show":
							return []byte("root"), nil
						case "is-active":
							if (restarts == 1 && scenario == "inactive-apply") || (restarts == 2 && scenario == "inactive-recovery") {
								return []byte("inactive"), nil
							}
							return []byte("active"), nil
						case "restart":
							restarts++
							if restarts == 1 {
								if scenario == "canceled" {
									cancel()
								}
								if scenario == "restore-fails" {
									if err := os.Remove(path); err != nil {
										t.Fatal(err)
									}
									if err := os.Mkdir(path, 0700); err != nil {
										t.Fatal(err)
									}
								}
								if scenario == "inactive-apply" {
									return nil, nil
								}
								return nil, cause
							}
							if commandCtx.Err() != nil {
								t.Fatalf("recovery context canceled: %v", commandCtx.Err())
							}
							if deadline, ok := commandCtx.Deadline(); !ok || time.Until(deadline) > 30*time.Second {
								t.Fatal("recovery has no bounded deadline")
							}
							got, err := os.ReadFile(path)
							if err != nil || !bytes.Equal(got, old) {
								t.Fatalf("restarted without original configuration: %s, %v", got, err)
							}
							if scenario == "restart-fails" {
								return nil, restoreErr
							}
						}
						return nil, nil
					})
					a := New(provider.NewRegistry(p), runner, layout, io.Discard)
					node := domain.NodeSpec{Core: p.Name(), Port: 443}
					var err error
					if setting {
						_, err = a.applyServerSetting(ctx, p, p.Name(), "test", old, []byte(`{"manual":"patched"}`))
					} else {
						_, err = a.applyServerConfig(ctx, p, p.Name(), node, node, true, []byte(`{"manual":"patched"}`), false)
					}
					if err == nil || strings.Contains(err.Error(), "%!") {
						t.Fatalf("unexpected error: %v", err)
					}
					if scenario != "inactive-apply" && !errors.Is(err, cause) {
						t.Fatalf("lost original error: %v", err)
					}
					if scenario == "restart-fails" && !errors.Is(err, restoreErr) {
						t.Fatalf("lost recovery error: %v", err)
					}
					if scenario == "restore-fails" {
						var pathErr *os.LinkError
						if restarts != 1 || !errors.As(err, &pathErr) {
							t.Fatalf("unsafe restart or lost filesystem error: restarts=%d, %v", restarts, err)
						}
					} else if restarts != 2 {
						t.Fatalf("recovery restart not attempted: %d", restarts)
					}
					incomplete := scenario == "restart-fails" || scenario == "restore-fails" || scenario == "inactive-recovery"
					if strings.Contains(err.Error(), "回滚未完整完成") != incomplete || (incomplete && strings.Contains(err.Error(), "已恢复")) {
						t.Fatalf("incorrect recovery result: %v", err)
					}
				})
			}
		}
	}
}

func TestRecoveryContextRetainsValuesAndReleasesResources(t *testing.T) {
	type contextKey struct{}
	original, cancel := context.WithCancel(context.WithValue(context.Background(), contextKey{}, "value"))
	cancel()
	ctx, release := recoveryContext(original)
	defer release()
	if ctx.Err() != nil || ctx.Value(contextKey{}) != "value" {
		t.Fatal("recovery did not detach cancellation while retaining context values")
	}
	release()
	if !errors.Is(ctx.Err(), context.Canceled) {
		t.Fatal("recovery context was not released")
	}
}

func TestStoppedServiceRecoverySurvivesCanceledPortCheck(t *testing.T) {
	for _, failStart := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "start-fails"}[failStart], func(t *testing.T) {
			p := xray.New()
			layout := system.Layout{Root: t.TempDir()}
			path := layout.Resolve(p.ConfigPath())
			original := []byte(`{"manual":true}`)
			if err := system.AtomicWrite(path, original, 0600); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			cause, startErr := errors.New("port busy"), errors.New("start failed")
			started := false
			runner := configValidationRunner(func(commandCtx context.Context, name string, args ...string) ([]byte, error) {
				if name != "systemctl" {
					return nil, nil
				}
				switch args[0] {
				case "is-active":
					return []byte("active"), nil
				case "start":
					started = true
					if commandCtx.Err() != nil {
						t.Fatalf("recovery start used canceled context: %v", commandCtx.Err())
					}
					if failStart {
						return nil, startErr
					}
				}
				return nil, nil
			})
			a := New(provider.NewRegistry(p), runner, layout, io.Discard)
			a.PortFree = func(int) error { cancel(); return cause }
			node := domain.NodeSpec{Core: p.Name(), Port: 444}
			old := domain.NodeSpec{Core: p.Name(), Port: 443}
			_, err := a.applyServerConfig(ctx, p, p.Name(), node, old, true, []byte(`{"new":true}`), false)
			if !started || !errors.Is(err, cause) || errors.Is(err, startErr) != failStart {
				t.Fatalf("incorrect service recovery: started=%t, %v", started, err)
			}
			got, readErr := os.ReadFile(path)
			if readErr != nil || !bytes.Equal(got, original) {
				t.Fatalf("configuration changed after failed port check: %s, %v", got, readErr)
			}
		})
	}
}

func TestSecureConfigRejectsInvalidGIDBeforeChangingPermissions(t *testing.T) {
	for _, gid := range []string{"invalid", "", "-1", "4294967295", "9999999999999999999999999999999"} {
		t.Run(gid, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "config.json")
			if err := os.WriteFile(path, []byte("original"), 0600); err != nil {
				t.Fatal(err)
			}
			before, err := readMetadata(path, true)
			if err != nil {
				t.Fatal(err)
			}
			dirBefore, err := readMetadata(dir, true)
			if err != nil {
				t.Fatal(err)
			}
			if err := secureConfigForGroup(path, gid); err == nil || !strings.Contains(err.Error(), "GID") {
				t.Fatalf("unexpected GID error: %v", err)
			}
			after, err := readMetadata(path, true)
			if err != nil || after != before {
				t.Fatalf("file metadata changed: %v, %v", after, err)
			}
			dirAfter, err := readMetadata(dir, true)
			if err != nil || dirAfter != dirBefore {
				t.Fatalf("directory metadata changed: %v, %v", dirAfter, err)
			}
		})
	}
}
