// Package selfupdate starts ProxyForge's reviewed management script.
// Version discovery, comparison, artifact verification, and replacement are
// intentionally owned by that script.
package selfupdate

import (
	"context"
	"fmt"
	"io"
	"net/http"

	"proxyforge/internal/install"
	"proxyforge/internal/system"
)

const defaultScriptURL = "https://raw.githubusercontent.com/JokerKernel/proxyforge/main/scripts/install.sh"

var defaultHosts = []string{
	"github.com",
	"raw.githubusercontent.com",
	"release-assets.githubusercontent.com",
	"objects.githubusercontent.com",
}

type Options struct {
	AssumeYes bool
	Uninstall bool
}

type Updater struct {
	Client       *http.Client
	Installer    install.Installer
	ScriptURL    string
	AllowedHosts []string
	Stdout       io.Writer
	Stderr       io.Writer
}

func (u Updater) Run(ctx context.Context, opts Options) error {
	scriptURL, hosts := u.settings()
	installer := u.Installer
	if u.Client != nil {
		installer.Client = u.Client
	}
	output := installer.Output
	if output == nil {
		output = io.Discard
	}
	operation := "安装/更新"
	if opts.Uninstall {
		operation = "卸载"
	}
	system.WriteDetail(output, "准备程序管理脚本", "下载并验证 ProxyForge %s脚本", operation)
	system.WriteDetail(output, "脚本来源", "%s", scriptURL)
	script, err := installer.PrepareScript(ctx, scriptURL, hosts)
	if err != nil {
		return fmt.Errorf("准备 ProxyForge %s脚本: %w", operation, err)
	}
	system.WriteDetail(output, "重定向后地址", "%s", script.FinalURL)
	system.WriteDetail(output, "脚本大小", "%d bytes", len(script.Content))
	system.WriteDetail(output, "脚本 SHA-256", "%s", script.SHA256)
	system.WriteDetail(output, "启动程序管理", "ProxyForge %s流程", operation)

	var args []string
	if opts.Uninstall {
		args = append(args, "uninstall")
	}
	if opts.AssumeYes {
		args = append(args, "--yes")
	}
	stdout := u.Stdout
	if stdout == nil {
		stdout = output
	}
	stderr := u.Stderr
	if stderr == nil {
		stderr = output
	}
	if err := installer.ExecutePreparedScriptAttached(ctx, script, stdout, stderr, args...); err != nil {
		return fmt.Errorf("执行 ProxyForge %s脚本: %w", operation, err)
	}
	return nil
}

func (u Updater) settings() (string, []string) {
	scriptURL := u.ScriptURL
	if scriptURL == "" {
		scriptURL = defaultScriptURL
	}
	hosts := u.AllowedHosts
	if len(hosts) == 0 {
		hosts = defaultHosts
	}
	return scriptURL, hosts
}
