package app

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"proxyforge/internal/provider"
	"proxyforge/internal/provider/singbox"
	"proxyforge/internal/provider/xray"
	"proxyforge/internal/system"
)

type allowTarget struct{}

func (allowTarget) Validate(context.Context, string, string, string) ([]string, error) {
	return nil, nil
}

type fakeRunner struct {
	mu               sync.Mutex
	calls            []string
	port             int
	failRestart      bool
	failEnable       bool
	failDisable      bool
	failRemove       bool
	incompleteRemove bool
	missingBinary    bool
	unitRemoved      bool
	keyGeneration    int
	serviceStopped   bool
	serviceEnabled   bool
}

func (f *fakeRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	call := name + " " + strings.Join(args, " ")
	f.calls = append(f.calls, call)
	if name == "sing-box" && len(args) > 0 && args[0] == "version" {
		if f.missingBinary {
			return nil, errors.New("sing-box not found")
		}
		return []byte("sing-box version 1.14.0\n"), nil
	}
	if name == "sing-box" && len(args) > 1 && args[0] == "generate" {
		f.keyGeneration++
		return []byte(fmt.Sprintf("PrivateKey: private-key-%d\nPublicKey: public-key-%d\n", f.keyGeneration, f.keyGeneration)), nil
	}
	if name == "sing-box" && len(args) > 0 && args[0] == "check" {
		return nil, nil
	}
	if name == "xray" && len(args) > 0 && args[0] == "version" {
		if f.missingBinary {
			return nil, errors.New("xray not found")
		}
		return []byte("Xray 25.1.1\n"), nil
	}
	if name == "xray" && len(args) > 0 && args[0] == "x25519" {
		f.keyGeneration++
		return []byte(fmt.Sprintf("PrivateKey: private-key-%d\nPassword: public-key-%d\n", f.keyGeneration, f.keyGeneration)), nil
	}
	if name == "systemctl" && len(args) > 0 && args[0] == "show" && strings.Contains(strings.Join(args, " "), "LoadState") {
		if f.unitRemoved {
			return []byte("not-found\n"), nil
		}
		return []byte("loaded\n"), nil
	}
	if name == "systemctl" && len(args) > 0 && args[0] == "show" {
		return []byte("root\n"), nil
	}
	if name == "systemctl" && len(args) > 0 && args[0] == "is-active" {
		if f.serviceStopped {
			return []byte("inactive\n"), errors.New("service inactive")
		}
		return []byte("active\n"), nil
	}
	if name == "systemctl" && len(args) > 0 && args[0] == "is-enabled" {
		if f.unitRemoved {
			return []byte("not-found\n"), errors.New("unit not found")
		}
		if f.serviceEnabled {
			return []byte("enabled\n"), nil
		}
		return []byte("disabled\n"), errors.New("service disabled")
	}
	if name == "systemctl" && len(args) > 0 && args[0] == "stop" {
		f.serviceStopped = true
		return nil, nil
	}
	if name == "systemctl" && len(args) > 0 && args[0] == "start" {
		f.serviceStopped = false
		return nil, nil
	}
	if name == "systemctl" && len(args) > 0 && args[0] == "restart" {
		if f.failRestart {
			f.failRestart = false
			return nil, errors.New("injected restart failure")
		}
		f.serviceStopped = false
		return nil, nil
	}
	if name == "systemctl" && len(args) > 0 && args[0] == "enable" {
		if f.failEnable {
			return nil, errors.New("injected enable failure")
		}
		f.serviceEnabled = true
		return nil, nil
	}
	if name == "systemctl" && len(args) > 0 && args[0] == "disable" {
		if f.failDisable {
			return nil, errors.New("injected disable failure")
		}
		f.serviceStopped = true
		f.serviceEnabled = false
		return nil, nil
	}
	if name == "systemctl" && len(args) > 0 && args[0] == "daemon-reload" {
		return nil, nil
	}
	if name == "dpkg" || name == "rpm" {
		if f.failRemove {
			return nil, errors.New("injected package removal failure")
		}
		if !f.incompleteRemove {
			f.missingBinary = true
			f.unitRemoved = true
			f.serviceStopped = true
		}
		return nil, nil
	}
	if name == "sh" {
		return nil, errors.New("not installed")
	}
	return nil, nil
}

func (f *fakeRunner) close() {}

func (f *fakeRunner) callLog() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return strings.Join(f.calls, "\n")
}

func (f *fakeRunner) stopped() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.serviceStopped
}

func testApp(t *testing.T, runner *fakeRunner) (*App, string) {
	t.Helper()
	root := t.TempDir()
	var out bytes.Buffer
	a := New(provider.NewRegistry(singbox.New(), xray.New()), runner, system.Layout{Root: root}, &out)
	a.RootCheck = func() error { return nil }
	a.Targets = allowTarget{}
	a.PortFree = func(int) error { return nil }
	a.Listening = func(context.Context, int, time.Duration) error { return nil }
	a.LookPath = func(name string) (string, error) {
		runner.mu.Lock()
		defer runner.mu.Unlock()
		if runner.missingBinary {
			return "", exec.ErrNotFound
		}
		return "/usr/bin/" + name, nil
	}
	return a, root
}

func freePort(t *testing.T) int {
	t.Helper()
	return 15443
}

func writeSupportedPlatform(t *testing.T, root string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(root, "proc/1"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "etc"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "proc/1/comm"), []byte("systemd\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "etc/os-release"), []byte("ID=debian\n"), 0644); err != nil {
		t.Fatal(err)
	}
}
