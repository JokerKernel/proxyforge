package app

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

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

func TestCoreInstallStatusIdentifiesLiveChannels(t *testing.T) {
	for _, tt := range []struct {
		name, core, version, response, channel string
		status                                 int
		fetch                                  bool
	}{
		{name: "sing-box stable", core: domain.CoreSingBox, version: "sing-box version 1.14.0", channel: InstallChannelStable},
		{name: "sing-box beta", core: domain.CoreSingBox, version: "sing-box version 1.14.0-beta.2", channel: InstallChannelDevelopment},
		{name: "sing-box alpha", core: domain.CoreSingBox, version: "sing-box version 1.14.0-alpha.2", channel: InstallChannelDevelopment},
		{name: "sing-box rc", core: domain.CoreSingBox, version: "sing-box version 1.14.0-rc.1+build", channel: InstallChannelDevelopment},
		{name: "sing-box build metadata", core: domain.CoreSingBox, version: "sing-box version 1.14.0+build", channel: InstallChannelStable},
		{name: "sing-box custom build", core: domain.CoreSingBox, version: "sing-box version 1.14.0-custom"},
		{name: "sing-box unreadable version", core: domain.CoreSingBox, version: "sing-box version unknown"},
		{name: "xray numeric stable", core: domain.CoreXray, version: "Xray 26.9.9 (Xray, Penetrates Everything.)", fetch: true, response: `{"tag_name":"v26.9.9","prerelease":false}`, channel: InstallChannelStable},
		{name: "xray numeric development", core: domain.CoreXray, version: "Xray 26.9.9 (Xray, Penetrates Everything.)", fetch: true, response: `{"tag_name":"v26.9.9","prerelease":true}`, channel: InstallChannelDevelopment},
		{name: "xray API unavailable", core: domain.CoreXray, version: "Xray 26.9.9", fetch: true, status: http.StatusForbidden},
		{name: "xray network failure", core: domain.CoreXray, version: "Xray 26.9.9", fetch: true, status: -1},
		{name: "xray malformed metadata", core: domain.CoreXray, version: "Xray 26.9.9", fetch: true, response: "{"},
		{name: "xray missing prerelease marker", core: domain.CoreXray, version: "Xray 26.9.9", fetch: true, response: `{"tag_name":"v26.9.9"}`},
		{name: "xray wrong release", core: domain.CoreXray, version: "Xray 26.9.9", fetch: true, response: `{"tag_name":"v26.9.8","prerelease":false}`},
		{name: "xray draft", core: domain.CoreXray, version: "Xray 26.9.9", fetch: true, response: `{"tag_name":"v26.9.9","prerelease":false,"draft":true}`},
		{name: "xray oversized metadata", core: domain.CoreXray, version: "Xray 26.9.9", fetch: true, response: strings.Repeat(" ", (1<<20)+1)},
		{name: "xray custom build", core: domain.CoreXray, version: "Xray custom"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			a := &App{Registry: provider.NewRegistry(singbox.New(), xray.New()), Runner: installStatusRunner(tt.version)}
			requests := 0
			a.Installer.Client = &http.Client{Transport: installStatusTransport(func(req *http.Request) (*http.Response, error) {
				requests++
				if req.URL.String() != "https://api.github.com/repos/XTLS/Xray-core/releases/tags/v26.9.9" {
					t.Fatalf("unexpected URL: %s", req.URL)
				}
				if deadline, ok := req.Context().Deadline(); !ok || time.Until(deadline) > 3*time.Second {
					t.Fatal("release lookup must have a bounded timeout")
				}
				if tt.status == -1 {
					return nil, errors.New("network unavailable")
				}
				status := tt.status
				if status == 0 {
					status = http.StatusOK
				}
				return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(tt.response))}, nil
			})}
			got := a.CoreInstallStatus(context.Background(), tt.core)
			if !got.Installed || got.Version != tt.version || got.Channel != tt.channel {
				t.Fatalf("status=%+v, want version=%q channel=%q", got, tt.version, tt.channel)
			}
			if (requests != 0) != tt.fetch {
				t.Fatalf("requests=%d, fetch=%v", requests, tt.fetch)
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
			if got.Installed != installed || got.Version != "" || got.Channel != "" {
				t.Fatalf("core=%s installed=%v status=%+v", core, installed, got)
			}
		}
	}
}
