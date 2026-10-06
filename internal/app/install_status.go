package app

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"proxyforge/internal/domain"
)

const (
	InstallChannelStable      = "stable"
	InstallChannelDevelopment = "development"
)

type CoreInstallStatus struct {
	Installed bool
	Version   string
	Channel   string
}

var installedCoreVersionPattern = regexp.MustCompile(`^v?[0-9]+\.[0-9]+\.[0-9]+(?:-[0-9A-Za-z.-]+)?(?:\+[0-9A-Za-z.-]+)?$`)

// CoreInstallStatus reads the live binary rather than the version saved with a
// node configuration, which may be stale after an external upgrade.
func (a *App) CoreInstallStatus(ctx context.Context, core string) CoreInstallStatus {
	status := CoreInstallStatus{Version: a.CoreVersion(ctx, core)}
	if status.Version == "" {
		if a != nil && a.Registry != nil {
			if p, err := a.Registry.Get(core); err == nil {
				_, err = a.lookPath(p.Binary())
				status.Installed = err == nil
			}
		}
		return status
	}
	status.Installed = true
	fields := strings.Fields(status.Version)
	var version string
	switch core {
	case domain.CoreSingBox:
		if len(fields) >= 3 && fields[0] == "sing-box" && fields[1] == "version" {
			version = fields[2]
		}
	case domain.CoreXray:
		if len(fields) >= 2 && strings.EqualFold(fields[0], "xray") {
			version = fields[1]
		}
	}
	if !installedCoreVersionPattern.MatchString(version) {
		return status
	}
	if core == domain.CoreXray {
		// Xray prereleases can have purely numeric version numbers. Only the
		// official release metadata can distinguish them from stable releases.
		status.Channel = a.xrayReleaseChannel(ctx, "v"+strings.TrimPrefix(version, "v"))
		return status
	}
	version, _, _ = strings.Cut(version, "+")
	_, suffix, prerelease := strings.Cut(version, "-")
	if !prerelease {
		status.Channel = InstallChannelStable
	} else {
		for _, marker := range []string{"alpha", "beta", "rc", "dev"} {
			if suffix == marker || strings.HasPrefix(suffix, marker+".") {
				status.Channel = InstallChannelDevelopment
				break
			}
		}
	}
	return status
}

func (a *App) xrayReleaseChannel(ctx context.Context, tag string) string {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		"https://api.github.com/repos/XTLS/Xray-core/releases/tags/"+tag, nil)
	if err != nil {
		return ""
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	client := a.Installer.Client
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return ""
	}
	const maxReleaseSize = 1 << 20
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxReleaseSize+1))
	if err != nil || len(body) > maxReleaseSize {
		return ""
	}
	var release struct {
		Tag        string `json:"tag_name"`
		Prerelease *bool  `json:"prerelease"`
		Draft      bool   `json:"draft"`
	}
	if json.Unmarshal(body, &release) != nil || release.Tag != tag || release.Draft || release.Prerelease == nil {
		return ""
	}
	if *release.Prerelease {
		return InstallChannelDevelopment
	}
	return InstallChannelStable
}
