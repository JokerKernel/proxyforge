package cli

import (
	"bufio"
	"context"
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

type commandSet struct {
	app         *app.App
	in          io.Reader
	reader      *bufio.Reader
	out         io.Writer
	errOut      io.Writer
	yes         bool
	probeSNI    sniCandidateProbeFunc
	randomIndex func(int) int
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
					return err
				}
			}
			return c.runGenerate(cmd.Context(), args[0], o, interactive)
		},
	}
	cmd.Flags().StringVar(&o.Server, "server", "", "客户端连接的公网 IP 或域名")
	cmd.Flags().IntVar(&o.Port, "port", 0, "监听 TCP 端口")
	cmd.Flags().StringVar(&o.SNI, "sni", "", "REALITY SNI 域名")
	cmd.Flags().StringVar(&o.Target, "target", "", "REALITY 目标 host:port（默认 SNI:443）")
	cmd.Flags().BoolVar(&o.RotateCredentials, "rotate-credentials", false, "轮换 UUID、密钥和 short ID，使旧客户端失效")
	cmd.Flags().BoolVar(&o.TakeOver, "take-over", false, "备份并接管现有未知配置")
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
	if o.Server == "" {
		detected, err := app.PublicAddress(ctx)
		if err != nil {
			fmt.Fprintln(c.errOut, "自动探测公网地址失败:", err)
		}
		o.Server = c.askDefault("公网 IP 或域名", detected)
	}
	if o.Port == 0 {
		v := c.askDefault("监听端口", strconv.Itoa(app.DefaultPort(c.app.Store, core)))
		port, err := strconv.Atoi(v)
		if err != nil {
			return fmt.Errorf("端口无效: %w", err)
		}
		o.Port = port
	}
	if o.SNI == "" {
		o.SNI = c.askDefault("REALITY SNI（输入域名；直接回车自动测速候选）", "")
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
	ok, err := c.confirm("确认 SNI 和 REALITY target？输入 yes/y 继续")
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("用户取消配置生成")
	}
	return nil
}

func (c *commandSet) runGenerate(ctx context.Context, core string, o domain.GenerateOptions, interactive bool) error {
	n, err := c.app.Generate(ctx, core, o)
	if err != nil && interactive && !o.TakeOver && strings.Contains(err.Error(), "--take-over") {
		fmt.Fprintln(c.errOut, err)
		ok, confirmErr := c.confirm("是否备份并接管现有配置？输入 yes/y 继续")
		if confirmErr != nil {
			return confirmErr
		}
		if ok {
			o.TakeOver = true
			n, err = c.app.Generate(ctx, core, o)
		}
	}
	if err != nil {
		return err
	}
	fmt.Fprintf(c.out, "%s 节点已启用：%s:%d，SNI=%s，内核=%s\n", core, n.Server, n.Port, n.SNI, n.CoreVersion)
	return nil
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
			err = c.app.Install(ctx, core, install.Options{Confirm: c.confirm})
		case 2:
			o := domain.GenerateOptions{}
			if err = c.fillGenerate(ctx, core, &o); err == nil {
				err = c.runGenerate(ctx, core, o, true)
			}
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
	fmt.Fprintln(c.out, "2) 生成服务端配置")
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
		fmt.Fprintf(c.out, "无效选择，请输入 %d 到 %d 之间的数字。\n", min, max)
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
	if def != "" {
		fmt.Fprintf(c.out, "%s [%s]: ", label, def)
	} else {
		fmt.Fprintf(c.out, "%s: ", label)
	}
	line, err := c.reader.ReadString('\n')
	if err == nil || len(line) > 0 {
		v := strings.TrimSpace(line)
		if v != "" {
			return v
		}
	}
	return def
}

func (c *commandSet) confirm(message string) (bool, error) {
	fmt.Fprint(c.out, message+" ")
	line, err := c.reader.ReadString('\n')
	if err != nil && len(line) == 0 {
		return false, err
	}
	value := strings.TrimSpace(line)
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
		fmt.Fprint(c.out, "\033[H\033[2J")
	}
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
