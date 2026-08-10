// Package selfupdate upgrades the ProxyForge binary through its reviewed
// installation script.
package selfupdate

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"proxyforge/internal/install"
)

const (
	defaultVersionURL = "https://github.com/JokerKernel/proxyforge/releases/latest/download/version"
	defaultScriptURL  = "https://raw.githubusercontent.com/JokerKernel/proxyforge/main/scripts/install.sh"
	maxVersionSize    = 256
)

var releaseVersionPattern = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+([+-][0-9A-Za-z.-]+)?$`)

var defaultHosts = []string{
	"github.com",
	"raw.githubusercontent.com",
	"release-assets.githubusercontent.com",
	"objects.githubusercontent.com",
}

type ConfirmFunc func(string) (bool, error)

type Options struct {
	CurrentVersion string
	AssumeYes      bool
	Confirm        ConfirmFunc
}

type Updater struct {
	Client       *http.Client
	Installer    install.Installer
	Output       io.Writer
	VersionURL   string
	ScriptURL    string
	AllowedHosts []string
}

func (u Updater) Run(ctx context.Context, opts Options) error {
	versionURL, scriptURL, hosts := u.settings()
	latest, err := u.latestVersion(ctx, versionURL, hosts)
	if err != nil {
		return fmt.Errorf("检查最新版本: %w", err)
	}
	out := u.Output
	if out == nil {
		out = io.Discard
	}
	current := strings.TrimSpace(opts.CurrentVersion)
	if current == latest {
		fmt.Fprintf(out, "[ProxyForge/结果] 当前版本 %s 已是最新正式版本。\n", current)
		return nil
	}

	installer := u.Installer
	if u.Client != nil {
		installer.Client = u.Client
	}
	script, err := installer.PrepareScript(ctx, scriptURL, hosts)
	if err != nil {
		return fmt.Errorf("准备自升级脚本: %w", err)
	}
	fmt.Fprintf(out, "[ProxyForge/更新] 当前版本：%s\n", displayVersion(current))
	fmt.Fprintf(out, "[ProxyForge/更新] 目标版本：%s\n", latest)
	fmt.Fprintf(out, "[ProxyForge/更新] 脚本来源：%s\n", script.SourceURL)
	fmt.Fprintf(out, "[ProxyForge/更新] 最终地址：%s\n", script.FinalURL)
	fmt.Fprintf(out, "[ProxyForge/更新] 脚本大小：%d bytes\n", len(script.Content))
	fmt.Fprintf(out, "[ProxyForge/更新] 脚本 SHA-256：%s\n", script.SHA256)
	fmt.Fprintln(out, "[ProxyForge/风险] 将以 root 执行脚本并替换 /usr/local/sbin/xraybox。")

	if !opts.AssumeYes {
		if opts.Confirm == nil {
			return fmt.Errorf("执行自升级前需要交互确认，自动化时请显式提供 --yes")
		}
		ok, err := opts.Confirm(fmt.Sprintf("确认将 ProxyForge 从 %s 升级到 %s？", displayVersion(current), latest))
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("用户取消自升级")
		}
	}

	if err := installer.ExecutePreparedScript(ctx, script, []string{"PROXYFORGE_VERSION=" + latest}); err != nil {
		return fmt.Errorf("执行自升级脚本: %w", err)
	}
	fmt.Fprintf(out, "[ProxyForge/结果] ProxyForge 已升级到 %s。\n", latest)
	return nil
}

func (u Updater) settings() (string, string, []string) {
	versionURL := u.VersionURL
	if versionURL == "" {
		versionURL = defaultVersionURL
	}
	scriptURL := u.ScriptURL
	if scriptURL == "" {
		scriptURL = defaultScriptURL
	}
	hosts := u.AllowedHosts
	if len(hosts) == 0 {
		hosts = defaultHosts
	}
	return versionURL, scriptURL, hosts
}

func (u Updater) latestVersion(ctx context.Context, rawURL string, hosts []string) (string, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return "", err
	}
	if err := checkURL(parsed, hosts); err != nil {
		return "", err
	}
	client := u.Client
	if client == nil {
		client = u.Installer.Client
	}
	if client == nil {
		client = &http.Client{Timeout: 45 * time.Second}
	}
	secureClient := *client
	secureClient.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= 5 {
			return fmt.Errorf("重定向次数过多")
		}
		return checkURL(req.URL, hosts)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "text/plain")
	resp, err := secureClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("version HTTP 状态 %s", resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxVersionSize+1))
	if err != nil {
		return "", err
	}
	if len(body) > maxVersionSize {
		return "", fmt.Errorf("version 文件超过 %d bytes", maxVersionSize)
	}
	version := strings.TrimSpace(string(body))
	if strings.ContainsAny(version, "\r\n") || !releaseVersionPattern.MatchString(version) {
		return "", fmt.Errorf("version 文件内容无效：%q", version)
	}
	return version, nil
}

func checkURL(u *url.URL, hosts []string) error {
	if u.Scheme != "https" {
		return fmt.Errorf("自升级只允许 HTTPS")
	}
	host := strings.ToLower(u.Hostname())
	for _, allowed := range hosts {
		if host == strings.ToLower(allowed) {
			return nil
		}
	}
	return fmt.Errorf("自升级地址主机 %q 不在白名单", host)
}

func displayVersion(version string) string {
	if version == "" {
		return "unknown"
	}
	return version
}
