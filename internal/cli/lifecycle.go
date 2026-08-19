package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"proxyforge/internal/install"
	"proxyforge/internal/selfupdate"
)

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
	cmd := &cobra.Command{
		Use: "install <sing-box|xray>", Aliases: []string{"upgrade"}, Short: "安装或升级内核", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			nonInteractive := c.yes || !readerInteractive(c.in)
			if !nonInteractive {
				c.clearScreen()
				c.printPageHeader(args[0], "安装/升级内核")
			}
			opts := install.Options{URL: scriptURL, Version: version, NonInteractive: nonInteractive, TrustScriptSHA256: trust, Confirm: c.confirm}
			return c.app.Install(cmd.Context(), args[0], opts)
		},
	}
	cmd.Flags().StringVar(&version, "version", "", "指定内核版本（默认最新稳定版）")
	cmd.Flags().StringVar(&trust, "trust-script-sha256", "", "非交互模式固定的官方脚本 SHA-256")
	cmd.Flags().StringVar(&scriptURL, "script-url", "", "官方安装脚本地址（高级选项，仍受主机白名单限制）")
	return cmd
}

func (c *commandSet) confirmUninstall(core string) (bool, error) {
	c.printConfirmationPanel(
		"危险操作确认：卸载内核并清理数据",
		[]string{"目标内核：" + core},
		confirmationSection{title: "将执行", items: []string{
			"停止并禁用 systemd 服务",
			"卸载内核并核验卸载结果",
			"卸载成功后自动清理全部残留",
		}},
		confirmationSection{title: "永久删除", items: []string{
			"服务端配置、运行数据和文件日志",
			"ProxyForge 状态、信任记录和历史备份",
		}},
		confirmationSection{title: "重要影响", items: []string{
			"现有客户端将立即失效",
			"卸载或核验失败时不会执行自动清理",
		}},
	)
	return c.confirm("卸载并清理 " + core + "？")
}

func (c *commandSet) confirmInstall(core string) (bool, error) {
	c.printConfirmationPanel(
		"操作确认：安装/升级内核",
		[]string{"目标内核：" + core},
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
