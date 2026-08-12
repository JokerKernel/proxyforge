package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"proxyforge/internal/app"
	"proxyforge/internal/domain"
	"proxyforge/internal/install"
	"proxyforge/internal/provider"
	"proxyforge/internal/provider/singbox"
	"proxyforge/internal/provider/xray"
	"proxyforge/internal/selfupdate"
	"proxyforge/internal/system"
)

var errReturnToMenu = errors.New("返回主菜单")

const proxyForgeHeaderRule = "╰──────────────────────────────────────────────"

type confirmationSection struct {
	title string
	items []string
}

type commandSet struct {
	app              *app.App
	in               io.Reader
	reader           *bufio.Reader
	out              io.Writer
	errOut           io.Writer
	yes              bool
	probeSNI         sniCandidateProbeFunc
	physicalIPs      func() ([]app.PublicInterfaceAddress, error)
	externalIP       func(context.Context) (string, error)
	interruptContext func(context.Context) (context.Context, context.CancelFunc)
	currentVersion   string
	selfUpdate       func(context.Context, selfupdate.Options) error
}

func New(version string) *cobra.Command {
	return newCommand(version, app.RequireRoot)
}

func newCommand(version string, rootCheck func() error) *cobra.Command {
	stdout := system.NewTerminalColorWriter(os.Stdout)
	stderr := system.NewTerminalColorWriter(os.Stderr)
	runner := &system.LoggingRunner{Runner: system.ExecRunner{Stdin: os.Stdin}, Out: stderr}
	layout := system.Layout{Root: os.Getenv("PROXYFORGE_ROOT")}
	reg := provider.NewRegistry(singbox.New(), xray.New())
	a := app.New(reg, runner, layout, stdout)
	a.RootCheck = rootCheck
	a.Progress = stderr
	a.Installer.Output = stderr
	updater := selfupdate.Updater{Installer: a.Installer}
	c := &commandSet{
		app: a, in: os.Stdin, reader: bufio.NewReader(os.Stdin), out: stdout, errOut: stderr,
		probeSNI:    app.ProbeSNICandidates,
		physicalIPs: app.PhysicalInterfaceAddresses, externalIP: app.PublicAddress,
		currentVersion: strings.SplitN(version, "\n", 2)[0], selfUpdate: updater.Run,
	}
	root := &cobra.Command{
		Use: "proxyforge", Short: "Linux 双内核 VLESS + REALITY + Vision 管理器", Version: version,
		SilenceUsage: true, SilenceErrors: true,
		PersistentPreRunE: func(*cobra.Command, []string) error {
			fmt.Fprintln(c.errOut, "[步骤] 验证 root 运行权限")
			return rootCheck()
		},
		RunE: func(cmd *cobra.Command, args []string) error { return c.menu(cmd.Context()) },
	}
	root.SetOut(stdout)
	root.SetErr(stderr)
	root.SetIn(os.Stdin)
	root.SetVersionTemplate("{{.Name}} {{.Version}}\n")
	root.PersistentFlags().BoolVarP(&c.yes, "yes", "y", false, "非交互确认（内核管理脚本仍必须提供 SHA-256）")
	root.AddCommand(c.installCommand(), c.updateCommand(), c.uninstallCommand(), c.cleanupCommand(), c.configCommand(), c.serviceCommand())
	return root
}

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
			if err := c.confirmServerConfigOverwrite(args[0], interactive); err != nil {
				if errors.Is(err, errReturnToMenu) {
					fmt.Fprintln(c.out, "已取消生成服务端配置。")
					return nil
				}
				return err
			}
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
	cmd.Flags().StringVar(&o.UserName, "user-name", "", "服务端用户名称（默认 one）")
	cmd.Flags().StringVar(&o.InboundTag, "inbound-tag", "", "入站标签（默认按内核自动生成）")
	cmd.Flags().BoolVar(&o.StandardConfig, "standard-config", false, "使用不带本机回落防护的原标准配置")
	cmd.Flags().BoolVar(&o.SimplifiedConfig, "simplified-config", false, "sing-box 使用简化配置（系统 DNS、较少 DNS 日志，但私网域名拦截较弱）")
	cmd.Flags().BoolVar(&o.SingBoxFallbackGuard, "sing-box-fallback-guard", false, "sing-box 使用 direct 入站限制 REALITY 未认证回落流量")
	cmd.Flags().IntVar(&o.SingBoxFallbackPort, "sing-box-fallback-port", 0, "sing-box 防偷跑回落入站端口（默认 61432）")
	cmd.Flags().BoolVar(&o.SingBoxFallbackHTTPDomain, "sing-box-fallback-http-domain", false, "sing-box HTTP 回落仅放行与 SNI 一致的域名（默认不限制）")
	cmd.Flags().BoolVar(&o.XrayFallbackGuard, "xray-fallback-guard", false, "Xray 使用 dokodemo-door 限制 REALITY 未认证回落流量")
	cmd.Flags().IntVar(&o.XrayFallbackPort, "xray-fallback-port", 0, "Xray 防偷跑回落入站端口（默认 61431）")
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
	c.printPageHeader(core, "生成服务端配置")
	fmt.Fprintln(c.out, "提示：任意输入步骤输入 q 或 0 可取消并返回主菜单。")
	if core == domain.CoreSingBox {
		fmt.Fprintln(c.out, "\n配置模式")
		c.printMenuChoice("1", "标准安全配置（内部 DNS 解析后拦截私网和保留地址）")
		c.printMenuChoice("2", "简化配置（系统默认 DNS；DNS 日志较少，但域名解析到私网时可能绕过拦截）")
		c.printMenuChoice("3", "回落防偷跑配置（默认；direct 入站仅放行与 SNI 一致的 TLS 流量）")
		defaultChoice := 3
		if o.SingBoxFallbackGuard {
			defaultChoice = 3
		} else if o.SimplifiedConfig {
			defaultChoice = 2
		} else if o.StandardConfig {
			defaultChoice = 1
		}
		choice, err := c.chooseNumberCancelable("请选择配置模式", 1, 3, defaultChoice)
		if err != nil {
			return err
		}
		o.StandardConfig = choice == 1
		o.SimplifiedConfig = choice == 2
		o.SingBoxFallbackGuard = choice == 3
		if !o.SingBoxFallbackGuard {
			o.SingBoxFallbackPort = 0
			o.SingBoxFallbackHTTPDomain = false
		} else {
			fmt.Fprintln(c.out, "\nHTTP 回落域名限制")
			c.printMenuChoice("1", "不限制 HTTP Host（默认）")
			c.printMenuChoice("2", "仅放行与 SNI 一致的 HTTP Host")
			defaultHTTPChoice := 1
			if o.SingBoxFallbackHTTPDomain {
				defaultHTTPChoice = 2
			}
			choice, err := c.chooseNumberCancelable("请选择 HTTP 回落策略", 1, 2, defaultHTTPChoice)
			if err != nil {
				return err
			}
			o.SingBoxFallbackHTTPDomain = choice == 2
		}
	} else if core == domain.CoreXray {
		fmt.Fprintln(c.out, "\n配置模式")
		c.printMenuChoice("1", "标准配置（REALITY 未认证流量直接转发到 target）")
		c.printMenuChoice("2", "回落防偷跑配置（默认；dokodemo-door 仅放行与 SNI 一致的 TLS 流量）")
		defaultChoice := 2
		if o.StandardConfig {
			defaultChoice = 1
		}
		choice, err := c.chooseNumberCancelable("请选择配置模式", 1, 2, defaultChoice)
		if err != nil {
			return err
		}
		o.StandardConfig = choice == 1
		o.XrayFallbackGuard = choice == 2
		if !o.XrayFallbackGuard {
			o.XrayFallbackPort = 0
		}
	}
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
	if o.XrayFallbackGuard && o.XrayFallbackPort == 0 {
		defaultPort := domain.DefaultXrayFallbackPort
		if c.app != nil {
			if current, err := c.app.Store.Load(core); err == nil && current.XrayFallbackGuard && current.XrayFallbackPort != 0 {
				defaultPort = current.XrayFallbackPort
			}
		}
		v, err := c.askDefaultCancelable("本机 dokodemo-door 回落端口", strconv.Itoa(defaultPort))
		if err != nil {
			return err
		}
		port, err := strconv.Atoi(v)
		if err != nil {
			return fmt.Errorf("Xray 回落端口无效: %w", err)
		}
		o.XrayFallbackPort = port
	}
	if o.SingBoxFallbackGuard && o.SingBoxFallbackPort == 0 {
		defaultPort := domain.DefaultSingBoxFallbackPort
		if c.app != nil {
			if current, err := c.app.Store.Load(core); err == nil && current.SingBoxFallbackGuard && current.SingBoxFallbackPort != 0 {
				defaultPort = current.SingBoxFallbackPort
			}
		}
		v, err := c.askDefaultCancelable("本机 direct 回落端口", strconv.Itoa(defaultPort))
		if err != nil {
			return err
		}
		port, err := strconv.Atoi(v)
		if err != nil {
			return fmt.Errorf("sing-box 回落端口无效: %w", err)
		}
		o.SingBoxFallbackPort = port
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
	o.SNI = strings.TrimSpace(o.SNI)
	manualSNI := o.SNI != ""
	if o.SNI == "" {
		var err error
		o.SNI, err = c.askDefaultCancelable("REALITY SNI（输入域名；直接回车自动测速候选）", "")
		if err != nil {
			return err
		}
		o.SNI = strings.TrimSpace(o.SNI)
		manualSNI = o.SNI != ""
		if o.SNI == "" {
			selected, err := c.selectSNICandidate(ctx, o.Server)
			if err != nil {
				return fmt.Errorf("自动选择 SNI 失败: %w", err)
			}
			o.SNI = selected
			if c.interactiveUI() {
				c.clearScreen()
				c.printPageHeader(core, "生成服务端配置")
				fmt.Fprintf(c.out, "已选择 REALITY SNI：%s\n", o.SNI)
			}
		}
	}
	if manualSNI {
		if err := c.confirmManualSNI(ctx, o.SNI, o.Server); err != nil {
			return err
		}
	}
	if o.Target == "" {
		o.Target = netJoinHostPort(o.SNI, "443")
	}
	c.printConfirmationPanel(
		"配置确认：REALITY 回落目标",
		[]string{"SNI：" + o.SNI, "Target：" + o.Target},
		confirmationSection{title: "请确认", items: []string{
			"目标站点归属可信",
			"允许将其用作 REALITY 回落站点",
		}},
	)
	ok, err := c.confirmCancelableDefaultYes("确认 SNI 和 REALITY target？")
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("用户取消配置生成")
	}
	return nil
}

func (c *commandSet) confirmServerConfigOverwrite(core string, interactive bool) error {
	exists, err := c.app.ServerConfigExists(core)
	if err != nil {
		return err
	}
	if !exists {
		return nil
	}
	if !interactive {
		if c.yes {
			return nil
		}
		return fmt.Errorf("检测到已存在的 %s 服务端配置；非交互覆盖必须显式提供 --yes", core)
	}
	c.printConfirmationPanel(
		"操作确认：覆盖服务端配置",
		[]string{"目标内核：" + core},
		confirmationSection{title: "检测结果", items: []string{
			"检测到已存在的服务端配置",
		}},
		confirmationSection{title: "继续后", items: []string{
			"先备份当前配置文件",
			"使用 ProxyForge 标准模板完整覆盖",
			"原有自定义内容不会合并",
		}},
	)
	ok, err := c.confirmCancelable("覆盖现有服务端配置？")
	if err != nil {
		return err
	}
	if !ok {
		return errReturnToMenu
	}
	return nil
}

func (c *commandSet) selectPublicAddress(ctx context.Context) (string, error) {
	physicalIPs := c.physicalIPs
	if physicalIPs == nil {
		physicalIPs = app.PhysicalInterfaceAddresses
	}
	externalIP := c.externalIP
	if externalIP == nil {
		externalIP = app.PublicAddress
	}
	for {
		fmt.Fprintln(c.out, "公网地址获取方式")
		c.printMenuChoice("1", "从物理网卡获取（默认）")
		c.printMenuChoice("2", "通过 api.ipify.org HTTPS 探测")
		c.printMenuChoice("3", "手动输入")
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
			return address, nil
		}
		fmt.Fprintf(c.out, "获取公网地址失败：%v，请重新选择。\n\n", err)
	}
}

func (c *commandSet) selectPhysicalPublicAddress(addresses []app.PublicInterfaceAddress) (string, error) {
	if len(addresses) == 0 {
		return "", fmt.Errorf("物理网卡没有可用公网地址")
	}
	public := make([]app.PublicInterfaceAddress, 0, len(addresses))
	private := make([]app.PublicInterfaceAddress, 0, len(addresses))
	for _, item := range addresses {
		if item.Private {
			private = append(private, item)
		} else {
			public = append(public, item)
		}
	}
	ordered := append(public, private...)
	fmt.Fprintln(c.out, "检测到多个物理网卡地址（公网地址优先）：")
	for index, item := range ordered {
		family := "IPv4"
		if strings.Contains(item.Address, ":") {
			family = "IPv6"
		}
		scope := "公网"
		if item.Private {
			scope = "内网"
		}
		c.printMenuChoice(strconv.Itoa(index+1), fmt.Sprintf("%s  %s（%s） %s", item.Interface, item.Address, family, scope))
	}
	if len(ordered) == 1 {
		return ordered[0].Address, nil
	}
	choice, err := c.chooseNumberCancelable("请选择网卡地址", 1, len(ordered), 1)
	if err != nil {
		return "", err
	}
	return ordered[choice-1].Address, nil
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
	fmt.Fprintf(w, "  [结果] %s 服务端配置生成成功\n", n.Core)
	fmt.Fprintln(w, border)
	fmt.Fprintf(w, "服务状态：active（运行中）\n")
	fmt.Fprintf(w, "开机启动：enabled（已启用）\n")
	fmt.Fprintf(w, "连接地址：%s\n", netJoinHostPort(n.Server, strconv.Itoa(n.Port)))
	fmt.Fprintf(w, "REALITY SNI：%s\n", n.SNI)
	fmt.Fprintf(w, "REALITY target：%s\n", n.Target)
	fmt.Fprintf(w, "用户名称：%s\n", n.UserName)
	fmt.Fprintf(w, "入站标签：%s\n", n.InboundTag)
	if n.Core == domain.CoreSingBox {
		mode := "标准安全配置"
		if n.SingBoxFallbackGuard {
			httpPolicy := "HTTP Host 不限制"
			if n.SingBoxFallbackHTTPDomain {
				httpPolicy = "HTTP Host 仅限 SNI"
			}
			mode = fmt.Sprintf("回落防偷跑配置（direct 127.0.0.1:%d；%s）", n.SingBoxFallbackPort, httpPolicy)
		} else if n.SimplifiedConfig {
			mode = "简化配置（系统 DNS）"
		}
		fmt.Fprintf(w, "配置模式：%s\n", mode)
	} else if n.Core == domain.CoreXray {
		mode := "标准配置"
		if n.XrayFallbackGuard {
			mode = fmt.Sprintf("回落防偷跑配置（dokodemo-door 127.0.0.1:%d）", n.XrayFallbackPort)
		}
		fmt.Fprintf(w, "配置模式：%s\n", mode)
	}
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
		choice, err := c.chooseNumber("请选择", 0, 5, 0)
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
			shouldPause = false
			err = c.serviceMenu(ctx, core)
		case 5:
			var confirmed bool
			confirmed, err = c.confirmUninstall(core)
			if err == nil && confirmed {
				err = c.app.Uninstall(ctx, core, install.Options{Confirm: c.confirm})
			} else if err == nil {
				fmt.Fprintln(c.out, "已取消卸载。")
			}
		}
		if errors.Is(err, errReturnToMenu) {
			err = nil
			if choice == 1 {
				fmt.Fprintln(c.out, "已取消安装/升级。")
			} else if choice == 5 {
				fmt.Fprintln(c.out, "已取消卸载。")
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
		c.printPageHeader(core, "服务端配置管理")
		c.printMenuChoice("1", "生成/更新服务端配置（完整覆盖现有配置，不合并原配置）")
		c.printMenuChoice("2", "查看当前配置")
		c.printMenuChoice("3", "DNS 设置")
		c.printMenuChoice("4", "仅重置 SNI/target")
		c.printMenuChoice("5", "仅重置 UUID、REALITY 密钥和 short ID")
		c.printMenuChoice("0/q", "返回内核菜单")
		choice, err := c.chooseNumber("请选择", 0, 5, 0)
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
			err = c.app.CheckCoreInstalled(ctx, core)
			if err == nil {
				err = c.confirmServerConfigOverwrite(core, true)
			}
			if errors.Is(err, errReturnToMenu) {
				fmt.Fprintln(c.out, "已取消生成服务端配置，返回服务端配置管理菜单。")
				continue
			}
			if err == nil {
				err = c.fillGenerate(ctx, core, &o)
			}
			if err == nil {
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
				c.printPageHeader(core, "查看服务端配置")
				fmt.Fprintln(c.out, "警告：当前服务端配置可能包含 UUID、REALITY 私钥等敏感信息，请勿泄露。")
				fmt.Fprintln(c.out, "----------------------------------------")
				_, err = c.out.Write(b)
				if err == nil && len(b) > 0 && b[len(b)-1] != '\n' {
					fmt.Fprintln(c.out)
				}
			}
		case 3:
			err = c.dnsSettingsMenu(ctx, core)
			if errors.Is(err, errReturnToMenu) {
				continue
			}
		case 4:
			err = c.resetChoice(ctx, core, 1)
		case 5:
			err = c.resetChoice(ctx, core, 2)
		}
		if err != nil {
			c.printMenuError(err)
		}
		c.pauseForMenu()
	}
}

func (c *commandSet) dnsSettingsMenu(ctx context.Context, core string) error {
	settings, err := c.app.DNSSettings(ctx, core)
	if err != nil {
		return err
	}
	c.clearScreen()
	c.printPageHeader(core, "DNS 设置")
	fmt.Fprintf(c.out, "当前配置：%s\n\n", dnsProfileDisplay(core, settings.Current))
	defaultChoice := 1
	for index, profile := range settings.Profiles {
		c.printMenuChoice(strconv.Itoa(index+1), dnsProfileDisplay(core, profile))
		if profile == settings.Current ||
			(profile == provider.DNSProfilePublicCloudflare && settings.Current == provider.DNSProfileCloudflare) ||
			(profile == provider.DNSProfilePublicGoogle && settings.Current == provider.DNSProfileGoogle) {
			defaultChoice = index + 1
		}
	}
	c.printMenuChoice("0/q", "返回服务端配置管理")
	choice, err := c.chooseNumber("请选择 DNS", 0, len(settings.Profiles), defaultChoice)
	if err != nil {
		return err
	}
	if choice == 0 {
		return errReturnToMenu
	}
	selected := settings.Profiles[choice-1]
	if selected == settings.Current {
		fmt.Fprintf(c.out, "[提示] DNS 已经是 %s，无需修改。\n", dnsProfileDisplay(core, selected))
		return nil
	}

	sections := []confirmationSection{{title: "将执行", items: []string{
		"按 " + core + " 原生格式更新 DNS 配置",
		"保留私网和保留地址拦截",
		"使用内核原生命令校验候选配置",
		"备份并原子更新当前服务端配置",
		"服务正在运行时自动重启使设置立即生效",
	}}}
	if isEncryptedDNSProfile(selected) {
		items := []string{
			"普通域名查询将通过 HTTPS/443 加密传输",
			"该设置只修改代理内核解析器，不修改系统全局 DNS",
		}
		if core == domain.CoreSingBox {
			items = append(items,
				"sing-box 仅使用系统 DNS 引导解析 DoH 服务地址，普通域名查询不会使用系统 DNS",
				"sing-box 只写入所选的一个 DoH 上游，不配置无效的备用服务器",
			)
		} else {
			items = append(items, "Xray 使用 IP 形式的 DoH 地址，不依赖系统 DNS 引导，并按配置顺序自动回退")
		}
		sections = append(sections, confirmationSection{title: "加密 DNS", items: items})
	} else if selected != provider.DNSProfileSystem {
		items := []string{
			"公共 DNS 使用 UDP/53 明文查询，网络运营方可能观察或拦截",
			"该设置只修改代理内核解析器，不修改系统全局 DNS",
		}
		if core == domain.CoreSingBox {
			items = append(items, "sing-box 只写入所选的一个公共 DNS，不配置无效的备用服务器")
		}
		sections = append(sections, confirmationSection{title: "注意", items: items})
	}
	c.printConfirmationPanel(
		"操作确认：设置 DNS",
		[]string{
			"目标内核：" + core,
			"当前配置：" + dnsProfileDisplay(core, settings.Current),
			"新的配置：" + dnsProfileDisplay(core, selected),
		},
		sections...,
	)
	confirmed, err := c.confirmCancelable("应用新的 DNS 设置？")
	if err != nil {
		return err
	}
	if !confirmed {
		fmt.Fprintln(c.out, "已取消 DNS 修改。")
		return nil
	}
	change, err := c.app.SetDNSProfile(ctx, core, selected)
	if err != nil {
		return err
	}
	if !change.Changed {
		fmt.Fprintf(c.out, "[提示] DNS 已经是 %s，无需修改。\n", dnsProfileDisplay(core, change.Current))
		return nil
	}
	effect := "服务当前未运行，将在下次启动时生效"
	if change.Restarted {
		effect = "服务已重启，设置已生效"
	}
	fmt.Fprintf(c.out, "[结果] %s DNS 已从 %s 修改为 %s；%s。\n",
		core, dnsProfileDisplay(core, change.Previous), dnsProfileDisplay(core, change.Current), effect)
	return nil
}

func dnsProfileDisplay(core, profile string) string {
	switch profile {
	case provider.DNSProfileSystem:
		return "系统 DNS（推荐）"
	case provider.DNSProfilePublicCloudflare:
		if core == domain.CoreSingBox {
			return "公共 DNS（Cloudflare 1.1.1.1）"
		}
		return "公共 DNS（Cloudflare 默认；同时写入 1.1.1.1 + 8.8.8.8）"
	case provider.DNSProfilePublicGoogle:
		if core == domain.CoreSingBox {
			return "公共 DNS（Google 8.8.8.8）"
		}
		return "公共 DNS（Google 默认；同时写入 8.8.8.8 + 1.1.1.1）"
	case provider.DNSProfileDoHCloudflare:
		if core == domain.CoreSingBox {
			return "加密 DNS/DoH（Cloudflare）"
		}
		return "加密 DNS/DoH（Cloudflare 默认；同时配置 Google）"
	case provider.DNSProfileDoHGoogle:
		if core == domain.CoreSingBox {
			return "加密 DNS/DoH（Google）"
		}
		return "加密 DNS/DoH（Google 默认；同时配置 Cloudflare）"
	case provider.DNSProfileCloudflare:
		return "Cloudflare DNS（仅 1.1.1.1，旧单地址配置）"
	case provider.DNSProfileGoogle:
		return "Google DNS（仅 8.8.8.8，旧单地址配置）"
	case "none":
		return "未配置内核 DNS（简化模式）"
	case "implicit-system":
		return "隐式系统 DNS（尚未写入配置）"
	case "custom":
		return "自定义 DNS"
	default:
		return profile
	}
}

func isEncryptedDNSProfile(profile string) bool {
	return profile == provider.DNSProfileDoHCloudflare || profile == provider.DNSProfileDoHGoogle
}

func (c *commandSet) clientMenu(ctx context.Context, core string) (bool, error) {
	c.clearScreen()
	c.printPageHeader(core, "查看客户端配置")
	c.printMenuChoice("1", "内核原生 JSON")
	c.printMenuChoice("2", "Clash YAML（Mihomo/Clash Meta）")
	c.printMenuChoice("0/q", "返回内核菜单")
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
	c.printPageHeader(core)
	c.printMenuChoice("1", "安装/升级内核")
	c.printMenuChoice("2", "服务端配置管理")
	c.printMenuChoice("3", "查看订阅配置")
	c.printMenuChoice("4", "管理服务")
	c.printMenuChoice("5", "卸载内核并清理数据")
	c.printMenuChoice("0/q", "返回内核选择")
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

func (c *commandSet) resetMenu(ctx context.Context, core string) (bool, error) {
	c.clearScreen()
	c.printPageHeader(core, "重置节点")
	c.printMenuChoice("1", "仅重置 SNI/target（保留 UUID、REALITY 密钥和 short ID）")
	c.printMenuChoice("2", "重置 UUID、REALITY 密钥和 short ID（保留 SNI/target）")
	c.printMenuChoice("0/q", "返回内核菜单")
	choice, err := c.chooseNumber("请选择", 0, 2, 1)
	if err != nil {
		return false, err
	}
	if choice == 0 {
		return false, nil
	}
	return true, c.resetChoice(ctx, core, choice)
}

func (c *commandSet) resetChoice(ctx context.Context, core string, choice int) error {
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
		opts.Credentials = false
	} else {
		c.clearScreen()
		c.printPageHeader(core, "重置凭证")
		confirmed, err := c.confirmCredentialOnlyReset(core)
		if err != nil {
			return err
		}
		if !confirmed {
			fmt.Fprintln(c.out, "已取消凭证重置。")
			return nil
		}
		opts.Credentials = true
	}
	return c.runCredentialReset(ctx, core, opts)
}

func (c *commandSet) fillReset(ctx context.Context, core string, opts *domain.ResetOptions) error {
	current, err := c.app.Store.Load(core)
	if err != nil {
		return err
	}
	c.clearScreen()
	c.printPageHeader(core, "重置节点")
	opts.SNI = strings.TrimSpace(opts.SNI)
	manualSNI := opts.SNI != ""
	if opts.SNI == "" {
		opts.SNI = c.askDefault("新的 REALITY SNI（输入域名；直接回车自动测速候选）", "")
		opts.SNI = strings.TrimSpace(opts.SNI)
		manualSNI = opts.SNI != ""
		if opts.SNI == "" {
			selected, err := c.selectSNICandidate(ctx, current.Server)
			if err != nil {
				return fmt.Errorf("自动选择 SNI 失败: %w", err)
			}
			opts.SNI = selected
			if c.interactiveUI() {
				c.clearScreen()
				c.printPageHeader(core, "重置节点")
				fmt.Fprintf(c.out, "已选择 REALITY SNI：%s\n", opts.SNI)
			}
		}
	}
	if manualSNI {
		if err := c.confirmManualSNI(ctx, opts.SNI, current.Server); err != nil {
			return err
		}
	}
	if opts.Target == "" {
		defaultTarget := netJoinHostPort(opts.SNI, "443")
		opts.Target = c.askDefault("新的 REALITY target", defaultTarget)
	}
	return nil
}

func (c *commandSet) confirmCredentialReset(core string, opts domain.ResetOptions) (bool, error) {
	c.printConfirmationPanel(
		"危险操作确认：重置节点",
		[]string{"目标内核：" + core, "新 SNI：" + opts.SNI, "新 Target：" + opts.Target},
		confirmationSection{title: "将更新", items: []string{
			"受管入站的 SNI 和 target",
		}},
		confirmationSection{title: "将保留", items: []string{
			"DNS、路由、出站、日志、其他用户等手动配置会保留",
			"修改前会备份当前配置",
		}},
		confirmationSection{title: "重要影响", items: []string{
			"所有旧客户端配置中的 SNI/target 会立即失效；UUID、密钥和 short ID 保持不变",
		}},
	)
	return c.confirm("重置 " + core + " 节点？")
}

func (c *commandSet) confirmCredentialOnlyReset(core string) (bool, error) {
	current, err := c.app.Store.Load(core)
	if err != nil {
		return false, err
	}
	c.printConfirmationPanel(
		"危险操作确认：重置凭证",
		[]string{"目标内核：" + core, "SNI 和 target 保持不变：" + current.SNI + "，" + current.Target},
		confirmationSection{title: "将更新", items: []string{
			"UUID、REALITY 密钥和 short ID",
		}},
		confirmationSection{title: "将保留", items: []string{
			"DNS、路由、出站、日志、其他用户等手动配置会保留",
			"修改前会备份当前配置",
		}},
		confirmationSection{title: "重要影响", items: []string{
			"所有旧客户端配置会立即失效",
		}},
	)
	return c.confirm("重置 " + core + " 凭证？")
}

func (c *commandSet) runCredentialReset(ctx context.Context, core string, opts domain.ResetOptions) error {
	n, err := c.app.ResetCredentials(ctx, core, opts)
	if err != nil {
		return err
	}
	if opts.Credentials {
		fmt.Fprintf(c.out, "%s 节点凭据已全部重置，SNI=%s，target=%s；请重新导出并分发客户端配置。\n", core, n.SNI, n.Target)
	} else {
		fmt.Fprintf(c.out, "%s 节点 SNI/target 已重置为 %s / %s。\n", core, n.SNI, n.Target)
	}
	return nil
}

func (c *commandSet) selectCore() (string, bool, error) {
	c.clearScreen()
	c.printProxyForgeHeader()
	c.printMenuChoice("1", "sing-box 管理")
	c.printMenuChoice("2", "Xray-core 管理")
	c.printMenuChoice("0/q", "退出")
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

func (c *commandSet) printProxyForgeHeader() {
	fmt.Fprintln(c.out, "╭─ ProxyForge")
	fmt.Fprintf(c.out, "│ 双内核代理管理器  [版本 %s]\n", displayCurrentVersion(c.currentVersion))
	fmt.Fprintln(c.out, proxyForgeHeaderRule)
	fmt.Fprintln(c.out)
}

func (c *commandSet) printPageHeader(parts ...string) {
	clean := make([]string, 0, len(parts)+1)
	clean = append(clean, "ProxyForge")
	for _, part := range parts {
		if part = strings.TrimSpace(part); part != "" {
			clean = append(clean, part)
		}
	}
	fmt.Fprintf(c.out, "╭─ %s\n", strings.Join(clean, "  ›  "))
	fmt.Fprintln(c.out, proxyForgeHeaderRule)
	fmt.Fprintln(c.out)
}

func (c *commandSet) printMenuChoice(key, label string) {
	fmt.Fprintf(c.out, "  %s %s\n", key, label)
}

func displayCurrentVersion(version string) string {
	version = strings.TrimSpace(version)
	if version == "" {
		return "unknown"
	}
	return version
}

func (c *commandSet) serviceMenu(ctx context.Context, core string) error {
	actions := []string{"", "start", "stop", "restart", "status", "logs"}
	for {
		c.clearScreen()
		c.printPageHeader(core, "服务管理")
		c.printMenuChoice("1", "启动服务")
		c.printMenuChoice("2", "停止服务")
		c.printMenuChoice("3", "重启服务")
		c.printMenuChoice("4", "查看状态")
		c.printMenuChoice("5", "查看最近日志")
		c.printMenuChoice("6", "实时日志查看（Ctrl+C 返回服务管理）")
		c.printMenuChoice("7", "设置日志级别")
		c.printMenuChoice("0/q", "返回内核菜单")
		choice, chooseErr := c.chooseNumber("请选择", 0, 7, 4)
		if chooseErr != nil {
			return chooseErr
		}
		if choice == 0 {
			return nil
		}
		if choice == 6 {
			c.clearScreen()
			if followErr := c.followServiceLogs(ctx, core); followErr != nil {
				c.printMenuError(followErr)
				c.pauseForMenu()
			}
			continue
		}
		if choice == 7 {
			c.clearScreen()
			logErr := c.logLevelMenu(ctx, core)
			if errors.Is(logErr, errReturnToMenu) {
				continue
			}
			if logErr != nil {
				c.printMenuError(logErr)
			}
			c.pauseForMenu()
			continue
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

func (c *commandSet) logLevelMenu(ctx context.Context, core string) error {
	settings, err := c.app.LogLevelSettings(ctx, core)
	if err != nil {
		return err
	}
	c.clearScreen()
	c.printPageHeader(core, "设置日志级别")
	fmt.Fprintf(c.out, "当前级别：%s\n\n", logLevelDisplay(core, settings.Current))
	defaultChoice := 1
	for index, level := range settings.Levels {
		c.printMenuChoice(strconv.Itoa(index+1), logLevelDisplay(core, level))
		if level == settings.Current {
			defaultChoice = index + 1
		}
	}
	c.printMenuChoice("0/q", "返回服务管理")
	choice, err := c.chooseNumber("请选择日志级别", 0, len(settings.Levels), defaultChoice)
	if err != nil {
		return err
	}
	if choice == 0 {
		return errReturnToMenu
	}
	selected := settings.Levels[choice-1]
	if selected == settings.Current {
		fmt.Fprintf(c.out, "[提示] 日志级别已经是 %s，无需修改。\n", logLevelDisplay(core, selected))
		return nil
	}

	sections := []confirmationSection{{title: "将执行", items: []string{
		"使用内核原生命令校验候选配置",
		"备份并原子更新当前服务端配置",
		"服务正在运行时自动重启使设置立即生效",
	}}}
	if selected == "off" {
		sections = append(sections, confirmationSection{title: "注意", items: []string{
			"关闭后将无法通过 systemd journal 查看内核运行日志",
			"排查故障时需要重新开启日志",
		}})
	}
	c.printConfirmationPanel(
		"操作确认：设置日志级别",
		[]string{
			"目标内核：" + core,
			"当前级别：" + logLevelDisplay(core, settings.Current),
			"新的级别：" + logLevelDisplay(core, selected),
		},
		sections...,
	)
	confirmed, err := c.confirmCancelable("应用新的日志级别？")
	if err != nil {
		return err
	}
	if !confirmed {
		fmt.Fprintln(c.out, "已取消日志级别修改。")
		return nil
	}
	change, err := c.app.SetLogLevel(ctx, core, selected)
	if err != nil {
		return err
	}
	if !change.Changed {
		fmt.Fprintf(c.out, "[提示] 日志级别已经是 %s，无需修改。\n", logLevelDisplay(core, change.Current))
		return nil
	}
	effect := "服务当前未运行，将在下次启动时生效"
	if change.Restarted {
		effect = "服务已重启，设置已生效"
	}
	fmt.Fprintf(c.out, "[结果] %s 日志级别已从 %s 修改为 %s；%s。\n",
		core, logLevelDisplay(core, change.Previous), logLevelDisplay(core, change.Current), effect)
	return nil
}

func logLevelDisplay(core, level string) string {
	if level == "default" {
		return "未显式设置（使用内核默认值）"
	}
	descriptions := map[string]string{
		"trace":   "trace（最详细跟踪）",
		"debug":   "debug（调试信息）",
		"info":    "info（常规运行信息）",
		"warn":    "warn（警告及错误）",
		"warning": "warning（警告及错误）",
		"error":   "error（仅错误）",
		"fatal":   "fatal（仅致命错误）",
		"panic":   "panic（仅崩溃信息）",
		"off":     "关闭日志",
	}
	display := descriptions[level]
	if display == "" {
		return level
	}
	if (core == domain.CoreSingBox && level == "info") || (core == domain.CoreXray && level == "warning") {
		return display + "（ProxyForge 默认）"
	}
	if core == domain.CoreXray && level == "off" {
		return "关闭访问日志和错误日志"
	}
	return display
}

func (c *commandSet) followServiceLogs(ctx context.Context, core string) error {
	interruptContext := c.interruptContext
	if interruptContext == nil {
		interruptContext = func(parent context.Context) (context.Context, context.CancelFunc) {
			return signal.NotifyContext(parent, os.Interrupt)
		}
	}
	followCtx, stop := interruptContext(ctx)
	defer stop()

	c.printPageHeader(core, "实时日志")
	fmt.Fprintln(c.out, "按 Ctrl+C 返回服务管理")
	fmt.Fprintln(c.out, "----------------------------------------")
	err := c.app.FollowServiceLogs(followCtx, core, c.out)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if followCtx.Err() != nil {
		fmt.Fprintln(c.out, "\n已停止实时日志，返回服务管理菜单。")
		return nil
	}
	return err
}

func (c *commandSet) chooseNumber(label string, min, max, def int) (int, error) {
	return c.chooseNumberInput(label, min, max, def, false)
}

func (c *commandSet) chooseNumberCancelable(label string, min, max, def int) (int, error) {
	return c.chooseNumberInput(label, min, max, def, true)
}

func (c *commandSet) chooseNumberInput(label string, min, max, def int, cancelable bool) (int, error) {
	invalidShown := false
	firstPrompt := true
	prompt := "❯ 输入选项"
	if detailStart := strings.Index(label, "（"); detailStart >= 0 {
		prompt += label[detailStart:]
	}
	for {
		if firstPrompt {
			fmt.Fprintln(c.out)
			firstPrompt = false
		}
		if def >= min && def <= max {
			fmt.Fprintf(c.out, "%s [%d]: ", prompt, def)
		} else {
			fmt.Fprintf(c.out, "%s: ", prompt)
		}
		line, err := c.reader.ReadString('\n')
		if err != nil && len(line) == 0 {
			return 0, err
		}
		value := strings.TrimSpace(line)
		if cancelable && (strings.EqualFold(value, "q") || (value == "0" && min > 0)) {
			return 0, errReturnToMenu
		}
		if strings.EqualFold(value, "q") && min == 0 {
			return 0, nil
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
			fmt.Fprintf(c.out, "无效选择，请输入 %d 到 %d 之间的数字，或输入 q/0 返回主菜单。\n", min, max)
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
	fmt.Fprintf(c.errOut, "[错误] 操作失败：%v\n", err)
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
	return c.confirmInput(message, false, false)
}

func (c *commandSet) confirmCancelable(message string) (bool, error) {
	return c.confirmInput(message, true, false)

}

func (c *commandSet) confirmCancelableDefaultYes(message string) (bool, error) {
	return c.confirmInput(message, true, true)
}

func (c *commandSet) printConfirmationPanel(title string, details []string, sections ...confirmationSection) {
	c.clearScreen()
	c.printPageHeader(title)
	for _, detail := range details {
		fmt.Fprintln(c.out, detail)
	}
	for _, section := range sections {
		if section.title == "" || len(section.items) == 0 {
			continue
		}
		fmt.Fprintf(c.out, "\n%s：\n", section.title)
		for _, item := range section.items {
			fmt.Fprintf(c.out, "  - %s\n", item)
		}
	}
	fmt.Fprintln(c.out, "----------------------------------------")
}

func (c *commandSet) confirmInput(message string, cancelable, defaultYes bool) (bool, error) {
	fmt.Fprintln(c.out, strings.TrimSpace(message))
	for {
		if defaultYes {
			fmt.Fprint(c.out, "确认操作？[Y/1=确认，Q/0=返回，回车=确认]： ")
		} else {
			fmt.Fprint(c.out, "确认操作？[Y/1=确认，Q/0=返回]： ")
		}
		line, err := c.reader.ReadString('\n')
		if err != nil && len(line) == 0 {
			return false, err
		}
		value := strings.TrimSpace(line)
		if strings.EqualFold(value, "q") || value == "0" {
			return false, errReturnToMenu
		}
		if defaultYes && value == "" {
			return true, nil
		}
		if strings.EqualFold(value, "yes") || strings.EqualFold(value, "y") || value == "1" {
			return true, nil
		}
		fmt.Fprintln(c.out, "输入无效，请输入 yes/y 或 1 确认，或输入 q/0 返回当前菜单。")
	}
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
	return system.WriterInteractive(w)
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
