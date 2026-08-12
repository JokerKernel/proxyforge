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
	fmt.Fprintf(output, "[步骤] 下载并验证 ProxyForge %s脚本\n", operation)
	fmt.Fprintf(output, "[信息] 安装脚本地址：%s\n", scriptURL)
	script, err := installer.PrepareScript(ctx, scriptURL, hosts)
	if err != nil {
		return fmt.Errorf("准备 ProxyForge %s脚本: %w", operation, err)
	}
	fmt.Fprintf(output, "[信息] 重定向后地址：%s\n", script.FinalURL)
	fmt.Fprintf(output, "[信息] 脚本大小：%d bytes\n", len(script.Content))
	fmt.Fprintf(output, "[信息] 脚本 SHA-256：%s\n", script.SHA256)
	fmt.Fprintf(output, "[步骤] 启动 ProxyForge %s流程\n", operation)

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
