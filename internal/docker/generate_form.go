package docker

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"strings"

	"proxyforge/internal/docker/model"
)

func (m *Manager) askGenerateUI(ctx context.Context, u *ui, opts model.GenerateOptions) (model.GenerateOptions, error) {
	current, _ := m.LoadState()
	n := current.Node
	opened := false
	open := func() {
		if opened {
			return
		}
		opened = true
		u.clearScreen()
		u.pageHeader("Xray（Docker）", "生成服务端配置")
		fmt.Fprintln(u.out, "提示：输入 q 或 0 可取消并返回上级菜单。")
	}
	if !opts.DropLinks && (len(n.RelayLinks) > 0 || len(n.LandingAccesses) > 0) {
		open()
		fmt.Fprintf(u.out, "\n检测到 %d 条中转线路和 %d 个落地接入。\n", len(n.RelayLinks), len(n.LandingAccesses))
		u.menuChoice("1", "保留并重新应用这些线路（默认）")
		u.menuChoice("2", "清除全部中转线路和落地接入")
		choice, err := u.chooseNumberCancelable("请选择线路处理方式", 1, 2, 1)
		if err != nil {
			return opts, err
		}
		opts.DropLinks = choice == 2
	}
	if opts.Security == "" {
		open()
		defaultChoice := 1
		if n.IsTLS() {
			defaultChoice = 2
		}
		fmt.Fprintln(u.out, "\n主节点协议")
		u.menuChoice("1", "VLESS + RAW + REALITY + Vision")
		u.menuChoice("2", "VLESS + RAW + TLS + Vision（自动生成自签证书）")
		choice, err := u.chooseNumberCancelable("请选择主节点协议", 1, 2, defaultChoice)
		if err != nil {
			return opts, err
		}
		opts.Security = model.LandingSecurityReality
		if choice == 2 {
			opts.Security = model.LandingSecurityTLS
		}
		if n.Server != "" && model.NormalizeLandingSecurity(n.Security) != model.NormalizeLandingSecurity(opts.Security) {
			fmt.Fprintln(u.out, "[提示] 切换协议将自动更换主节点 UUID，需要重新导出客户端配置。")
		}
	}
	tlsMode := model.NormalizeLandingSecurity(opts.Security) == model.LandingSecurityTLS
	if tlsMode {
		opts.StandardConfig = false
		opts.XrayFallbackGuard = false
		opts.XrayFallbackPort = 0
		opts.XrayFallbackHTTPDomain = false
		opts.XrayFallbackExactDomain = false
		opts.ApplyFallbackPolicy = false
	} else {
		open()
		fmt.Fprintln(u.out, "\n配置模式")
		u.menuChoice("1", "回落防护（默认；tunnel 仅放行与 SNI 一致的 TLS 流量）")
		u.menuChoice("2", "标准模式（REALITY 未认证流量直接转发到 target）")
		defaultChoice := 1
		if opts.StandardConfig {
			defaultChoice = 2
		}
		choice, err := u.chooseNumberCancelable("请选择配置模式", 1, 2, defaultChoice)
		if err != nil {
			return opts, err
		}
		opts.XrayFallbackGuard = choice == 1
		opts.StandardConfig = choice == 2
		opts.ApplyFallbackPolicy = true
		if opts.XrayFallbackGuard {
			fmt.Fprintln(u.out, "\nHTTP 回落域名限制")
			u.menuChoice("1", "不限制 Host（默认）")
			u.menuChoice("2", "限制 Host（明文 HTTP 仅放行当前 SNI）")
			defaultHTTP := 1
			if opts.XrayFallbackHTTPDomain {
				defaultHTTP = 2
			}
			choice, err = u.chooseNumberCancelable("请选择 HTTP 回落策略", 1, 2, defaultHTTP)
			if err != nil {
				return opts, err
			}
			opts.XrayFallbackHTTPDomain = choice == 2

			fmt.Fprintln(u.out, "\n严格匹配回落域名")
			u.menuChoice("1", "允许子域名（默认）")
			u.menuChoice("2", "严格匹配（仅放行完整域名）")
			defaultDomain := 1
			if opts.XrayFallbackExactDomain {
				defaultDomain = 2
			}
			choice, err = u.chooseNumberCancelable("请选择回落域名匹配", 1, 2, defaultDomain)
			if err != nil {
				return opts, err
			}
			opts.XrayFallbackExactDomain = choice == 2
		} else {
			opts.XrayFallbackPort = 0
			opts.XrayFallbackHTTPDomain = false
			opts.XrayFallbackExactDomain = false
		}
	}
	var err error
	if opts.Server == "" {
		open()
		detected, detectErr := m.selectPublicAddress(ctx, u)
		if detectErr != nil {
			return opts, detectErr
		}
		opts.Server, err = u.ask("公网 IP 或域名", detected)
		if err != nil {
			return opts, err
		}
	}
	if opts.Port == 0 {
		open()
		def := n.Port
		if def == 0 {
			def = 443
		}
		value, askErr := u.ask("监听端口", strconv.Itoa(def))
		if askErr != nil {
			return opts, askErr
		}
		opts.Port, err = strconv.Atoi(value)
		if err != nil || opts.Port < 1 || opts.Port > 65535 {
			return opts, fmt.Errorf("端口无效")
		}
	}
	if opts.XrayFallbackGuard && opts.XrayFallbackPort == 0 {
		opts.XrayFallbackPort, err = m.askFallbackPort(u, n, opts.Port)
		if err != nil {
			return opts, err
		}
	}
	if opts.UserName == "" {
		def := strings.TrimSpace(n.UserName)
		if def == "" {
			def = model.DefaultUserName
		}
		opts.UserName, err = u.ask("用户名称", def)
		if err != nil {
			return opts, err
		}
	}
	if opts.InboundTag == "" {
		def := strings.TrimSpace(n.InboundTag)
		if def == "" {
			def = model.DefaultInboundTag(model.CoreXray)
		}
		opts.InboundTag, err = u.ask("入站标签", def)
		if err != nil {
			return opts, err
		}
	}
	if tlsMode {
		if opts.SNI != "" || opts.Target != "" {
			return opts, fmt.Errorf("自签 TLS 自动生成证书名称，无需 SNI 或 target")
		}
		u.confirmationPanel("配置确认：自签 TLS", []string{
			"连接地址：" + net.JoinHostPort(opts.Server, strconv.Itoa(opts.Port)),
			"自动生成证书和 TLS 名称；客户端使用固定指纹验证",
		})
		if err = u.confirmDefaultYes("生成并启用 TLS 配置？"); err != nil {
			return opts, err
		}
		return opts, nil
	}
	opts.SNI = strings.TrimSpace(opts.SNI)
	manualSNI := opts.SNI != ""
	if opts.SNI == "" && strings.TrimSpace(n.SNI) != "" && !n.IsTLS() {
		open()
		fmt.Fprintf(u.out, "\n当前已配置 REALITY SNI：%s\n", strings.TrimSpace(n.SNI))
		u.menuChoice("1", "使用原有 SNI（默认）")
		u.menuChoice("2", "重新输入或自动测速")
		choice, chooseErr := u.chooseNumberCancelable("请选择 SNI", 1, 2, 1)
		if chooseErr != nil {
			return opts, chooseErr
		}
		if choice == 1 {
			opts.SNI = strings.TrimSpace(n.SNI)
			if opts.Target == "" && strings.TrimSpace(n.Target) != "" {
				opts.Target = strings.TrimSpace(n.Target)
			}
		}
	}
	if opts.SNI == "" {
		open()
		value, askErr := u.ask("REALITY SNI（输入域名；直接回车自动测速候选）", "")
		if askErr != nil {
			return opts, askErr
		}
		opts.SNI = strings.TrimSpace(value)
		manualSNI = opts.SNI != ""
		if opts.SNI == "" {
			selected, probeErr := m.selectSNICandidate(ctx, u, opts.Server)
			if probeErr != nil {
				return opts, probeErr
			}
			opts.SNI = selected
			if u.interactive {
				u.clearScreen()
				u.pageHeader("Xray（Docker）", "生成服务端配置")
				fmt.Fprintf(u.out, "已选择 REALITY SNI：%s\n", opts.SNI)
			}
		}
	}
	if manualSNI {
		if err = m.confirmManualSNI(ctx, u, opts.SNI, opts.Server); err != nil {
			return opts, err
		}
	}
	if strings.TrimSpace(opts.Target) == "" {
		opts.Target = net.JoinHostPort(opts.SNI, "443")
	}
	u.confirmationPanel(
		"配置确认：REALITY 回落目标",
		[]string{"SNI：" + opts.SNI, "Target：" + opts.Target},
		confirmSection{title: "请确认", items: []string{"目标站点归属可信", "允许将其用作 REALITY 回落站点"}},
	)
	if err = u.confirmDefaultYes("确认 SNI 和 REALITY target？"); err != nil {
		return opts, err
	}
	return opts, nil
}

func (m *Manager) selectPublicAddress(ctx context.Context, u *ui) (string, error) {
	for {
		fmt.Fprintln(u.out, "公网地址获取方式")
		u.menuChoice("1", "物理网卡（默认）")
		u.menuChoice("2", "在线探测（api.ipify.org HTTPS）")
		u.menuChoice("3", "手动输入（自行填写公网地址）")
		choice, err := u.chooseNumberCancelable("请选择", 1, 3, 1)
		if err != nil {
			return "", err
		}
		if choice == 3 {
			return "", nil
		}
		var address string
		if choice == 1 {
			var addresses []publicAddress
			addresses, err = m.listPhysicalAddresses()
			if err == nil {
				address, err = u.selectPhysicalPublicAddress(addresses)
			}
		} else {
			address, err = m.discoverPublicAddress(ctx)
		}
		if err == nil {
			return address, nil
		}
		fmt.Fprintf(u.out, "获取公网地址失败：%v，请重新选择。\n\n", err)
	}
}

func (u *ui) selectPhysicalPublicAddress(addresses []publicAddress) (string, error) {
	if len(addresses) == 0 {
		return "", fmt.Errorf("物理网卡没有可用公网地址")
	}
	public := make([]publicAddress, 0, len(addresses))
	private := make([]publicAddress, 0, len(addresses))
	for _, item := range addresses {
		if item.Private {
			private = append(private, item)
		} else {
			public = append(public, item)
		}
	}
	ordered := append(public, private...)
	fmt.Fprintln(u.out, "检测到多个物理网卡地址（公网地址优先）：")
	for index, item := range ordered {
		family := "IPv4"
		if strings.Contains(item.Address, ":") {
			family = "IPv6"
		}
		scope := "公网"
		if item.Private {
			scope = "内网"
		}
		u.menuChoice(strconv.Itoa(index+1), fmt.Sprintf("%s  %s（%s） %s", item.Interface, item.Address, family, scope))
	}
	if len(ordered) == 1 {
		return ordered[0].Address, nil
	}
	choice, err := u.chooseNumberCancelable("请选择网卡地址", 1, len(ordered), 1)
	if err != nil {
		return "", err
	}
	return ordered[choice-1].Address, nil
}

func (m *Manager) askFallbackPort(u *ui, n model.Node, publicPort int) (int, error) {
	def := n.XrayFallbackPort
	if def < 1024 || def > 65535 || def == publicPort {
		var err error
		def, err = m.freeHighPort(publicPort)
		if err != nil {
			return 0, err
		}
	}
	value, err := u.ask("本机 tunnel 回落端口", strconv.Itoa(def))
	if err != nil {
		return 0, err
	}
	port, err := strconv.Atoi(value)
	if err != nil {
		return 0, fmt.Errorf("本机 tunnel 回落端口无效: %w", err)
	}
	return port, nil
}
