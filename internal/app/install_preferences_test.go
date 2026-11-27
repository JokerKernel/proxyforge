package app

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"

	"proxyforge/internal/domain"
	"proxyforge/internal/install"
	"proxyforge/internal/system"
)

type preferenceInstallRunner struct {
	base *fakeRunner
	fail string
}

func (r preferenceInstallRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	if r.fail == "script" && name == "bash" {
		return nil, errors.New("installation failed")
	}
	if r.fail == "validation" && (name == "sing-box" || name == "xray") && len(args) > 0 && (args[0] == "check" || args[0] == "run") {
		return nil, errors.New("validation failed")
	}
	if r.fail == "service" && name == "systemctl" && len(args) > 0 && args[0] == "is-active" {
		return []byte("failed\n"), errors.New("service failed")
	}
	return r.base.Run(ctx, name, args...)
}

func TestInstallPersistsChannelOnlyAfterAllChecksPass(t *testing.T) {
	for _, core := range []string{domain.CoreSingBox, domain.CoreXray} {
		for _, tt := range []struct {
			name, initial, fail, want string
			beta, pinned, inactive    bool
		}{
			{name: "development", initial: system.InstallChannelStable, beta: true, want: system.InstallChannelBeta},
			{name: "switch back to stable", initial: system.InstallChannelBeta, want: system.InstallChannelStable},
			{name: "first install inactive", beta: true, inactive: true, want: system.InstallChannelBeta},
			{name: "specified keeps channel", initial: system.InstallChannelBeta, pinned: true, want: system.InstallChannelBeta},
			{name: "specified creates no preference", pinned: true, want: system.InstallChannelStable},
			{name: "script failure keeps channel", initial: system.InstallChannelStable, beta: true, fail: "script", want: system.InstallChannelStable},
			{name: "validation failure keeps channel", initial: system.InstallChannelStable, beta: true, fail: "validation", want: system.InstallChannelStable},
			{name: "service failure keeps channel", initial: system.InstallChannelStable, beta: true, fail: "service", want: system.InstallChannelStable},
		} {
			t.Run(core+"/"+tt.name, func(t *testing.T) {
				base := &fakeRunner{serviceStopped: tt.inactive}
				a, root := testApp(t, base)
				writeSupportedPlatform(t, root)
				runner := preferenceInstallRunner{base: base, fail: tt.fail}
				a.Runner, a.Installer.Runner, a.Services.Runner = runner, runner, runner
				script := "#!/bin/bash\nexit 0\n"
				a.Installer.Client = &http.Client{Transport: installStatusTransport(func(req *http.Request) (*http.Response, error) {
					return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(script)), Request: req}, nil
				})}
				store := system.InstallPreferencesStore{Layout: a.Layout}
				if tt.initial != "" {
					if err := store.SaveChannel(core, tt.initial); err != nil {
						t.Fatal(err)
					}
				}
				opts := install.Options{Beta: tt.beta, NonInteractive: true, TrustScriptSHA256: system.SHA256([]byte(script))}
				if tt.pinned {
					opts.Version = "1.14.0"
					if core == domain.CoreXray {
						opts.Version = "25.1.1"
					}
				}
				err := a.Install(context.Background(), core, opts)
				if (err != nil) != (tt.fail != "") {
					t.Fatalf("install error=%v, fail=%q", err, tt.fail)
				}
				// Reopen using a new app instance to verify persistence.
				got, readErr := (&App{Layout: a.Layout}).PreferredInstallChannel(core)
				if readErr != nil || got != tt.want {
					t.Fatalf("saved channel=%q err=%v, want %q", got, readErr, tt.want)
				}
				if tt.pinned && tt.initial == "" {
					if _, err := os.Stat(a.Layout.InstallPreferencesPath(core)); !os.IsNotExist(err) {
						t.Fatalf("pinned version created a preference: %v", err)
					}
				}
				other := domain.CoreSingBox
				if core == other {
					other = domain.CoreXray
				}
				if _, err := os.Stat(a.Layout.InstallPreferencesPath(other)); !os.IsNotExist(err) {
					t.Fatalf("installation modified the other core's preference: %v", err)
				}
			})
		}
	}
}
