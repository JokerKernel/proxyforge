package cli

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"

	"proxyforge/internal/app"
)

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

func (c *commandSet) clientMenu(ctx context.Context, core string) (bool, error) {
	c.clearScreen()
	c.printPageHeader(core, "客户端配置")
	c.printMenuChoice("1", "普通节点 · 原生 JSON（sing-box / Xray 客户端）")
	c.printMenuChoice("2", "普通节点 · Clash YAML（Mihomo/Clash Meta）")
	c.printMenuChoice("3", "中转节点客户端配置（选择线路与格式）")
	c.printMenuChoice("0/q", "返回")
	choice, err := c.chooseNumber("请选择", 0, 3, 1)
	if err != nil {
		return false, err
	}
	if choice == 0 {
		return false, nil
	}
	if choice == 3 {
		return c.relayClientMenu(ctx, core)
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

func (c *commandSet) relayClientMenu(ctx context.Context, core string) (bool, error) {
	links, err := c.app.RelayLinks(core)
	if err != nil {
		return true, err
	}
	c.clearScreen()
	c.printPageHeader(core, "客户端配置", "中转节点")
	if len(links) == 0 {
		fmt.Fprintln(c.out, "尚未配置中转线路，请先从服务端配置中添加中转线路。")
		return true, nil
	}
	for i, link := range links {
		c.printMenuBadgeChoice(fmt.Sprintf("%d", i+1), link.Name+" · "+link.UserName, "["+enabledLabel(link.Enabled)+"]")
	}
	c.printMenuChoice("0/q", "返回")
	choice, err := c.chooseNumber("请选择中转线路", 0, len(links), 1)
	if err != nil || choice == 0 {
		return false, err
	}
	selected := links[choice-1]

	c.clearScreen()
	c.printPageHeader(core, "客户端配置", "中转节点", selected.Name)
	c.printMenuChoice("1", "原生 JSON（sing-box / Xray 客户端）")
	c.printMenuChoice("2", "Clash YAML（Mihomo/Clash Meta）")
	c.printMenuChoice("0/q", "返回")
	formatChoice, err := c.chooseNumber("请选择客户端格式", 0, 2, 1)
	if err != nil || formatChoice == 0 {
		return false, err
	}
	format := app.ClientFormatNative
	formatLabel := "原生 JSON"
	if formatChoice == 2 {
		format = app.ClientFormatClash
		formatLabel = "Clash YAML"
	}
	b, err := c.app.RelayClientConfig(ctx, core, selected.Name, format, "", false)
	if err != nil {
		return true, err
	}
	fmt.Fprintf(c.out, "\n中转节点 %s 的 %s 配置：\n\n", selected.Name, formatLabel)
	_, err = c.out.Write(b)
	return true, err
}
