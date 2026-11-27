package app

import (
	"context"
	"errors"
	"net/http"
	"os"
	"testing"

	"proxyforge/internal/domain"
	"proxyforge/internal/provider"
	"proxyforge/internal/provider/singbox"
	"proxyforge/internal/provider/xray"
)

type installStatusRunner string

func (r installStatusRunner) Run(context.Context, string, ...string) ([]byte, error) {
	if r == "" {
		return nil, errors.New("version unavailable")
	}
	return []byte(r), nil
}

type installStatusTransport func(*http.Request) (*http.Response, error)

func (f installStatusTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestCoreInstallStatusReadsLocalVersionWithoutNetwork(t *testing.T) {
	for _, tt := range []struct{ core, version string }{
		{domain.CoreSingBox, "sing-box version 1.14.0"},
		{domain.CoreSingBox, "sing-box version 1.14.0-beta.2"},
		{domain.CoreXray, "Xray 26.3.27 (Xray, Penetrates Everything.)"},
		{domain.CoreXray, "Xray custom"},
	} {
		t.Run(tt.core+"/"+tt.version, func(t *testing.T) {
			a := &App{Registry: provider.NewRegistry(singbox.New(), xray.New()), Runner: installStatusRunner(tt.version)}
			a.Installer.Client = &http.Client{Transport: installStatusTransport(func(*http.Request) (*http.Response, error) {
				t.Fatal("opening the installation page must not make a network request")
				return nil, errors.New("unexpected network request")
			})}
			got := a.CoreInstallStatus(context.Background(), tt.core)
			if !got.Installed || got.Version != tt.version {
				t.Fatalf("status=%+v, want version=%q", got, tt.version)
			}
		})
	}
}

func TestCoreInstallStatusDistinguishesMissingAndUnreadableBinary(t *testing.T) {
	for _, core := range []string{domain.CoreSingBox, domain.CoreXray} {
		for _, installed := range []bool{false, true} {
			a := &App{
				Registry: provider.NewRegistry(singbox.New(), xray.New()), Runner: installStatusRunner(""),
				LookPath: func(string) (string, error) {
					if installed {
						return "/bin/core", nil
					}
					return "", os.ErrNotExist
				},
			}
			got := a.CoreInstallStatus(context.Background(), core)
			if got.Installed != installed || got.Version != "" {
				t.Fatalf("core=%s installed=%v status=%+v", core, installed, got)
			}
		}
	}
}
