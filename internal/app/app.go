package app

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"time"

	"proxyforge/internal/install"
	"proxyforge/internal/provider"
	"proxyforge/internal/system"
)

type App struct {
	Registry  *provider.Registry
	Runner    provider.Runner
	Layout    system.Layout
	Store     system.StateStore
	Services  system.ServiceManager
	Installer install.Installer
	Targets   TargetValidator
	Out       io.Writer
	Progress  io.Writer
	Now       func() time.Time
	RootCheck func() error
	LookPath  func(string) (string, error)
	PortFree  func(int) error
	Listening func(context.Context, int, time.Duration) error
}

func New(reg *provider.Registry, runner provider.Runner, layout system.Layout, out io.Writer) *App {
	a := &App{
		Registry: reg, Runner: runner, Layout: layout, Store: system.StateStore{Layout: layout},
		Services: system.ServiceManager{Runner: runner}, Installer: install.Installer{Runner: runner, Layout: layout, Output: out},
		Out: out, Progress: out, Now: time.Now,
		RootCheck: RequireRoot, LookPath: exec.LookPath, PortFree: checkPortFree, Listening: waitListening,
	}
	a.Targets = NetworkTargetValidator{Progress: func(message string) { a.progressf("%s", message) }}
	return a
}

func (a *App) progressf(format string, args ...any) {
	if a.Progress == nil {
		return
	}
	fmt.Fprintf(a.Progress, "[步骤] "+format+"\n", args...)
}

func RequireRoot() error {
	if os.Geteuid() != 0 {
		return fmt.Errorf("此操作必须以 root 运行")
	}
	return nil
}

func (a *App) lookPath(name string) (string, error) {
	if a.LookPath != nil {
		return a.LookPath(name)
	}
	return exec.LookPath(name)
}
