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
	invalidAccount  bool
	failUserCheck   bool
	failRunuser     bool
	failIsActive    bool
	active          bool
	restartFailures int
	daemonReloads   int
	failReloadAt    int
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
			if r.failIsActive {
				return nil, errors.New("injected status failure")
			}
			if r.active {
				return []byte("active\n"), nil
			}
			return []byte("inactive\n"), errors.New("inactive")
		case "daemon-reload":
			r.daemonReloads++
			if r.failReloadAt == r.daemonReloads {
				return nil, errors.New("injected daemon-reload failure")
			}
			data, err := os.ReadFile(r.unitPath)
			if err != nil {
				return nil, err
			}
			r.user = serviceUserInUnit(data)
			return nil, nil
		case "restart":
			if r.restartFailures > 0 {
				r.restartFailures--
				r.active = false
				return nil, errors.New("injected restart failure")
			}
			r.active = true
			return nil, nil
		}
	}
	if name == "getent" && len(args) == 2 {
		if !r.accountExists || r.failUserCheck {
			return nil, errors.New("unknown user")
		}
		if args[0] == "passwd" {
			if r.invalidAccount {
				return []byte("xray:x:1000:1000:Xray User:/home/xray:/bin/bash\n"), nil
			}
			return []byte("xray:x:999:999:Xray Service:/nonexistent:/usr/sbin/nologin\n"), nil
		}
		if args[0] == "group" {
			return []byte("xray:x:999:\n"), nil
		}
	}
	if name == "systemd-sysusers" {
		r.accountExists = true
	}
	if name == "runuser" && r.failRunuser {
		return nil, errors.New("injected permission failure")
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
		unitPath: mainUnit, user: "nobody", active: true,
	}
	if failRestart {
		runner.restartFailures = 1
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
		"systemd-sysusers " + filepath.Join(root, "usr/lib/sysusers.d/proxyforge-xray.conf"),
		"getent passwd xray",
		"getent group xray",
		"runuser -u xray --",
		"systemctl daemon-reload",
		"systemctl restart xray.service",
	} {
		if !strings.Contains(calls, want) {
			t.Fatalf("calls missing %q:\n%s", want, calls)
		}
	}
	sysusers, err := os.ReadFile(filepath.Join(root, "usr/lib/sysusers.d/proxyforge-xray.conf"))
	if err != nil {
		t.Fatal(err)
	}
	if string(sysusers) != string(xraySysusersContent) {
		t.Fatalf("sysusers config=%q", sysusers)
	}
	restartsBefore := strings.Count(strings.Join(runner.calls, "\n"), "systemctl restart xray.service")
	second, err := a.UseDedicatedXrayServiceUser(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if second.Changed || second.Restarted || second.UserCreated || second.Current != "xray" {
		t.Fatalf("idempotent change=%#v", second)
	}
	restartsAfter := strings.Count(strings.Join(runner.calls, "\n"), "systemctl restart xray.service")
	if restartsAfter != restartsBefore {
		t.Fatalf("idempotent verification restarted service: before=%d after=%d", restartsBefore, restartsAfter)
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
	if _, statErr := os.Stat(filepath.Join(root, "usr/lib/sysusers.d/proxyforge-xray.conf")); !os.IsNotExist(statErr) {
		t.Fatalf("sysusers config should be removed on rollback: %v", statErr)
	}
	if runner.user != "nobody" || !runner.active {
		t.Fatalf("service state user=%q active=%v", runner.user, runner.active)
	}
}

func TestUseDedicatedXrayServiceUserRejectsUnknownServiceStateBeforeWriting(t *testing.T) {
	a, runner, root := newServiceUserTestApp(t, false)
	runner.failIsActive = true
	_, err := a.UseDedicatedXrayServiceUser(context.Background())
	if err == nil || !strings.Contains(err.Error(), "检查迁移前 xray.service 运行状态") {
		t.Fatalf("error=%v", err)
	}
	data, readErr := os.ReadFile(filepath.Join(root, "etc/systemd/system/xray.service"))
	if readErr != nil {
		t.Fatal(readErr)
	}
	if !strings.Contains(string(data), "User=nobody") {
		t.Fatalf("unit changed after status failure: %s", data)
	}
	if runner.accountExists {
		t.Fatal("account was created after status failure")
	}
	if _, statErr := os.Stat(filepath.Join(root, "etc/systemd/system/xray.service.d/20-proxyforge-user.conf")); !os.IsNotExist(statErr) {
		t.Fatalf("drop-in was written after status failure: %v", statErr)
	}
}

func TestUseDedicatedXrayServiceUserReportsRollbackRestartFailure(t *testing.T) {
	a, runner, root := newServiceUserTestApp(t, false)
	runner.restartFailures = 2
	_, err := a.UseDedicatedXrayServiceUser(context.Background())
	if err == nil || !strings.Contains(err.Error(), "回滚未完整完成") ||
		!strings.Contains(err.Error(), "恢复 xray.service 运行状态") {
		t.Fatalf("error=%v", err)
	}
	data, readErr := os.ReadFile(filepath.Join(root, "etc/systemd/system/xray.service"))
	if readErr != nil {
		t.Fatal(readErr)
	}
	if !strings.Contains(string(data), "User=nobody") {
		t.Fatalf("unit was not restored: %s", data)
	}
	if runner.active {
		t.Fatal("service should remain inactive when rollback restart fails")
	}
}

func TestUseDedicatedXrayServiceUserReportsRollbackDaemonReloadFailure(t *testing.T) {
	a, runner, root := newServiceUserTestApp(t, true)
	runner.failReloadAt = 2
	_, err := a.UseDedicatedXrayServiceUser(context.Background())
	if err == nil || !strings.Contains(err.Error(), "回滚未完整完成") ||
		!strings.Contains(err.Error(), "恢复后刷新 systemd unit") {
		t.Fatalf("error=%v", err)
	}
	data, readErr := os.ReadFile(filepath.Join(root, "etc/systemd/system/xray.service"))
	if readErr != nil {
		t.Fatal(readErr)
	}
	if !strings.Contains(string(data), "User=nobody") {
		t.Fatalf("unit was not restored on disk: %s", data)
	}
	if !runner.active {
		t.Fatal("rollback should still attempt to restore the running service")
	}
}

func TestUseDedicatedXrayServiceUserRejectsExistingLoginAccount(t *testing.T) {
	a, runner, root := newServiceUserTestApp(t, false)
	runner.accountExists = true
	runner.invalidAccount = true
	_, err := a.UseDedicatedXrayServiceUser(context.Background())
	if err == nil || !strings.Contains(err.Error(), "不是 ProxyForge 专用服务账号") {
		t.Fatalf("error=%v", err)
	}
	data, readErr := os.ReadFile(filepath.Join(root, "etc/systemd/system/xray.service"))
	if readErr != nil {
		t.Fatal(readErr)
	}
	if !strings.Contains(string(data), "User=nobody") {
		t.Fatalf("unit was not restored: %s", data)
	}
}

func TestUseDedicatedXrayServiceUserRollsBackWhenUserPreflightFails(t *testing.T) {
	a, runner, root := newServiceUserTestApp(t, false)
	runner.failRunuser = true
	_, err := a.UseDedicatedXrayServiceUser(context.Background())
	if err == nil || !strings.Contains(err.Error(), "证书、密钥和数据文件权限") ||
		!strings.Contains(err.Error(), "专用账号已安全保留") {
		t.Fatalf("error=%v", err)
	}
	data, readErr := os.ReadFile(filepath.Join(root, "etc/systemd/system/xray.service"))
	if readErr != nil {
		t.Fatal(readErr)
	}
	if !strings.Contains(string(data), "User=nobody") {
		t.Fatalf("unit was not restored: %s", data)
	}
	if !runner.accountExists {
		t.Fatal("newly created service account should be retained")
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
