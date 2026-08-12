package app

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"proxyforge/internal/domain"
	"proxyforge/internal/provider"
	"proxyforge/internal/provider/singbox"
	"proxyforge/internal/provider/xray"
	"proxyforge/internal/system"
)

type serviceUserRunner struct {
	unitPath        string
	user            string
	accountExists   bool
	active          bool
	failNextRestart bool
	calls           []string
}

func (r *serviceUserRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	r.calls = append(r.calls, name+" "+strings.Join(args, " "))
	if name == "systemctl" && len(args) > 0 {
		switch args[0] {
		case "show":
			if strings.Contains(strings.Join(args, " "), "LoadState") {
				return []byte("loaded\n"), nil
			}
			return []byte(r.user + "\n"), nil
		case "is-active":
			if r.active {
				return []byte("active\n"), nil
			}
			return []byte("inactive\n"), errors.New("inactive")
		case "daemon-reload":
			data, err := os.ReadFile(r.unitPath)
			if err != nil {
				return nil, err
			}
			r.user = serviceUserInUnit(data)
			return nil, nil
		case "restart":
			if r.failNextRestart {
				r.failNextRestart = false
				return nil, errors.New("injected restart failure")
			}
			r.active = true
			return nil, nil
		}
	}
	if name == "id" {
		if !r.accountExists {
			return nil, errors.New("unknown user")
		}
		return []byte("999\n"), nil
	}
	if name == "useradd" {
		r.accountExists = true
	}
	return nil, nil
}

func serviceUserInUnit(data []byte) string {
	user := "root"
	section := ""
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			section = strings.Trim(line, "[]")
			continue
		}
		if section == "Service" && strings.HasPrefix(line, "User=") {
			user = strings.TrimSpace(strings.TrimPrefix(line, "User="))
		}
	}
	return user
}

func newServiceUserTestApp(t *testing.T, failRestart bool) (*App, *serviceUserRunner, string) {
	t.Helper()
	root := t.TempDir()
	mainUnit := filepath.Join(root, "etc/systemd/system/xray.service")
	templateUnit := filepath.Join(root, "etc/systemd/system/xray@.service")
	unit := []byte("[Unit]\nDescription=Xray Service\n[Service]\nUser=nobody\nExecStart=/usr/local/bin/xray run\n")
	for _, path := range []string{mainUnit, templateUnit} {
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, unit, 0644); err != nil {
			t.Fatal(err)
		}
	}
	config := filepath.Join(root, "usr/local/etc/xray/config.json")
	if err := os.MkdirAll(filepath.Dir(config), 0750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(config, []byte("{}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	runner := &serviceUserRunner{
		unitPath: mainUnit, user: "nobody", active: true, failNextRestart: failRestart,
	}
	a := New(provider.NewRegistry(singbox.New(), xray.New()), runner, system.Layout{Root: root}, nil)
	a.RootCheck = func() error { return nil }
	a.LookPath = func(string) (string, error) { return "/usr/local/bin/xray", nil }
	return a, runner, root
}

func TestUseDedicatedXrayServiceUserMigratesOfficialUnits(t *testing.T) {
	a, runner, root := newServiceUserTestApp(t, false)
	change, err := a.UseDedicatedXrayServiceUser(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !change.Changed || !change.Restarted || !change.UserCreated || change.Previous != "nobody" || change.Current != "xray" {
		t.Fatalf("change=%#v", change)
	}
	for _, relative := range []string{"etc/systemd/system/xray.service", "etc/systemd/system/xray@.service"} {
		data, err := os.ReadFile(filepath.Join(root, relative))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(data), "User=nobody") || !strings.Contains(string(data), "User=xray") {
			t.Fatalf("unit %s was not migrated: %s", relative, data)
		}
	}
	dropIn, err := os.ReadFile(filepath.Join(root, "etc/systemd/system/xray.service.d/20-proxyforge-user.conf"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"User=xray", "Group=xray"} {
		if !strings.Contains(string(dropIn), want) {
			t.Fatalf("drop-in missing %q: %s", want, dropIn)
		}
	}
	calls := strings.Join(runner.calls, "\n")
	for _, want := range []string{
		"groupadd -r -f xray",
		"useradd -r -g xray -d /nonexistent -s /usr/sbin/nologin -M xray",
		"systemctl daemon-reload",
		"systemctl restart xray.service",
	} {
		if !strings.Contains(calls, want) {
			t.Fatalf("calls missing %q:\n%s", want, calls)
		}
	}
}

func TestUseDedicatedXrayServiceUserRollsBackWhenRestartFails(t *testing.T) {
	a, runner, root := newServiceUserTestApp(t, true)
	_, err := a.UseDedicatedXrayServiceUser(context.Background())
	if err == nil || !strings.Contains(err.Error(), "已恢复旧 systemd 设置") {
		t.Fatalf("error=%v", err)
	}
	data, readErr := os.ReadFile(filepath.Join(root, "etc/systemd/system/xray.service"))
	if readErr != nil {
		t.Fatal(readErr)
	}
	if !strings.Contains(string(data), "User=nobody") {
		t.Fatalf("main unit was not restored: %s", data)
	}
	if _, statErr := os.Stat(filepath.Join(root, "etc/systemd/system/xray.service.d/20-proxyforge-user.conf")); !os.IsNotExist(statErr) {
		t.Fatalf("drop-in should be removed on rollback: %v", statErr)
	}
	if runner.user != "nobody" || !runner.active {
		t.Fatalf("service state user=%q active=%v", runner.user, runner.active)
	}
}

func TestReplaceSystemdServiceUser(t *testing.T) {
	input := []byte("[Unit]\nDescription=test\n[Service]\n  User = nobody\nExecStart=/bin/true\n")
	got, changed, err := replaceSystemdServiceUser(input, XrayDedicatedServiceUser)
	if err != nil || !changed {
		t.Fatalf("changed=%v error=%v", changed, err)
	}
	if strings.Contains(string(got), "nobody") || !strings.Contains(string(got), "  User=xray\n") {
		t.Fatalf("result=%q", got)
	}
	if _, _, err := replaceSystemdServiceUser([]byte("[Service]\nExecStart=/bin/true\n"), "xray"); err == nil {
		t.Fatal("missing User= should fail")
	}
}

func TestXrayServiceUserRequiresInstalledCore(t *testing.T) {
	a := &App{
		Registry:  provider.NewRegistry(xray.New()),
		RootCheck: func() error { return nil },
		LookPath:  func(string) (string, error) { return "", exec.ErrNotFound },
	}
	if _, err := a.XrayServiceUser(context.Background()); err == nil || !strings.Contains(err.Error(), domain.CoreXray) {
		t.Fatalf("error=%v", err)
	}
}
