package cli

import (
	"context"
	"io"
	"strings"

	"proxyforge/internal/app"
	"proxyforge/internal/system"
)

type liveLogRunner struct {
	name string
	args []string
}

type installedUnitRunner struct{}

func (installedUnitRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	if name == "systemctl" && len(args) > 0 && args[0] == "is-active" {
		return []byte("active\n"), nil
	}
	if name == "systemctl" && len(args) > 0 && args[0] == "show" {
		if strings.Contains(strings.Join(args, " "), "-p User") {
			return []byte("xray\n"), nil
		}
		return []byte("loaded\n"), nil
	}
	return nil, nil
}

type installedVersionRunner struct{}

func (installedVersionRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	if name == "xray" && len(args) > 0 && args[0] == "version" {
		return []byte("Xray 25.1.1 (Xray, Penetrates Everything.)\n"), nil
	}
	if name == "sing-box" && len(args) > 0 && args[0] == "version" {
		return []byte("sing-box version 1.14.0\n"), nil
	}
	return nil, nil
}

func markCoreInstalled(a *app.App) {
	a.LookPath = func(name string) (string, error) { return "/usr/bin/" + name, nil }
	a.Services = system.ServiceManager{Runner: installedUnitRunner{}}
}

func (r *liveLogRunner) Run(context.Context, string, ...string) ([]byte, error) {
	return nil, nil
}

func (r *liveLogRunner) RunStreaming(ctx context.Context, stdout, _ io.Writer, name string, args ...string) error {
	r.name = name
	r.args = append([]string(nil), args...)
	_, _ = io.WriteString(stdout, "live entry\n")
	<-ctx.Done()
	return ctx.Err()
}
