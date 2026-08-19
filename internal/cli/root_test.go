package cli

import (
	"bytes"
	"errors"
	"testing"
)

func TestStableCommandTree(t *testing.T) {
	root := New("test")
	for _, args := range [][]string{{"install"}, {"update"}, {"uninstall"}, {"cleanup"}, {"config", "generate"}, {"config", "client"}, {"config", "reset"}, {"service"}} {
		cmd, remaining, err := root.Find(args)
		if err != nil {
			t.Fatalf("find %v: %v", args, err)
		}
		if len(remaining) != 0 {
			t.Fatalf("find %v left %v", args, remaining)
		}
		if cmd.Name() != args[len(args)-1] {
			t.Fatalf("find %v got %s", args, cmd.Name())
		}
	}
	cmd, _, err := root.Find([]string{"upgrade"})
	if err != nil || cmd.Name() != "install" {
		t.Fatalf("upgrade alias resolved to %v, error=%v", cmd, err)
	}
}

func TestVersionOutputIsDetailedAndSkipsRootCheck(t *testing.T) {
	rootChecked := false
	root := newCommand(
		"v1.2.3\ncommit: 0123456789abcdef0123456789abcdef01234567\nbuild date: 2026-08-08T12:00:00Z\ngo: go1.25.0\nplatform: linux/amd64",
		func() error {
			rootChecked = true
			return errors.New("root check should not run")
		},
	)
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetArgs([]string{"-v"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	if rootChecked {
		t.Fatal("root check ran for -v")
	}
	want := "proxyforge v1.2.3\ncommit: 0123456789abcdef0123456789abcdef01234567\nbuild date: 2026-08-08T12:00:00Z\ngo: go1.25.0\nplatform: linux/amd64\n"
	if got := out.String(); got != want {
		t.Fatalf("version output = %q, want %q", got, want)
	}
}

func TestCommandsRequireRootBeforeRunning(t *testing.T) {
	want := errors.New("root required")
	checks := 0
	root := newCommand("test", func() error {
		checks++
		return want
	})
	root.SetArgs([]string{"config", "client", "sing-box"})
	err := root.Execute()
	if !errors.Is(err, want) {
		t.Fatalf("error = %v, want %v", err, want)
	}
	if checks != 1 {
		t.Fatalf("root checks = %d, want 1", checks)
	}
}

func TestMenuRequiresRootBeforeRunning(t *testing.T) {
	want := errors.New("root required")
	root := newCommand("test", func() error { return want })
	root.SetArgs(nil)
	if err := root.Execute(); !errors.Is(err, want) {
		t.Fatalf("error = %v, want %v", err, want)
	}
}
