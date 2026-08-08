package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"proxyforge/internal/app"
	"proxyforge/internal/domain"
	"proxyforge/internal/install"
	"proxyforge/internal/provider"
	"proxyforge/internal/provider/singbox"
	"proxyforge/internal/provider/xray"
	"proxyforge/internal/system"
)

var errReturnToMenu = errors.New("返回主菜单")

type commandSet struct {
	app         *app.App
	in          io.Reader
	reader      *bufio.Reader
	out         io.Writer
	errOut      io.Writer
	yes         bool
	probeSNI    sniCandidateProbeFunc
	randomIndex func(int) int
	physicalIPs func() ([]app.PublicInterfaceAddress, error)
	externalIP  func(context.Context) (string, error)
}

func New(version string) *cobra.Command {
	return newCommand(version, app.RequireRoot)
}

func newCommand(version string, rootCheck func() error) *cobra.Command {
	runner := &system.LoggingRunner{Runner: system.ExecRunner{}, Out: os.Stderr}
	layout := system.Layout{Root: os.Getenv("PROXYFORGE_ROOT")}
	reg := provider.NewRegistry(singbox.New(), xray.New())
	a := app.New(reg, runner, layout, os.Stdout)
	a.RootCheck = rootCheck
	a.Progress = os.Stderr
	a.Installer.Output = os.Stderr
	c := &commandSet{
		app: a, in: os.Stdin, reader: bufio.NewReader(os.Stdin), out: os.Stdout, errOut: os.Stderr,
		probeSNI: app.ProbeSNICandidates, randomIndex: secureRandomIndex,
		physicalIPs: app.PhysicalPublicAddresses, externalIP: app.PublicAddress,
	}
	root := &cobra.Command{
		Use: "proxyforge", Short: "Linux 双内核 VLESS + REALITY + Vision 管理器", Version: version,
		SilenceUsage: true, SilenceErrors: true,
		PersistentPreRunE: func(*cobra.Command, []string) error {
			fmt.Fprintln(os.Stderr, "[步骤] 验证 root 运行权限")
			return rootCheck()
		},
		RunE: func(cmd *cobra.Command, args []string) error { return c.menu(cmd.Context()) },
	}
	root.SetOut(os.Stdout)
	root.SetErr(os.Stderr)
	root.SetIn(os.Stdin)
	root.PersistentFlags().BoolVarP(&c.yes, "yes", "y", false, "非交互模式（执行下载的管理脚本仍必须提供 SHA-256）")
	root.AddCommand(c.installCommand(), c.uninstallCommand(), c.cleanupCommand(), c.configCommand(), c.serviceCommand())
	return root
}

func (c *commandSet) uninstallCommand() *cobra.Command {
	var trust, scriptURL string
	cmd := &cobra.Command{
		Use: "uninstall <sing-box|xray>", Short: "卸载指定内核", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
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
				fmt.Fprintf(c.out, "安装/升级内核：%s\n\n", args[0])
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

func (c *commandSet) configCommand() *cobra.Command {
	cmd := &cobra.Command{Use: "config", Short: "管理服务端、客户端和节点凭据"}
	cmd.AddCommand(c.generateCommand(), c.clientCommand(), c.resetCommand())
	return cmd
}

func (c *commandSet) resetCommand() *cobra.Command {
	var opts domain.ResetOptions
	cmd := &cobra.Command{
		Use: "reset <sing-box|xray>", Short: "重置 UUID、REALITY 密钥和 short ID", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if !c.yes {
				if !readerInteractive(c.in) {
					return fmt.Errorf("非交互模式重置凭据必须显式提供 --yes")
				}
				if err := c.fillReset(cmd.Context(), args[0], &opts); err != nil {
					return err
				}
				confirmed, err := c.confirmCredentialReset(args[0], opts)
				if err != nil {
					return err
				}
				if !confirmed {
					return fmt.Errorf("用户取消凭据重置")
				}
			}
			return c.runCredentialReset(cmd.Context(), args[0], opts)
		},
	}
	cmd.Flags().StringVar(&opts.SNI, "sni", "", "重置时使用的新 SNI（默认保留当前值）")
	cmd.Flags().StringVar(&opts.Target, "target", "", "新的 REALITY target（仅修改 SNI 时默认新 SNI:443）")
	return cmd
}

func (c *commandSet) generateCommand() *cobra.Command {
	var o domain.GenerateOptions
	cmd := &cobra.Command{
		Use: "generate <sing-box|xray>", Short: "生成并启用服务端配置", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			interactive := !c.yes && readerInteractive(c.in)
			o.NonInteractive = !interactive
			if interactive {
				if err := c.fillGenerate(cmd.Context(), args[0], &o); err != nil {
					if errors.Is(err, errReturnToMenu) {
						fmt.Fprintln(c.out, "已取消生成服务端配置。")
						return nil
					}
					return err
				}
			}
			err := c.runGenerate(cmd.Context(), args[0], o)
			if errors.Is(err, errReturnToMenu) {
				fmt.Fprintln(c.out, "已取消生成服务端配置。")
				return nil
			}
			return err
		},
	}
	cmd.Flags().StringVar(&o.Server, "server", "", "客户端连接的公网 IP 或域名")
	cmd.Flags().IntVar(&o.Port, "port", 0, "监听 TCP 端口")
	cmd.Flags().StringVar(&o.SNI, "sni", "", "REALITY SNI 域名")
	cmd.Flags().StringVar(&o.Target, "target", "", "REALITY 目标 host:port（默认 SNI:443）")
	cmd.Flags().StringVar(&o.UserName, "user-name", "", "服务端用户名称（默认 proxyforge-user）")
	cmd.Flags().StringVar(&o.InboundTag, "inbound-tag", "", "入站标签（默认按内核自动生成）")
	cmd.Flags().BoolVar(&o.RotateCredentials, "rotate-credentials", false, "轮换 UUID、密钥和 short ID，使旧客户端失效")
	cmd.Flags().Bool("take-over", false, "兼容旧版本；当前始终备份并完整覆盖现有配置")
	_ = cmd.Flags().MarkDeprecated("take-over", "当前生成流程会自动备份并完整覆盖现有配置，无需此参数")
	_ = cmd.Flags().MarkHidden("take-over")
	return cmd
}

func (c *commandSet) clientCommand() *cobra.Command {
	var output string
	var format string
	var force bool
	cmd := &cobra.Command{
		Use: "client <sing-box|xray>", Short: "输出原生 JSON 或 Clash YAML 客户端配置", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			b, err := c.app.ClientConfig(cmd.Context(), args[0], format, output, force)
			if err != nil {
				return err
			}
			if output == "" {
				_, err = c.out.Write(b)
			} else {
				fmt.Fprintf(c.out, "客户端配置已安全写入 %s（0600）\n", output)
			}
			return err
		},
	}
	cmd.Flags().StringVar(&format, "format", app.ClientFormatNative, "客户端格式：native 或 clash（Mihomo/Clash Meta）")
	cmd.Flags().StringVarP(&output, "output", "o", "", "写入文件（默认 stdout）")
	cmd.Flags().BoolVar(&force, "force", false, "覆盖已有输出文件")
	return cmd
}

func (c *commandSet) serviceCommand() *cobra.Command {
	return &cobra.Command{
		Use: "service <sing-box|xray> <start|stop|restart|status|logs>", Short: "管理单个内核的 systemd 服务", Args: cobra.ExactArgs(2),
		ValidArgsFunction: func(_ *cobra.Command, _ []string, _ string) ([]string, cobra.ShellCompDirective) {
			return []string{"sing-box", "xray", "start", "stop", "restart", "status", "logs"}, cobra.ShellCompDirectiveNoFileComp
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			b, err := c.app.Service(cmd.Context(), args[0], args[1])
			if len(b) > 0 {
				fmt.Fprint(c.out, string(b))
				if b[len(b)-1] != '\n' {
					fmt.Fprintln(c.out)
				}
			}
			return err
		},
	}
}

func (c *commandSet) fillGenerate(ctx context.Context, core string, o *domain.GenerateOptions) error {
	c.clearScreen()
	fmt.Fprintf(c.out, "生成服务端配置：%s\n\n", core)
	fmt.Fprintln(c.out, "提示：任意输入步骤输入 q 可取消并返回主菜单。")
	if o.Server == "" {
		detected, err := c.selectPublicAddress(ctx)
		if err != nil {
			return err
		}
		o.Server, err = c.askDefaultCancelable("公网 IP 或域名", detected)
		if err != nil {
			return err
		}
	}
	if o.Port == 0 {
		v, err := c.askDefaultCancelable("监听端口", strconv.Itoa(app.DefaultPort(c.app.Store, core)))
		if err != nil {
			return err
		}
		port, err := strconv.Atoi(v)
		if err != nil {
			return fmt.Errorf("端口无效: %w", err)
		}
		o.Port = port
	}
	if o.UserName == "" {
		defaultUserName := domain.DefaultUserName
		if c.app != nil {
			if current, err := c.app.Store.Load(core); err == nil && strings.TrimSpace(current.UserName) != "" {
				defaultUserName = current.UserName
			}
		}
		var err error
		o.UserName, err = c.askDefaultCancelable("用户名称", defaultUserName)
		if err != nil {
			return err
		}
	}
	if o.InboundTag == "" {
		defaultInboundTag := domain.DefaultInboundTag(core)
		if c.app != nil {
			if current, err := c.app.Store.Load(core); err == nil && strings.TrimSpace(current.InboundTag) != "" {
				defaultInboundTag = current.InboundTag
			}
		}
		var err error
		o.InboundTag, err = c.askDefaultCancelable("入站标签", defaultInboundTag)
		if err != nil {
			return err
		}
	}
	if o.SNI == "" {
		var err error
		o.SNI, err = c.askDefaultCancelable("REALITY SNI（输入域名；直接回车自动测速候选）", "")
		if err != nil {
			return err
		}
		if o.SNI == "" {
			selected, err := c.selectSNICandidate(ctx, o.Server)
			if err != nil {
				return fmt.Errorf("自动选择 SNI 失败: %w", err)
			}
			o.SNI = selected
			if c.interactiveUI() {
				c.clearScreen()
				fmt.Fprintf(c.out, "生成服务端配置：%s\n\n已选择 REALITY SNI：%s\n", core, o.SNI)
			}
		}
	}
	if o.Target == "" {
		o.Target = netJoinHostPort(o.SNI, "443")
	}
	fmt.Fprintf(c.out, "将使用 SNI %s、目标 %s。请确认该目标归属可信且允许作为 REALITY 回落站点。\n", o.SNI, o.Target)
	ok, err := c.confirmCancelable("确认 SNI 和 REALITY target？输入 yes/y 继续，输入 q 返回主菜单")
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("用户取消配置生成")
	}
	return nil
}

func (c *commandSet) selectPublicAddress(ctx context.Context) (string, error) {
	physicalIPs := c.physicalIPs
	if physicalIPs == nil {
		physicalIPs = app.PhysicalPublicAddresses
	}
	externalIP := c.externalIP
	if externalIP == nil {
		externalIP = app.PublicAddress
	}
	for {
		fmt.Fprintln(c.out, "公网地址获取方式")
		fmt.Fprintln(c.out, "1) 从物理网卡获取（默认）")
		fmt.Fprintln(c.out, "2) 通过 api.ipify.org HTTPS 探测")
		fmt.Fprintln(c.out, "3) 手动输入")
		choice, err := c.chooseNumberCancelable("请选择", 1, 3, 1)
		if err != nil {
			return "", err
		}
		if choice == 3 {
			return "", nil
		}
		var address string
		if choice == 1 {
			var addresses []app.PublicInterfaceAddress
			addresses, err = physicalIPs()
			if err == nil {
				address, err = c.selectPhysicalPublicAddress(addresses)
			}
		} else {
			address, err = externalIP(ctx)
		}
		if err == nil {
			fmt.Fprintf(c.out, "已获取公网地址：%s\n", address)
			return address, nil
		}
		fmt.Fprintf(c.out, "获取公网地址失败：%v，请重新选择。\n\n", err)
	}
}

func (c *commandSet) selectPhysicalPublicAddress(addresses []app.PublicInterfaceAddress) (string, error) {
	if len(addresses) == 0 {
		return "", fmt.Errorf("物理网卡没有可用公网地址")
	}
	if len(addresses) == 1 {
		return addresses[0].Address, nil
	}
	fmt.Fprintln(c.out, "检测到多个物理网卡公网地址：")
	for index, item := range addresses {
		family := "IPv4"
		if strings.Contains(item.Address, ":") {
			family = "IPv6"
		}
		fmt.Fprintf(c.out, "%d) %s  %s（%s）\n", index+1, item.Interface, item.Address, family)
	}
	choice, err := c.chooseNumberCancelable("请选择公网地址", 1, len(addresses), 1)
	if err != nil {
		return "", err
	}
	return addresses[choice-1].Address, nil
}

func (c *commandSet) runGenerate(ctx context.Context, core string, o domain.GenerateOptions) error {
	n, err := c.app.Generate(ctx, core, o)
	if err != nil {
		return err
	}
	printGenerateSuccess(c.out, n)
	return nil
}

func printGenerateSuccess(w io.Writer, n domain.NodeSpec) {
	const border = "========================================================"
	fmt.Fprintln(w)
	fmt.Fprintln(w, border)
	fmt.Fprintf(w, "  %s 服务端配置生成成功\n", n.Core)
	fmt.Fprintln(w, border)
	fmt.Fprintf(w, "服务状态：active（运行中）\n")
	fmt.Fprintf(w, "连接地址：%s\n", netJoinHostPort(n.Server, strconv.Itoa(n.Port)))
	fmt.Fprintf(w, "REALITY SNI：%s\n", n.SNI)
	fmt.Fprintf(w, "REALITY target：%s\n", n.Target)
	fmt.Fprintf(w, "用户名称：%s\n", n.UserName)
	fmt.Fprintf(w, "入站标签：%s\n", n.InboundTag)
	fmt.Fprintf(w, "内核版本：%s\n", n.CoreVersion)
	fmt.Fprintln(w, border)
	fmt.Fprintln(w, "提示：请从“查看客户端配置”导出并妥善保管客户端配置。")
}

func (c *commandSet) menu(ctx context.Context) error {
	for {
		core, selected, err := c.selectCore()
		if err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
		if !selected {
			fmt.Fprintln(c.out, "已退出 ProxyForge。")
			return nil
		}
		if err := c.coreMenu(ctx, core); err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
	}
}

func (c *commandSet) coreMenu(ctx context.Context, core string) error {
	for {
		c.clearScreen()
		c.printCoreMenu(core)
		choice, err := c.chooseNumber("请选择", 0, 7, 0)
		if err != nil {
			return err
		}
		if choice == 0 {
			return nil
		}

		c.clearScreen()
		shouldPause := true
		switch choice {
		case 1:
			var confirmed bool
			confirmed, err = c.confirmInstall(core)
			if err == nil && confirmed {
				err = c.app.Install(ctx, core, install.Options{Confirm: c.confirm})
			} else if err == nil {
				fmt.Fprintln(c.out, "已取消安装/升级。")
			}
		case 2:
			shouldPause = false
			err = c.serverConfigMenu(ctx, core)
		case 3:
			shouldPause, err = c.clientMenu(ctx, core)
		case 4:
			shouldPause, err = c.resetMenu(ctx, core)
		case 5:
			shouldPause = false
			err = c.serviceMenu(ctx, core)
		case 6:
			var confirmed bool
			confirmed, err = c.confirmUninstall(core)
			if err == nil && confirmed {
				err = c.app.Uninstall(ctx, core, install.Options{Confirm: c.confirm})
			} else if err == nil {
				fmt.Fprintln(c.out, "已取消卸载。")
			}
		case 7:
			var confirmed bool
			confirmed, err = c.confirmCleanup(core)
			if err == nil && confirmed {
				err = c.app.Cleanup(ctx, core)
			} else if err == nil {
				fmt.Fprintln(c.out, "已取消清理。")
			}
		}
		if err != nil {
			c.printMenuError(err)
		}
		if shouldPause {
			c.pauseForMenu()
		}
	}
}

func (c *commandSet) serverConfigMenu(ctx context.Context, core string) error {
	for {
		c.clearScreen()
		fmt.Fprintf(c.out, "服务端配置管理：%s\n\n", core)
		fmt.Fprintln(c.out, "1) 生成/更新服务端配置")
		fmt.Fprintln(c.out, "2) 查看当前配置")
		fmt.Fprintln(c.out, "0) 返回内核菜单")
		choice, err := c.chooseNumber("请选择", 0, 2, 0)
		if err != nil {
			return err
		}
		if choice == 0 {
			return nil
		}

		c.clearScreen()
		switch choice {
		case 1:
			o := domain.GenerateOptions{}
			if err = c.fillGenerate(ctx, core, &o); err == nil {
				err = c.runGenerate(ctx, core, o)
			}
			if errors.Is(err, errReturnToMenu) {
				fmt.Fprintln(c.out, "已取消生成服务端配置。")
				return nil
			}
		case 2:
			var b []byte
			b, err = c.app.ServerConfig(core)
			if err == nil {
				// ServerConfig may emit progress on stderr. Clear once more after the
				// read so merged terminal streams cannot split the displayed JSON.
				c.clearScreen()
				fmt.Fprintln(c.out, "警告：当前服务端配置可能包含 UUID、REALITY 私钥等敏感信息，请勿泄露。")
				fmt.Fprintln(c.out, "----------------------------------------")
				_, err = c.out.Write(b)
				if err == nil && len(b) > 0 && b[len(b)-1] != '\n' {
					fmt.Fprintln(c.out)
				}
			}
		}
		if err != nil {
			c.printMenuError(err)
		}
		c.pauseForMenu()
	}
}

func (c *commandSet) clientMenu(ctx context.Context, core string) (bool, error) {
	c.clearScreen()
	fmt.Fprintf(c.out, "查看客户端配置：%s\n\n", core)
	fmt.Fprintln(c.out, "1) 内核原生 JSON")
	fmt.Fprintln(c.out, "2) Clash YAML（Mihomo/Clash Meta）")
	fmt.Fprintln(c.out, "0) 返回内核菜单")
	choice, err := c.chooseNumber("请选择", 0, 2, 1)
	if err != nil {
		return false, err
	}
	if choice == 0 {
		return false, nil
	}
	format := app.ClientFormatNative
	if choice == 2 {
		format = app.ClientFormatClash
	}
	b, err := c.app.ClientConfig(ctx, core, format, "", false)
	if err == nil {
		_, err = c.out.Write(b)
	}
	return true, err
}

func (c *commandSet) printCoreMenu(core string) {
	fmt.Fprintln(c.out)
	fmt.Fprintln(c.out, "========================================")
	fmt.Fprintf(c.out, "       %s 管理菜单\n", core)
	fmt.Fprintln(c.out, "========================================")
	fmt.Fprintln(c.out, "1) 安装/升级内核")
	fmt.Fprintln(c.out, "2) 服务端配置管理")
	fmt.Fprintln(c.out, "3) 查看客户端配置")
	fmt.Fprintln(c.out, "4) 重置节点/凭证")
	fmt.Fprintln(c.out, "5) 管理服务")
	fmt.Fprintln(c.out, "6) 卸载内核")
	fmt.Fprintln(c.out, "7) 清理卸载残留")
	fmt.Fprintln(c.out, "0) 返回内核选择")
	fmt.Fprintln(c.out, "----------------------------------------")
}

func (c *commandSet) confirmUninstall(core string) (bool, error) {
	fmt.Fprintf(c.out, "即将停止并卸载 %s，删除其 ProxyForge 状态和受管活动配置。\n", core)
	fmt.Fprintln(c.out, "客户端将立即失效；历史备份和安装脚本信任记录会保留。")
	return c.confirm("确认卸载？输入 yes/y 继续")
}

func (c *commandSet) confirmInstall(core string) (bool, error) {
	fmt.Fprintf(c.out, "即将安装或升级 %s。\n", core)
	fmt.Fprintln(c.out, "此操作会下载并执行官方安装方式，可能修改内核二进制、软件包文件和 systemd unit；现有配置会先备份。")
	return c.confirm("确认安装/升级？输入 yes/y 继续")
}

func (c *commandSet) confirmCleanup(target string) (bool, error) {
	fmt.Fprintf(c.out, "即将直接清理 %s 的卸载残留，不会创建新备份。\n", target)
	fmt.Fprintln(c.out, "配置目录、运行数据、文件日志、ProxyForge 状态、信任记录和历史备份都会永久删除。")
	return c.confirm("确认清理？输入 yes/y 继续")
}

func (c *commandSet) resetMenu(ctx context.Context, core string) (bool, error) {
	c.clearScreen()
	fmt.Fprintf(c.out, "重置 %s\n", core)
	fmt.Fprintln(c.out, "1) 重置节点（重选 SNI/target，并重置凭证）")
	fmt.Fprintln(c.out, "2) 重置凭证（保留 SNI/target，仅重置 UUID、REALITY 密钥和 short ID）")
	fmt.Fprintln(c.out, "0) 返回内核菜单")
	choice, err := c.chooseNumber("请选择", 0, 2, 1)
	if err != nil {
		return false, err
	}
	if choice == 0 {
		return false, nil
	}

	opts := domain.ResetOptions{}
	if choice == 1 {
		if err := c.fillReset(ctx, core, &opts); err != nil {
			return true, err
		}
		confirmed, err := c.confirmCredentialReset(core, opts)
		if err != nil {
			return true, err
		}
		if !confirmed {
			fmt.Fprintln(c.out, "已取消节点重置。")
			return true, nil
		}
	} else {
		c.clearScreen()
		fmt.Fprintf(c.out, "重置凭证：%s\n\n", core)
		confirmed, err := c.confirmCredentialOnlyReset(core)
		if err != nil {
			return true, err
		}
		if !confirmed {
			fmt.Fprintln(c.out, "已取消凭证重置。")
			return true, nil
		}
	}
	return true, c.runCredentialReset(ctx, core, opts)
}

func (c *commandSet) fillReset(ctx context.Context, core string, opts *domain.ResetOptions) error {
	current, err := c.app.Store.Load(core)
	if err != nil {
		return err
	}
	c.clearScreen()
	fmt.Fprintf(c.out, "重置节点：%s\n\n", core)
	if opts.SNI == "" {
		opts.SNI = c.askDefault("新的 REALITY SNI（输入域名；直接回车自动测速候选）", "")
		if opts.SNI == "" {
			selected, err := c.selectSNICandidate(ctx, current.Server)
			if err != nil {
				return fmt.Errorf("自动选择 SNI 失败: %w", err)
			}
			opts.SNI = selected
			if c.interactiveUI() {
				c.clearScreen()
				fmt.Fprintf(c.out, "重置节点：%s\n\n已选择 REALITY SNI：%s\n", core, opts.SNI)
			}
		}
	}
	if opts.Target == "" {
		defaultTarget := netJoinHostPort(opts.SNI, "443")
		opts.Target = c.askDefault("新的 REALITY target", defaultTarget)
	}
	return nil
}

func (c *commandSet) confirmCredentialReset(core string, opts domain.ResetOptions) (bool, error) {
	fmt.Fprintf(c.out, "即将重置 %s 的 UUID、REALITY 密钥和 short ID；所有旧客户端配置会立即失效。\n", core)
	fmt.Fprintf(c.out, "新 SNI：%s\n新 target：%s\n", opts.SNI, opts.Target)
	return c.confirm("确认重置？输入 yes/y 继续")
}

func (c *commandSet) confirmCredentialOnlyReset(core string) (bool, error) {
	current, err := c.app.Store.Load(core)
	if err != nil {
		return false, err
	}
	fmt.Fprintf(c.out, "即将仅重置 %s 的 UUID、REALITY 密钥和 short ID；所有旧客户端配置会立即失效。\n", core)
	fmt.Fprintf(c.out, "SNI 和 target 保持不变：%s，%s\n", current.SNI, current.Target)
	return c.confirm("确认重置凭证？输入 yes/y 继续")
}

func (c *commandSet) runCredentialReset(ctx context.Context, core string, opts domain.ResetOptions) error {
	n, err := c.app.ResetCredentials(ctx, core, opts)
	if err != nil {
		return err
	}
	fmt.Fprintf(c.out, "%s 节点凭据已全部重置，SNI=%s，target=%s；请重新导出并分发客户端配置。\n", core, n.SNI, n.Target)
	return nil
}

func (c *commandSet) selectCore() (string, bool, error) {
	c.clearScreen()
	fmt.Fprintln(c.out, "========================================")
	fmt.Fprintln(c.out, "       ProxyForge 双内核代理管理器")
	fmt.Fprintln(c.out, "========================================")
	fmt.Fprintln(c.out, "请选择要管理的内核")
	fmt.Fprintln(c.out, "1) sing-box")
	fmt.Fprintln(c.out, "2) Xray-core")
	fmt.Fprintln(c.out, "0) 退出")
	fmt.Fprintln(c.out, "----------------------------------------")
	choice, err := c.chooseNumber("请选择", 0, 2, 1)
	if err != nil {
		return "", false, err
	}
	switch choice {
	case 1:
		return domain.CoreSingBox, true, nil
	case 2:
		return domain.CoreXray, true, nil
	default:
		return "", false, nil
	}
}

func (c *commandSet) serviceMenu(ctx context.Context, core string) error {
	actions := []string{"", "start", "stop", "restart", "status", "logs"}
	for {
		c.clearScreen()
		fmt.Fprintf(c.out, "服务管理：%s\n", core)
		fmt.Fprintln(c.out, "1) 启动服务")
		fmt.Fprintln(c.out, "2) 停止服务")
		fmt.Fprintln(c.out, "3) 重启服务")
		fmt.Fprintln(c.out, "4) 查看状态")
		fmt.Fprintln(c.out, "5) 查看最近日志")
		fmt.Fprintln(c.out, "0) 返回内核菜单")
		choice, chooseErr := c.chooseNumber("请选择", 0, 5, 4)
		if chooseErr != nil {
			return chooseErr
		}
		if choice == 0 {
			return nil
		}
		b, actionErr := c.app.Service(ctx, core, actions[choice])
		if len(b) > 0 {
			fmt.Fprint(c.out, string(b))
			if b[len(b)-1] != '\n' {
				fmt.Fprintln(c.out)
			}
		}
		if actionErr != nil {
			c.printMenuError(actionErr)
		}
		c.pauseForMenu()
	}
}

func (c *commandSet) chooseNumber(label string, min, max, def int) (int, error) {
	return c.chooseNumberInput(label, min, max, def, false)
}

func (c *commandSet) chooseNumberCancelable(label string, min, max, def int) (int, error) {
	return c.chooseNumberInput(label, min, max, def, true)
}

func (c *commandSet) chooseNumberInput(label string, min, max, def int, cancelable bool) (int, error) {
	invalidShown := false
	for {
		if def >= min && def <= max {
			fmt.Fprintf(c.out, "%s [%d]: ", label, def)
		} else {
			fmt.Fprintf(c.out, "%s: ", label)
		}
		line, err := c.reader.ReadString('\n')
		if err != nil && len(line) == 0 {
			return 0, err
		}
		value := strings.TrimSpace(line)
		if cancelable && strings.EqualFold(value, "q") {
			return 0, errReturnToMenu
		}
		if value == "" && def >= min && def <= max {
			return def, nil
		}
		choice, parseErr := strconv.Atoi(value)
		if parseErr == nil && choice >= min && choice <= max {
			return choice, nil
		}
		if c.interactiveUI() {
			eraseChoiceRetry(c.out, invalidShown)
		}
		if cancelable {
			fmt.Fprintf(c.out, "无效选择，请输入 %d 到 %d 之间的数字，或输入 q 返回主菜单。\n", min, max)
		} else {
			fmt.Fprintf(c.out, "无效选择，请输入 %d 到 %d 之间的数字。\n", min, max)
		}
		invalidShown = true
	}
}

func eraseChoiceRetry(w io.Writer, invalidShown bool) {
	erasePreviousLine := "\033[1A\r\033[2K"
	fmt.Fprint(w, erasePreviousLine)
	if invalidShown {
		fmt.Fprint(w, erasePreviousLine)
	}
}

func (c *commandSet) printMenuError(err error) {
	fmt.Fprintf(c.errOut, "操作失败：%v\n", err)
}

func (c *commandSet) askDefault(label, def string) string {
	value, _ := c.askDefaultInput(label, def, false)
	return value
}

func (c *commandSet) askDefaultCancelable(label, def string) (string, error) {
	return c.askDefaultInput(label, def, true)
}

func (c *commandSet) askDefaultInput(label, def string, cancelable bool) (string, error) {
	if def != "" {
		fmt.Fprintf(c.out, "%s [%s]: ", label, def)
	} else {
		fmt.Fprintf(c.out, "%s: ", label)
	}
	line, err := c.reader.ReadString('\n')
	if err == nil || len(line) > 0 {
		v := strings.TrimSpace(line)
		if cancelable && strings.EqualFold(v, "q") {
			return "", errReturnToMenu
		}
		if v != "" {
			return v, nil
		}
	}
	return def, nil
}

func (c *commandSet) confirm(message string) (bool, error) {
	return c.confirmInput(message, false)
}

func (c *commandSet) confirmCancelable(message string) (bool, error) {
	return c.confirmInput(message, true)
}

func (c *commandSet) confirmInput(message string, cancelable bool) (bool, error) {
	fmt.Fprint(c.out, message+" ")
	line, err := c.reader.ReadString('\n')
	if err != nil && len(line) == 0 {
		return false, err
	}
	value := strings.TrimSpace(line)
	if cancelable && strings.EqualFold(value, "q") {
		return false, errReturnToMenu
	}
	return strings.EqualFold(value, "yes") || strings.EqualFold(value, "y"), nil
}

func readerInteractive(r io.Reader) bool {
	f, ok := r.(*os.File)
	if !ok {
		return false
	}
	info, err := f.Stat()
	return err == nil && (info.Mode()&os.ModeCharDevice) != 0
}

func writerInteractive(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	info, err := f.Stat()
	return err == nil && (info.Mode()&os.ModeCharDevice) != 0
}

func (c *commandSet) interactiveUI() bool {
	return !c.yes && os.Getenv("TERM") != "dumb" && readerInteractive(c.in) && writerInteractive(c.out)
}

func (c *commandSet) clearScreen() {
	if c.interactiveUI() {
		clearTerminal(c.out)
	}
}

func clearTerminal(w io.Writer) {
	// CSI 2 J clears the visible screen; CSI 3 J also clears saved scrollback.
	// Both are needed for terminal frontends that otherwise retain every menu
	// redraw and make one configuration appear several times.
	fmt.Fprint(w, "\033[H\033[2J\033[3J")
}

func (c *commandSet) pauseForMenu() {
	if !c.interactiveUI() {
		return
	}
	fmt.Fprint(c.out, "\n按 Enter 返回菜单……")
	_, _ = c.reader.ReadString('\n')
}

func netJoinHostPort(host, port string) string {
	if strings.Contains(host, ":") && !strings.HasPrefix(host, "[") {
		return "[" + host + "]:" + port
	}
	return host + ":" + port
}
