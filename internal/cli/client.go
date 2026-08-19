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
	c.printMenuChoice("1", "原生 JSON")
	c.printMenuChoice("2", "Clash YAML（Mihomo/Clash Meta）")
	c.printMenuChoice("0/q", "返回")
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
