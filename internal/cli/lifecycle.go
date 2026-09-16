package cli

import (
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/spf13/cobra"

	"proxyforge/internal/domain"
	"proxyforge/internal/install"
	"proxyforge/internal/selfupdate"
)

var specifiedCoreVersionPattern = regexp.MustCompile(`^v?[0-9][0-9A-Za-z._+-]*$`)

func (c *commandSet) updateCommand() *cobra.Command {
	return &cobra.Command{
		Use: "update", Short: "升级 ProxyForge 自身到最新正式版本", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if c.selfUpdate == nil {
				return fmt.Errorf("自升级功能未初始化")
			}
			return c.selfUpdate(cmd.Context(), selfupdate.Options{
				AssumeYes: c.yes,
			})
		},
	}
}

func (c *commandSet) uninstallCommand() *cobra.Command {
	var trust, scriptURL string
	cmd := &cobra.Command{
		Use: "uninstall [sing-box|xray]", Short: "卸载 ProxyForge 自身，或卸载指定内核并清理数据", Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				if trust != "" || scriptURL != "" {
					return fmt.Errorf("--script-url 和 --trust-script-sha256 仅用于卸载代理内核")
				}
				if c.selfUpdate == nil {
					return fmt.Errorf("自身卸载功能未初始化")
				}
				return c.selfUpdate(cmd.Context(), selfupdate.Options{AssumeYes: c.yes, Uninstall: true})
			}
			interactive := !c.yes && readerInteractive(c.in)
			if !c.yes {
				if !interactive {
					return fmt.Errorf("非交互模式卸载必须显式提供 --yes")
				}
				confirmed, err := c.confirmUninstall(args[0])
				if err != nil {
					return err
				}
				if !confirmed {
					return fmt.Errorf("用户取消卸载")
				}
			}
			return c.app.Uninstall(cmd.Context(), args[0], install.Options{
				URL: scriptURL, NonInteractive: !interactive, TrustScriptSHA256: trust, Confirm: c.confirm,
			})
		},
	}
	cmd.Flags().StringVar(&trust, "trust-script-sha256", "", "非交互卸载 Xray 时固定的官方脚本 SHA-256")
	cmd.Flags().StringVar(&scriptURL, "script-url", "", "Xray 官方管理脚本地址（高级选项，仍受主机白名单限制）")
	return cmd
}

func (c *commandSet) cleanupCommand() *cobra.Command {
	return &cobra.Command{
		Use: "cleanup <sing-box|xray|all>", Short: "直接删除卸载残留", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if !c.yes {
				if !readerInteractive(c.in) {
					return fmt.Errorf("非交互模式清理必须显式提供 --yes")
				}
				confirmed, err := c.confirmCleanup(args[0])
				if err != nil {
					return err
				}
				if !confirmed {
					return fmt.Errorf("用户取消清理")
				}
			}
			return c.app.Cleanup(cmd.Context(), args[0])
		},
	}
}

func (c *commandSet) installCommand() *cobra.Command {
	var version, trust, scriptURL string
	var beta bool
	cmd := &cobra.Command{
		Use: "install <sing-box|xray>", Aliases: []string{"upgrade"}, Short: "安装或升级内核", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := validateInstallVersionFlags(args[0], version, beta); err != nil {
				return err
			}
			nonInteractive := c.yes || !readerInteractive(c.in)
			if !nonInteractive {
				c.clearScreen()
				c.printPageHeader(args[0], "安装/升级内核")
				if version == "" && !beta {
					chosen, err := c.chooseInstallVersion(args[0])
					if err != nil {
						if errors.Is(err, errReturnToMenu) {
							fmt.Fprintln(c.out, "已取消安装/升级。")
							return nil
						}
						return err
					}
					version = chosen.Version
					beta = chosen.Beta
				}
			}
			opts := install.Options{URL: scriptURL, Version: version, Beta: beta, NonInteractive: nonInteractive, TrustScriptSHA256: trust, Confirm: c.confirm}
			return c.app.Install(cmd.Context(), args[0], opts)
		},
	}
	cmd.Flags().StringVar(&version, "version", "", "指定内核版本（默认最新稳定版；与 --beta 互斥）")
	cmd.Flags().BoolVar(&beta, "beta", false, "安装官方最新预发布（仅 xray；与 --version 互斥）")
	cmd.Flags().StringVar(&trust, "trust-script-sha256", "", "非交互模式固定的官方脚本 SHA-256")
	cmd.Flags().StringVar(&scriptURL, "script-url", "", "官方安装脚本地址（高级选项，仍受主机白名单限制）")
	return cmd
}

func validateInstallVersionFlags(core, version string, beta bool) error {
	if beta && strings.TrimSpace(version) != "" {
		return fmt.Errorf("--beta 与 --version 不能同时使用")
	}
	if beta && core != domain.CoreXray {
		return fmt.Errorf("%s 不支持 --beta（仅 xray 可安装官方预发布）", core)
	}
	return nil
}

func (c *commandSet) chooseInstallVersion(core string) (install.Options, error) {
	fmt.Fprintln(c.out, "安装版本")
	c.printMenuChoice("1", "最新稳定版（默认；官方当前正式版）")
	maxChoice := 2
	specifiedChoice := 2
	if core == domain.CoreXray {
		c.printMenuChoice("2", "最新预发布（官方安装脚本 --beta）")
		c.printMenuChoice("3", "指定版本号（输入官方 GitHub 版本号）")
		maxChoice = 3
		specifiedChoice = 3
	} else {
		c.printMenuChoice("2", "指定版本号（输入官方 GitHub 版本号）")
	}
	choice, err := c.chooseNumberCancelable("请选择安装版本", 1, maxChoice, 1)
	if err != nil {
		return install.Options{}, err
	}
	if choice == 1 {
		return install.Options{}, nil
	}
	if core == domain.CoreXray && choice == 2 {
		return install.Options{Beta: true}, nil
	}
	if choice != specifiedChoice {
		return install.Options{}, fmt.Errorf("无效的安装版本选择")
	}
	for {
		fmt.Fprintln(c.out)
		value, err := c.askDefaultCancelable("版本号", "")
		if err != nil {
			return install.Options{}, err
		}
		version, err := parseSpecifiedCoreVersion(value)
		if err != nil {
			fmt.Fprintf(c.out, "%v。\n", err)
			continue
		}
		return install.Options{Version: version}, nil
	}
}

func parseSpecifiedCoreVersion(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", fmt.Errorf("版本号不能为空")
	}
	if !specifiedCoreVersionPattern.MatchString(value) {
		return "", fmt.Errorf("版本号无效，请输入类似 v26.9.9 的官方版本")
	}
	return value, nil
}

func installVersionLabel(opts install.Options) string {
	if opts.Beta {
		return "最新预发布"
	}
	if version := strings.TrimSpace(opts.Version); version != "" {
		return "指定版本 " + version
	}
	return "最新稳定版"
}

func (c *commandSet) confirmUninstall(core string) (bool, error) {
	permanentDeletes := []string{
		"服务端配置、运行数据和文件日志",
		"ProxyForge 状态、信任记录和历史备份",
	}
	if core == "xray" {
		permanentDeletes = append(permanentDeletes, "ProxyForge 创建且身份未变化的 xray 专用系统用户和组")
	}
	c.printConfirmationPanel(
		"危险操作确认：卸载内核并清理数据",
		[]string{"目标内核：" + core},
		confirmationSection{title: "将执行", items: []string{
			"停止并禁用 systemd 服务",
			"卸载内核并核验卸载结果",
			"卸载成功后自动清理全部残留",
		}},
		confirmationSection{title: "永久删除", items: permanentDeletes},
		confirmationSection{title: "重要影响", items: []string{
			"现有客户端将立即失效",
			"卸载或核验失败时不会执行自动清理",
		}},
	)
	return c.confirm("卸载并清理 " + core + "？")
}

func (c *commandSet) confirmInstall(core string, opts install.Options) (bool, error) {
	c.printConfirmationPanel(
		"操作确认：安装/升级内核",
		[]string{"目标内核：" + core, "安装版本：" + installVersionLabel(opts)},
		confirmationSection{title: "将执行", items: []string{
			"下载并执行官方管理脚本",
			"安装或升级 " + core + " 内核二进制",
			"可能更新软件包文件和 systemd unit",
		}},
		confirmationSection{title: "配置保护", items: []string{
			"检测到现有配置时，现有配置会先备份",
		}},
		confirmationSection{title: "安全确认", items: []string{
			"执行前还会展示脚本来源、大小和 SHA-256",
			"首次或脚本变更时需要再次确认信任",
		}},
	)
	return c.confirm("安装或升级 " + core + "？")
}

func (c *commandSet) confirmCleanup(target string) (bool, error) {
	c.printConfirmationPanel(
		"危险操作确认：清理卸载残留",
		[]string{"清理目标：" + target},
		confirmationSection{title: "永久删除", items: []string{
			"配置目录、运行数据和文件日志",
			"ProxyForge 状态、信任记录和历史备份",
		}},
		confirmationSection{title: "数据保护", items: []string{
			"此操作不会创建新备份",
		}},
	)
	return c.confirm("永久清理 " + target + " 的卸载残留？")
}
