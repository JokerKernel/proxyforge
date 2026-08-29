package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"proxyforge/internal/app"
	"proxyforge/internal/domain"
	"proxyforge/internal/install"
	"proxyforge/internal/provider"
)

const proxyForgeHeaderRule = "╰──────────────────────────────────────────────"

const menuDescriptionColumn = 18

const menuStatusColumn = 20

func (c *commandSet) menu(ctx context.Context) error {
	for {
		core, selected, err := c.selectCore(ctx)
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
		c.printCoreMenu(ctx, core)
		choice, err := c.chooseNumber("请选择", 0, 4, -1)
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
			} else if choice == 4 {
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
		c.printPageHeader(core, "服务端配置")
		c.printModifyConfigCard(ctx, core)
		c.printMenuChoice("1", "生成/更新配置（完整覆盖现有配置，不合并原配置）")
		c.printMenuChoice("2", "修改配置（DNS、出站 IP、回落 IP、重置节点与 SNI 检测）")
		c.printMenuChoice("3", "查看配置（显示当前 JSON，含敏感凭据）")
		c.printMenuChoice("4", "编辑配置（vim / nano / vi）")
		c.printMenuChoice("5", "日志级别（调整内核日志详细程度）")
		c.printMenuChoice("6", "服务管理（启动、停止、状态与日志）")
		maxChoice := 6
		if core == domain.CoreXray {
			c.printMenuChoice("7", "专用运行用户（修复 systemd 的 nobody 安全警告）")
			maxChoice = 7
		}
		c.printMenuChoice("0/q", "返回")
		choice, err := c.chooseNumber("请选择", 0, maxChoice, 0)
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
			o := domain.GenerateOptions{}
			err = c.app.CheckCoreInstalled(ctx, core)
			if err == nil {
				err = c.confirmServerConfigOverwrite(core, true)
			}
			if errors.Is(err, errReturnToMenu) {
				fmt.Fprintln(c.out, "已取消生成服务端配置，返回服务端配置菜单。")
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
				continue
			}
		case 2:
			shouldPause = false
			err = c.modifyConfigMenu(ctx, core)
		case 3:
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
		case 4:
			err = c.editServerConfig(ctx, core)
		case 5:
			err = c.logLevelMenu(ctx, core)
			if errors.Is(err, errReturnToMenu) {
				continue
			}
		case 6:
			shouldPause = false
			err = c.serviceMenu(ctx, core)
		case 7:
			err = c.dedicatedXrayServiceUser(ctx)
			if errors.Is(err, errReturnToMenu) {
				continue
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

func (c *commandSet) modifyConfigMenu(ctx context.Context, core string) error {
	for {
		c.clearScreen()
		c.printPageHeader(core, "修改配置")
		c.printModifyConfigCard(ctx, core)
		c.printMenuChoice("1", "DNS 设置（系统 DNS、明文 DNS 或 DoH）")
		c.printMenuChoice("2", "出站 IP（优先或仅使用 IPv4 / IPv6）")
		next := 3
		hasFallback := c.coreHasFallback(core)
		if hasFallback {
			c.printMenuChoice("3", "回落 IP（回落访问目标站的 IPv4 / IPv6）")
			next = 4
		}
		c.printMenuChoice(strconv.Itoa(next), "重置 SNI/target（保留节点凭证）")
		c.printMenuChoice(strconv.Itoa(next+1), "重置节点凭证（UUID、REALITY 密钥和 short ID；保留 SNI/target）")
		c.printMenuChoice(strconv.Itoa(next+2), "REALITY SNI 候选检测（重新测试，不修改配置）")
		c.printMenuChoice("0/q", "返回")
		maxChoice := next + 2
		choice, err := c.chooseNumber("请选择", 0, maxChoice, 0)
		if err != nil {
			return err
		}
		if choice == 0 {
			return nil
		}

		c.clearScreen()
		switch {
		case choice == 1:
			err = c.dnsSettingsMenu(ctx, core)
		case choice == 2:
			err = c.outboundIPMenu(ctx, core)
		case hasFallback && choice == 3:
			err = c.fallbackIPMenu(ctx, core)
		case choice == next:
			err = c.resetChoice(ctx, core, 1)
		case choice == next+1:
			err = c.resetChoice(ctx, core, 2)
		case choice == next+2:
			err = c.retestSNICandidates(ctx, core)
		}
		if errors.Is(err, errReturnToMenu) {
			continue
		}
		if err != nil {
			c.printMenuError(err)
		}
		c.pauseForMenu()
	}
}

func (c *commandSet) selectCore(ctx context.Context) (string, bool, error) {
	c.clearScreen()
	c.printProxyForgeHeader()
	c.printMenuStatusChoice("1", "Xray-core", c.coreInstalled(ctx, domain.CoreXray))
	c.printMenuStatusChoice("2", "sing-box", c.coreInstalled(ctx, domain.CoreSingBox))
	c.printMenuChoice("0/q", "退出")
	choice, err := c.chooseNumber("请选择", 0, 2, 1)
	if err != nil {
		return "", false, err
	}
	switch choice {
	case 1:
		return domain.CoreXray, true, nil
	case 2:
		return domain.CoreSingBox, true, nil
	default:
		return "", false, nil
	}
}

func (c *commandSet) coreInstalled(ctx context.Context, core string) bool {
	if c.app == nil {
		return false
	}
	return c.app.CheckCoreInstalled(ctx, core) == nil
}

func (c *commandSet) printCoreMenu(ctx context.Context, core string) {
	c.printPageHeader(core)
	c.printCoreStatusCard(ctx, core)
	c.printMenuChoice("1", "安装/升级（安装内核或升级版本）")
	c.printMenuChoice("2", "服务端配置（生成、修改、查看与服务管理）")
	c.printMenuChoice("3", "客户端配置（导出原生 JSON 或 Clash YAML）")
	c.printMenuChoice("4", "卸载内核（同时清理配置和运行数据）")
	c.printMenuChoice("0/q", "返回")
}

func (c *commandSet) printModifyConfigCard(ctx context.Context, core string) {
	var status app.ModifyConfigStatus
	if c.app != nil {
		status = c.app.ModifyConfigStatus(ctx, core)
	}
	var rows [][2]string
	serviceUser := strings.TrimSpace(status.ServiceUser)
	if serviceUser == "" {
		serviceUser = "无法读取"
	}
	rows = append(rows, [2]string{"运行用户", serviceUser})
	rows = append(rows,
		[2]string{"日志级别", modifyConfigValue(status.HasConfig, status.LogLevel, func() string { return logLevelCardDisplay(core, status.LogLevel) })},
		[2]string{"服务状态", serviceStatusCardDisplay(status)},
		[2]string{"DNS 设置", modifyConfigValue(status.HasConfig, status.DNS, func() string { return dnsCardDisplay(core, status.DNS, status.DNSServers) })},
		[2]string{"出站 IP", modifyConfigValue(status.HasConfig, status.OutboundIP, func() string { return outboundIPCardDisplay(core, status.OutboundIP) })},
	)
	if status.HasFallback {
		rows = append(rows, [2]string{
			"回落 IP", modifyConfigValue(status.HasConfig, status.FallbackIP, func() string { return outboundIPCardDisplay(core, status.FallbackIP) }),
		})
	}
	hasNode := status.HasConfig || status.SNI != ""
	rows = append(rows,
		[2]string{"SNI 防护", enabledCardDisplay(hasNode, status.HasFallback)},
		[2]string{"严格模式", enabledCardDisplay(hasNode, status.HasFallback && status.StrictMatch)},
		[2]string{"HTTP Host", httpHostCardDisplay(hasNode, status.HasFallback, status.HTTPHostRestrict)},
	)
	sni := strings.TrimSpace(status.SNI)
	if sni == "" {
		sni = "未生成"
	}
	rows = append(rows, [2]string{"端口", portCardDisplay(status)})
	rows = append(rows, [2]string{"SNI", sni})

	const labelWidth = 10
	fmt.Fprintln(c.out, "╭─ 当前配置")
	for _, row := range rows {
		padding := labelWidth - menuDisplayWidth(row[0])
		if padding < 1 {
			padding = 1
		}
		fmt.Fprintf(c.out, "│ %s%s%s\n", row[0], strings.Repeat(" ", padding), row[1])
	}
	fmt.Fprintln(c.out, proxyForgeHeaderRule)
	fmt.Fprintln(c.out)
}

func modifyConfigValue(hasConfig bool, raw string, display func() string) string {
	if raw == "" {
		if hasConfig {
			return "无法读取"
		}
		return "未生成"
	}
	return display()
}

func logLevelCardDisplay(core, level string) string {
	title, description := splitMenuChoiceLabel(logLevelDisplay(core, level))
	if description == "" {
		return title
	}
	return title + "  -- " + description
}

func enabledCardDisplay(hasNode bool, enabled bool) string {
	if !hasNode {
		return "未生成"
	}
	if enabled {
		return "已开启"
	}
	return "未开启"
}

func portCardDisplay(status app.ModifyConfigStatus) string {
	if status.Port < 1 || status.Port > 65535 {
		if status.HasConfig || status.SNI != "" {
			return "无法读取"
		}
		return "未生成"
	}
	return strconv.Itoa(status.Port)
}

func httpHostCardDisplay(hasNode, hasFallback, restrict bool) string {
	if !hasNode {
		return "未生成"
	}
	if !hasFallback {
		return "未开启"
	}
	if restrict {
		return "已开启"
	}
	return "不限制"
}

func serviceStatusCardDisplay(status app.ModifyConfigStatus) string {
	if !status.ServiceKnown {
		return "无法读取"
	}
	if status.ServiceActive {
		return "运行中"
	}
	if status.ServiceDetail == "failed" {
		return "已失败"
	}
	return "已停止"
}

func dnsCardDisplay(core, profile string, servers []string) string {
	label := dnsCardProfile(core, profile)
	if len(servers) == 0 {
		return label
	}
	return label + " · " + strings.Join(servers, ", ")
}

func dnsCardProfile(core, profile string) string {
	switch profile {
	case provider.DNSProfileSystem:
		return "系统 DNS（推荐）"
	case provider.DNSProfilePublicCloudflare, provider.DNSProfileCloudflare:
		return "Cloudflare DNS"
	case provider.DNSProfilePublicGoogle, provider.DNSProfileGoogle:
		return "Google DNS"
	case provider.DNSProfileDoHCloudflare:
		return "Cloudflare DoH"
	case provider.DNSProfileDoHGoogle:
		return "Google DoH"
	case "none":
		return "未配置内核 DNS"
	case "implicit-system":
		return "隐式系统 DNS"
	case "custom":
		return "自定义 DNS"
	default:
		return dnsProfileDisplay(core, profile)
	}
}

func outboundIPCardDisplay(core, strategy string) string {
	if strategy == provider.OutboundIPUnset {
		return outboundIPChoiceLabel(core, strategy)
	}
	return outboundIPDisplay(strategy)
}

func (c *commandSet) printCoreStatusCard(ctx context.Context, core string) {
	version := c.coreVersionLabel(ctx, core)
	installed := version != "未安装"
	name := coreDisplayName(core)
	padding := menuStatusColumn - menuDisplayWidth(name)
	if padding < 2 {
		padding = 2
	}
	status := "[未安装]"
	if installed {
		status = "[已安装]"
	}
	fmt.Fprintln(c.out, "╭─ 当前内核")
	fmt.Fprintf(c.out, "│ %s%s%s\n", name, strings.Repeat(" ", padding), status)
	if installed {
		fmt.Fprintf(c.out, "│ %s\n", version)
	}
	fmt.Fprintln(c.out, proxyForgeHeaderRule)
	fmt.Fprintln(c.out)
}

func (c *commandSet) coreVersionLabel(ctx context.Context, core string) string {
	if c.app == nil {
		return "未安装"
	}
	if version := c.app.CoreVersion(ctx, core); version != "" {
		return version
	}
	return "未安装"
}

func coreDisplayName(core string) string {
	switch core {
	case domain.CoreXray:
		return "Xray-core"
	case domain.CoreSingBox:
		return "sing-box"
	default:
		return core
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
	title, description := splitMenuChoiceLabel(label)
	var line strings.Builder
	fmt.Fprintf(&line, "  %-3s %s", key, title)
	if description != "" {
		padding := menuDescriptionColumn - menuDisplayWidth(title)
		if padding < 2 {
			padding = 2
		}
		fmt.Fprintf(&line, "%s-- %s", strings.Repeat(" ", padding), description)
	}
	fmt.Fprintln(c.out, line.String())
}

func (c *commandSet) printMenuStatusChoice(key, title string, installed bool) {
	status := "[未安装]"
	if installed {
		status = "[已安装]"
	}
	c.printMenuBadgeChoice(key, title, status)
}

func (c *commandSet) printMenuBadgeChoice(key, title, badge string) {
	padding := menuStatusColumn - menuDisplayWidth(title)
	if padding < 2 {
		padding = 2
	}
	fmt.Fprintf(c.out, "  %-3s %s%s%s\n", key, title, strings.Repeat(" ", padding), badge)
}

func menuDisplayWidth(value string) int {
	width := 0
	for _, r := range value {
		if isWideMenuRune(r) {
			width += 2
		} else {
			width++
		}
	}
	return width
}

func isWideMenuRune(r rune) bool {
	return r >= 0x1100 && (r <= 0x115f ||
		r == 0x2329 || r == 0x232a ||
		(r >= 0x2e80 && r <= 0xa4cf && r != 0x303f) ||
		(r >= 0xac00 && r <= 0xd7a3) ||
		(r >= 0xf900 && r <= 0xfaff) ||
		(r >= 0xfe10 && r <= 0xfe19) ||
		(r >= 0xfe30 && r <= 0xfe6f) ||
		(r >= 0xff00 && r <= 0xff60) ||
		(r >= 0xffe0 && r <= 0xffe6) ||
		(r >= 0x1f300 && r <= 0x1faff) ||
		(r >= 0x20000 && r <= 0x3fffd))
}

func splitMenuChoiceLabel(label string) (string, string) {
	label = strings.TrimSpace(label)
	if !strings.HasSuffix(label, "）") {
		return label, ""
	}
	start := strings.Index(label, "（")
	if start <= 0 {
		return label, ""
	}
	title := strings.TrimSpace(label[:start])
	description := strings.TrimSpace(strings.TrimSuffix(label[start+len("（"):], "）"))
	if title == "" || description == "" {
		return label, ""
	}
	return title, description
}

func displayCurrentVersion(version string) string {
	version = strings.TrimSpace(version)
	if version == "" {
		return "unknown"
	}
	return version
}
