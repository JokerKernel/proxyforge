package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"proxyforge/internal/app"
	"proxyforge/internal/domain"
)

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

func (c *commandSet) fillGenerate(ctx context.Context, core string, o *domain.GenerateOptions) error {
	c.clearScreen()
	c.printPageHeader(core, "生成服务端配置")
	fmt.Fprintln(c.out, "提示：输入 q 或 0 可取消并返回上级菜单。")
	if core == domain.CoreSingBox {
		fmt.Fprintln(c.out, "\n配置模式")
		c.printMenuChoice("1", "标准安全（内部 DNS 解析后拦截私网和保留地址）")
		c.printMenuChoice("2", "简化模式（使用系统 DNS；DNS 日志较少，私网域名可能绕过拦截）")
		c.printMenuChoice("3", "回落防护（默认；direct 入站仅放行与 SNI 一致的 TLS 流量）")
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
			c.printMenuChoice("1", "不限制 Host（默认）")
			c.printMenuChoice("2", "Host 匹配 SNI")
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
		c.printMenuChoice("1", "标准模式（REALITY 未认证流量直接转发到 target）")
		c.printMenuChoice("2", "回落防护（默认；dokodemo-door 仅放行与 SNI 一致的 TLS 流量）")
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
		existingSNI, existingTarget := currentSNIAndTarget(c.app, core)
		if existingSNI != "" {
			fmt.Fprintf(c.out, "\n当前已配置 REALITY SNI：%s\n", existingSNI)
			c.printMenuChoice("1", "使用原有 SNI（默认）")
			c.printMenuChoice("2", "重新输入或自动测速")
			choice, err := c.chooseNumberCancelable("请选择 SNI", 1, 2, 1)
			if err != nil {
				return err
			}
			if choice == 1 {
				o.SNI = existingSNI
				if o.Target == "" && existingTarget != "" {
					o.Target = existingTarget
				}
			}
		}
	}
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

func currentSNIAndTarget(a *app.App, core string) (string, string) {
	if a == nil {
		return "", ""
	}
	current, err := a.Store.Load(core)
	if err != nil {
		return "", ""
	}
	return strings.TrimSpace(current.SNI), strings.TrimSpace(current.Target)
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
		c.printMenuChoice("1", "物理网卡（默认）")
		c.printMenuChoice("2", "在线探测（api.ipify.org HTTPS）")
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
	fmt.Fprintln(w, "提示：请从“客户端配置”导出并妥善保管。")
}
