// Package selfupdate starts ProxyForge's reviewed installation script.
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
}

type Updater struct {
	Client       *http.Client
	Installer    install.Installer
	ScriptURL    string
	AllowedHosts []string
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
	fmt.Fprintln(output, "[步骤] 下载并验证安装/更新脚本")
	fmt.Fprintf(output, "[信息] 来源：%s\n", scriptURL)
	script, err := installer.PrepareScript(ctx, scriptURL, hosts)
	if err != nil {
		return fmt.Errorf("准备自升级脚本: %w", err)
	}
	fmt.Fprintf(output, "[信息] 最终地址：%s\n", script.FinalURL)
	fmt.Fprintf(output, "[信息] 脚本大小：%d bytes\n", len(script.Content))
	fmt.Fprintf(output, "[信息] 脚本 SHA-256：%s\n", script.SHA256)
	fmt.Fprintln(output, "[步骤] 启动安装/更新流程")

	var args []string
	if opts.AssumeYes {
		args = append(args, "--yes")
	}
	if err := installer.ExecutePreparedScriptDirect(ctx, script, nil, args...); err != nil {
		return fmt.Errorf("执行自升级脚本: %w", err)
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
