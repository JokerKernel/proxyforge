package cli

import (
	"context"
	"fmt"
	"strconv"

	"proxyforge/internal/domain"
	"proxyforge/internal/provider"
)

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
	c.printMenuChoice("0/q", "返回")
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
