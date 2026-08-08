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
	runner := system.ExecRunner{}
	layout := system.Layout{Root: os.Getenv("PROXYFORGE_ROOT")}
	reg := provider.NewRegistry(singbox.New(), xray.New())
	a := app.New(reg, runner, layout, os.Stdout)
	c := &commandSet{
		app: a, in: os.Stdin, reader: bufio.NewReader(os.Stdin), out: os.Stdout, errOut: os.Stderr,
		probeSNI: app.ProbeSNICandidates, randomIndex: secureRandomIndex,
	}
	root := &cobra.Command{
		Use: "proxyforge", Short: "Linux 双内核 VLESS + REALITY + Vision 管理器", Version: version,
		SilenceUsage: true, SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error { return c.menu(cmd.Context()) },
	}
	root.SetOut(os.Stdout)
	root.SetErr(os.Stderr)
	root.SetIn(os.Stdin)
	root.PersistentFlags().BoolVarP(&c.yes, "yes", "y", false, "非交互模式（安装仍必须提供脚本 SHA-256）")
	root.AddCommand(c.installCommand(false), c.installCommand(true), c.configCommand(), c.serviceCommand())
	return root
}

func (c *commandSet) installCommand(upgrade bool) *cobra.Command {
	name := "install"
	desc := "安装内核"
	if upgrade {
		name, desc = "upgrade", "升级内核"
	}
	var version, trust, scriptURL string
	cmd := &cobra.Command{
		Use: name + " <sing-box|xray>", Short: desc, Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			nonInteractive := c.yes || !readerInteractive(c.in)
			opts := install.Options{URL: scriptURL, Version: version, Upgrade: upgrade, NonInteractive: nonInteractive, TrustScriptSHA256: trust, Confirm: c.confirm}
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
	var force bool
	cmd := &cobra.Command{
		Use: "client <sing-box|xray>", Short: "输出完整客户端 JSON", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			b, err := c.app.Client(cmd.Context(), args[0], output, force)
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
		}
	}
	if o.Target == "" {
		o.Target = netJoinHostPort(o.SNI, "443")
	}
	fmt.Fprintf(c.out, "将使用 SNI %s、目标 %s。请确认该目标归属可信且允许作为 REALITY 回落站点。\n", o.SNI, o.Target)
	ok, err := c.confirm("确认 SNI 和 REALITY target？输入 yes 继续")
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
		ok, confirmErr := c.confirm("是否备份并接管现有配置？输入 yes 继续")
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
		c.printMainMenu()
		choice, err := c.chooseNumber("请选择", 0, 6, 0)
		if err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
		if choice == 0 {
			fmt.Fprintln(c.out, "已退出 ProxyForge。")
			return nil
		}

		if choice == 6 {
			if err := c.serviceMenu(ctx); err != nil {
				if err == io.EOF {
					return nil
				}
				c.printMenuError(err)
			}
			continue
		}

		core, selected, err := c.selectCore()
		if err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
		if !selected {
			continue
		}

		switch choice {
		case 1, 2:
			err = c.app.Install(ctx, core, install.Options{Upgrade: choice == 2, Confirm: c.confirm})
		case 3:
			o := domain.GenerateOptions{}
			if err = c.fillGenerate(ctx, core, &o); err == nil {
				err = c.runGenerate(ctx, core, o, true)
			}
		case 4:
			var b []byte
			b, err = c.app.Client(ctx, core, "", false)
			if err == nil {
				_, err = c.out.Write(b)
			}
		case 5:
			err = c.resetMenu(ctx, core)
		}
		if err != nil {
			c.printMenuError(err)
		}
	}
}

func (c *commandSet) printMainMenu() {
	fmt.Fprintln(c.out)
	fmt.Fprintln(c.out, "========================================")
	fmt.Fprintln(c.out, "       ProxyForge 双内核代理管理器")
	fmt.Fprintln(c.out, "========================================")
	fmt.Fprintln(c.out, "1) 安装内核")
	fmt.Fprintln(c.out, "2) 升级内核")
	fmt.Fprintln(c.out, "3) 生成服务端配置")
	fmt.Fprintln(c.out, "4) 查看客户端配置")
	fmt.Fprintln(c.out, "5) 重置节点/凭证")
	fmt.Fprintln(c.out, "6) 管理服务")
	fmt.Fprintln(c.out, "0) 退出")
	fmt.Fprintln(c.out, "----------------------------------------")
}

func (c *commandSet) resetMenu(ctx context.Context, core string) error {
	fmt.Fprintln(c.out)
	fmt.Fprintf(c.out, "重置 %s\n", core)
	fmt.Fprintln(c.out, "1) 重置节点（重选 SNI/target，并重置凭证）")
	fmt.Fprintln(c.out, "2) 重置凭证（保留 SNI/target，仅重置 UUID、REALITY 密钥和 short ID）")
	fmt.Fprintln(c.out, "0) 返回主菜单")
	choice, err := c.chooseNumber("请选择", 0, 2, 1)
	if err != nil || choice == 0 {
		return err
	}

	opts := domain.ResetOptions{}
	if choice == 1 {
		if err := c.fillReset(ctx, core, &opts); err != nil {
			return err
		}
		confirmed, err := c.confirmCredentialReset(core, opts)
		if err != nil {
			return err
		}
		if !confirmed {
			fmt.Fprintln(c.out, "已取消节点重置。")
			return nil
		}
	} else {
		confirmed, err := c.confirmCredentialOnlyReset(core)
		if err != nil {
			return err
		}
		if !confirmed {
			fmt.Fprintln(c.out, "已取消凭证重置。")
			return nil
		}
	}
	return c.runCredentialReset(ctx, core, opts)
}

func (c *commandSet) fillReset(ctx context.Context, core string, opts *domain.ResetOptions) error {
	current, err := c.app.Store.Load(core)
	if err != nil {
		return err
	}
	if opts.SNI == "" {
		opts.SNI = c.askDefault("新的 REALITY SNI（输入域名；直接回车自动测速候选）", "")
		if opts.SNI == "" {
			selected, err := c.selectSNICandidate(ctx, current.Server)
			if err != nil {
				return fmt.Errorf("自动选择 SNI 失败: %w", err)
			}
			opts.SNI = selected
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
	return c.confirm("确认重置？输入 yes 继续")
}

func (c *commandSet) confirmCredentialOnlyReset(core string) (bool, error) {
	current, err := c.app.Store.Load(core)
	if err != nil {
		return false, err
	}
	fmt.Fprintf(c.out, "即将仅重置 %s 的 UUID、REALITY 密钥和 short ID；所有旧客户端配置会立即失效。\n", core)
	fmt.Fprintf(c.out, "SNI 和 target 保持不变：%s，%s\n", current.SNI, current.Target)
	return c.confirm("确认重置凭证？输入 yes 继续")
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
	fmt.Fprintln(c.out)
	fmt.Fprintln(c.out, "请选择内核")
	fmt.Fprintln(c.out, "1) sing-box")
	fmt.Fprintln(c.out, "2) Xray-core")
	fmt.Fprintln(c.out, "0) 返回主菜单")
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

func (c *commandSet) serviceMenu(ctx context.Context) error {
	core, selected, err := c.selectCore()
	if err != nil || !selected {
		return err
	}
	actions := []string{"", "start", "stop", "restart", "status", "logs"}
	for {
		fmt.Fprintln(c.out)
		fmt.Fprintf(c.out, "服务管理：%s\n", core)
		fmt.Fprintln(c.out, "1) 启动服务")
		fmt.Fprintln(c.out, "2) 停止服务")
		fmt.Fprintln(c.out, "3) 重启服务")
		fmt.Fprintln(c.out, "4) 查看状态")
		fmt.Fprintln(c.out, "5) 查看最近日志")
		fmt.Fprintln(c.out, "0) 返回主菜单")
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
	}
}

func (c *commandSet) chooseNumber(label string, min, max, def int) (int, error) {
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
		fmt.Fprintf(c.out, "无效选择，请输入 %d 到 %d 之间的数字。\n", min, max)
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
	return strings.EqualFold(strings.TrimSpace(line), "yes"), nil
}

func readerInteractive(r io.Reader) bool {
	f, ok := r.(*os.File)
	if !ok {
		return false
	}
	info, err := f.Stat()
	return err == nil && (info.Mode()&os.ModeCharDevice) != 0
}

func netJoinHostPort(host, port string) string {
	if strings.Contains(host, ":") && !strings.HasPrefix(host, "[") {
		return "[" + host + "]:" + port
	}
	return host + ":" + port
}
