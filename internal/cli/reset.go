package cli

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"proxyforge/internal/domain"
)

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

func (c *commandSet) resetMenu(ctx context.Context, core string) (bool, error) {
	c.clearScreen()
	c.printPageHeader(core, "重置节点")
	c.printMenuChoice("1", "重置 SNI/target（保留节点凭证）")
	c.printMenuChoice("2", "重置节点凭证（UUID、REALITY 密钥和 short ID；保留 SNI/target）")
	c.printMenuChoice("0/q", "返回")
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
				if errors.Is(err, errReturnToMenu) {
					return err
				}
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
