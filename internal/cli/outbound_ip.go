package cli

import (
	"context"
	"fmt"
	"strconv"

	"proxyforge/internal/domain"
	"proxyforge/internal/provider"
)

func (c *commandSet) outboundIPMenu(ctx context.Context, core string) error {
	settings, err := c.app.OutboundIPSettings(ctx, core)
	if err != nil {
		return err
	}
	c.clearScreen()
	c.printPageHeader(core, "出站 IP")
	fmt.Fprintf(c.out, "当前配置：%s\n\n", outboundIPDisplay(settings.Current))
	defaultChoice := -1
	for index, strategy := range settings.Strategies {
		c.printMenuChoice(strconv.Itoa(index+1), outboundIPChoiceDisplay(strategy))
		if strategy == settings.Current {
			defaultChoice = index + 1
		}
	}
	c.printMenuChoice("0/q", "返回")
	choice, err := c.chooseNumber("请选择出站 IP", 0, len(settings.Strategies), defaultChoice)
	if err != nil {
		return err
	}
	if choice == 0 {
		return errReturnToMenu
	}
	selected := settings.Strategies[choice-1]
	if selected == settings.Current {
		fmt.Fprintf(c.out, "[提示] 出站 IP 已经是 %s，无需修改。\n", outboundIPDisplay(selected))
		return nil
	}

	sections := []confirmationSection{{title: "将执行", items: []string{
		"只修改 tag=direct 的出站解析策略",
		"不修改 DNS 服务器列表和系统全局 DNS",
		"使用内核原生命令校验候选配置",
		"备份并原子更新当前服务端配置",
		"服务正在运行时自动重启使设置立即生效",
	}}}
	switch selected {
	case provider.OutboundIPPreferIPv4, provider.OutboundIPPreferIPv6:
		items := []string{
			"优先解析所选地址族；解析不到时再尝试另一族",
		}
		if core == domain.CoreXray {
			items = append(items, "Xray 在已经解析出优先地址族后，连接失败不会改走另一族")
		} else {
			items = append(items, "sing-box 会在短延迟后尝试另一族地址（连接回退）")
		}
		sections = append(sections, confirmationSection{title: "优先策略", items: items})
	case provider.OutboundIPUnset:
		sections = append(sections, confirmationSection{title: "恢复默认", items: []string{
			"恢复为生成配置时的双栈行为，不偏科某一地址族",
		}})
	default:
		items := []string{"对端只有另一地址族时将无法访问"}
		if core == domain.CoreXray {
			items = append(items, "Xray Freedom 的 UDP 出站默认仍偏向 IPv4")
		}
		sections = append(sections, confirmationSection{title: "仅单族", items: items})
	}
	c.printConfirmationPanel(
		"操作确认：设置出站 IP",
		[]string{
			"目标内核：" + core,
			"当前配置：" + outboundIPDisplay(settings.Current),
			"新的配置：" + outboundIPDisplay(selected),
		},
		sections...,
	)
	confirmed, err := c.confirmCancelable("应用新的出站 IP 设置？")
	if err != nil {
		return err
	}
	if !confirmed {
		fmt.Fprintln(c.out, "已取消出站 IP 修改。")
		return nil
	}
	change, err := c.app.SetOutboundIPStrategy(ctx, core, selected)
	if err != nil {
		return err
	}
	if !change.Changed {
		fmt.Fprintf(c.out, "[提示] 出站 IP 已经是 %s，无需修改。\n", outboundIPDisplay(change.Current))
		return nil
	}
	effect := "服务当前未运行，将在下次启动时生效"
	if change.Restarted {
		effect = "服务已重启，设置已生效"
	}
	fmt.Fprintf(c.out, "[结果] %s 出站 IP 已从 %s 修改为 %s；%s。\n",
		core, outboundIPDisplay(change.Previous), outboundIPDisplay(change.Current), effect)
	return nil
}

func outboundIPChoiceDisplay(strategy string) string {
	if strategy == provider.OutboundIPUnset {
		return "恢复默认（双栈）"
	}
	return outboundIPDisplay(strategy)
}

func outboundIPDisplay(strategy string) string {
	switch strategy {
	case provider.OutboundIPPreferIPv4:
		return "优先 IPv4"
	case provider.OutboundIPPreferIPv6:
		return "优先 IPv6"
	case provider.OutboundIPIPv4Only:
		return "仅 IPv4"
	case provider.OutboundIPIPv6Only:
		return "仅 IPv6"
	case provider.OutboundIPUnset:
		return "未设置（双栈）"
	case "custom":
		return "自定义出站 IP 策略"
	default:
		return strategy
	}
}
