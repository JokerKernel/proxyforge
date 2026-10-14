package cli

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"proxyforge/internal/domain"
	"proxyforge/internal/system"
)

func (c *commandSet) resetCommand() *cobra.Command {
	var opts domain.ResetOptions
	cmd := &cobra.Command{
		Use: "reset <sing-box|xray>", Short: "重置节点凭据或重新生成 TLS 证书", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if !c.yes {
				if !readerInteractive(c.in) {
					return fmt.Errorf("非交互模式重置凭据必须显式提供 --yes")
				}
				if err := c.fillReset(cmd.Context(), args[0], &opts); err != nil {
					if errors.Is(err, errReturnToMenu) {
						system.WriteDetail(c.out, "取消操作", "%s", "已取消节点重置。")
						return nil
					}
					return err
				}
				confirmed, err := c.confirmCredentialReset(args[0], opts)
				if errors.Is(err, errReturnToMenu) {
					system.WriteDetail(c.out, "取消操作", "%s", "已取消节点重置。")
					return nil
				}
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
	cmd.Flags().BoolVar(&opts.RotateCertificate, "rotate-certificate", false, "重新生成 TLS 主节点证书（保留 UUID）")
	cmd.Flags().StringVar(&opts.SNI, "sni", "", "重置时使用的新 SNI（默认保留当前值）")
	cmd.Flags().StringVar(&opts.Target, "target", "", "新的 REALITY target（仅修改 SNI 时默认新 SNI:443）")
	return cmd
}

func (c *commandSet) resetChoice(ctx context.Context, core string, choice int) error {
	opts := domain.ResetOptions{}
	if current, err := c.app.Store.Load(core); err == nil && current.IsTLS() {
		opts.RotateCertificate = choice == 1
		opts.Credentials = choice != 1
		ok, err := c.confirmTLSReset(core, opts)
		if err != nil {
			return err
		}
		if !ok {
			return nil
		}
		return c.runCredentialReset(ctx, core, opts)
	}
	if choice == 1 {
		if err := c.fillReset(ctx, core, &opts); err != nil {
			return err
		}
		confirmed, err := c.confirmCredentialReset(core, opts)
		if err != nil {
			return err
		}
		if !confirmed {
			system.WriteDetail(c.out, "取消操作", "%s", "已取消节点重置。")
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
			system.WriteDetail(c.out, "取消操作", "%s", "已取消凭证重置。")
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
	if current.IsTLS() {
		if opts.SNI != "" || opts.Target != "" {
			return fmt.Errorf("自签 TLS 不支持手动修改 SNI/target")
		}
		if !opts.RotateCertificate {
			opts.Credentials = true
		}
		return nil
	}
	c.clearScreen()
	c.printPageHeader(core, "重置节点")
	opts.SNI = strings.TrimSpace(opts.SNI)
	manualSNI := opts.SNI != ""
	if opts.SNI == "" {
		opts.SNI, err = c.askDefaultCancelable("新的 REALITY SNI（输入域名；直接回车自动测速候选）", "")
		if err != nil {
			return err
		}
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
		opts.Target, err = c.askDefaultCancelable("新的 REALITY target", defaultTarget)
		if err != nil {
			return err
		}
	}
	return nil
}

func (c *commandSet) confirmCredentialReset(core string, opts domain.ResetOptions) (bool, error) {
	if c.app != nil {
		if current, err := c.app.Store.Load(core); err == nil && current.IsTLS() {
			return c.confirmTLSReset(core, opts)
		}
	}
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
	if n.IsTLS() {
		action := "UUID 已重置，证书保持不变"
		if opts.RotateCertificate {
			action = "证书已重新生成，UUID 保持不变"
		}
		system.WriteDetail(c.out, "重置结果", "%s TLS 节点%s；请重新导出客户端配置。", core, action)
		return nil
	}
	if opts.Credentials {
		system.WriteDetail(c.out, "重置结果", "%s 节点凭据已全部重置，SNI=%s，target=%s；请重新导出并分发客户端配置。", core, n.SNI, n.Target)
	} else {
		system.WriteDetail(c.out, "更新结果", "%s 节点 SNI/target 已重置为 %s / %s。", core, n.SNI, n.Target)
	}
	return nil
}

func (c *commandSet) confirmTLSReset(core string, opts domain.ResetOptions) (bool, error) {
	message := "将重置 UUID，保留 TLS 证书；旧客户端需要更新。"
	if opts.RotateCertificate {
		message = "将重新生成自签证书和 TLS 名称，保留 UUID；普通客户端及复用主入站的中转连接需要更新。"
	}
	c.printConfirmationPanel("操作确认：TLS 节点重置", []string{"目标内核：" + core, message})
	return c.confirmCancelable("确认重置？")
}
