package cli

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"

	"proxyforge/internal/app"
	"proxyforge/internal/provider"
	"proxyforge/internal/provider/xray"
	"proxyforge/internal/system"
)

type xrayAccountLookupExit int

func (e xrayAccountLookupExit) Error() string { return fmt.Sprintf("exit status %d", e) }
func (e xrayAccountLookupExit) ExitCode() int { return int(e) }

type xrayUserPromptRunner struct {
	exists    bool
	user      string
	lookupErr error
	calls     []string
}

func (r *xrayUserPromptRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	r.calls = append(r.calls, name+" "+strings.Join(args, " "))
	if name == "getent" {
		if r.lookupErr != nil {
			return nil, r.lookupErr
		}
		if !r.exists {
			return nil, xrayAccountLookupExit(2)
		}
		if args[0] == "passwd" {
			return []byte("xray:x:999:999:Xray:/var/empty:/usr/sbin/nologin\n"), nil
		}
		return []byte("xray:x:999:\n"), nil
	}
	if name == "systemctl" {
		switch args[0] {
		case "show":
			if strings.Contains(strings.Join(args, " "), "-p User") {
				return []byte(r.user + "\n"), nil
			}
			return []byte("loaded\n"), nil
		case "is-active":
			return []byte("active\n"), nil
		case "daemon-reload":
			r.user = "xray"
			return nil, nil
		case "restart":
			return nil, nil
		}
	}
	if name == "systemd-sysusers" {
		r.exists = true
		return nil, nil
	}
	if name == "chown" || name == "chmod" || name == "runuser" {
		return nil, nil
	}
	return nil, fmt.Errorf("unexpected command: %s %v", name, args)
}

func newXrayUserPrompt(t *testing.T, exists bool, input string) (*commandSet, *xrayUserPromptRunner, *bytes.Buffer) {
	t.Helper()
	runner := &xrayUserPromptRunner{exists: exists, user: "nobody"}
	layout := system.Layout{Root: t.TempDir()}
	for _, path := range []string{"/etc/systemd/system/xray.service", "/etc/systemd/system/xray@.service"} {
		if err := system.AtomicWrite(layout.Resolve(path), []byte("[Service]\nUser=nobody\nExecStart=/usr/local/bin/xray run\n"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	config := []byte(`{"inbounds":[{"settings":{"clients":[{"id":"keep-uuid"}]}}],"routing":{"rules":[]}}`)
	if err := system.AtomicWrite(layout.Resolve(xray.New().ConfigPath()), config, 0600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	a := app.New(provider.NewRegistry(xray.New()), runner, layout, &out)
	a.RootCheck = func() error { return nil }
	a.LookPath = func(string) (string, error) { return "/usr/local/bin/xray", nil }
	c := &commandSet{app: a, reader: bufio.NewReader(strings.NewReader(input)), out: &out}
	return c, runner, &out
}

func TestDedicatedXrayServiceUserConfirmationBeforeChanges(t *testing.T) {
	for _, tt := range []struct {
		name, input, want string
		exists            bool
		wantErr           error
	}{
		{"reuse", "q\n", "是否复用已有 xray 用户，并覆盖 systemd 运行用户设置及同步文件权限？", true, errReturnToMenu},
		{"create", "q\n", "创建 xray 专用用户并应用服务与权限设置？", false, errReturnToMenu},
		{"no answer", "", "已存在 xray 系统用户", true, io.EOF},
	} {
		t.Run(tt.name, func(t *testing.T) {
			c, runner, out := newXrayUserPrompt(t, tt.exists, tt.input)
			err := c.dedicatedXrayServiceUser(context.Background())
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("error=%v, want %v", err, tt.wantErr)
			}
			for _, want := range []string{tt.want, "User=xray、Group=xray", "属主和权限", "保留节点配置内容，包括 UUID、路由及其他代理用户"} {
				if !strings.Contains(out.String(), want) {
					t.Fatalf("missing %q in output: %s", want, out.String())
				}
			}
			if tt.exists && (!strings.Contains(out.String(), "核验并复用已有 xray 系统用户") || !strings.Contains(out.String(), "保留原 home")) {
				t.Fatalf("missing reuse details: %s", out.String())
			}
			for _, call := range runner.calls {
				if !strings.HasPrefix(call, "systemctl show ") && !strings.HasPrefix(call, "getent ") {
					t.Fatalf("command executed without confirmation: %s", call)
				}
			}
			if _, err := os.Stat(c.app.Layout.Resolve("/etc/systemd/system/xray.service.d")); !os.IsNotExist(err) {
				t.Fatalf("drop-in directory created without confirmation: %v", err)
			}
		})
	}
}

func TestDedicatedXrayServiceUserConfirmedReusePreservesNodeConfig(t *testing.T) {
	c, runner, out := newXrayUserPrompt(t, true, "y\n")
	configPath := c.app.Layout.Resolve(xray.New().ConfigPath())
	before, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.dedicatedXrayServiceUser(context.Background()); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(configPath)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("node config changed: %s, %v", after, err)
	}
	dropIn, err := os.ReadFile(c.app.Layout.Resolve("/etc/systemd/system/xray.service.d/20-proxyforge-user.conf"))
	if err != nil || !strings.Contains(string(dropIn), "User=xray\nGroup=xray") {
		t.Fatalf("drop-in=%s, error=%v", dropIn, err)
	}
	if _, err := os.Stat(c.app.Layout.XrayServiceAccountMarkerPath()); !os.IsNotExist(err) {
		t.Fatalf("reused account marked as owned: %v", err)
	}
	if !strings.Contains(out.String(), "复用了已有 xray 系统用户") {
		t.Fatalf("missing reuse result: %s", out.String())
	}
	calls := strings.Join(runner.calls, "\n")
	if !strings.Contains(calls, "runuser -u xray --") || !strings.Contains(calls, "systemctl restart xray.service") {
		t.Fatalf("missing validation or restart: %s", calls)
	}
}

func TestDedicatedXrayServiceUserAccountLookupFailure(t *testing.T) {
	for _, cause := range []error{errors.New("lookup unavailable"), xrayAccountLookupExit(1), context.Canceled} {
		c, runner, out := newXrayUserPrompt(t, true, "y\n")
		runner.lookupErr = cause
		err := c.dedicatedXrayServiceUser(context.Background())
		if !errors.Is(err, cause) {
			t.Fatalf("error=%v, want %v", err, cause)
		}
		if strings.Contains(out.String(), "确认操作") {
			t.Fatalf("confirmation shown after lookup failure: %s", out.String())
		}
		for _, call := range runner.calls {
			if !strings.HasPrefix(call, "systemctl show ") && !strings.HasPrefix(call, "getent ") {
				t.Fatalf("command executed after lookup failure: %s", call)
			}
		}
	}
}
