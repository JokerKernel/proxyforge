package cli

import (
	"errors"
	"fmt"
	"reflect"
	"strings"

	"github.com/spf13/cobra"

	"proxyforge/internal/domain"
	"proxyforge/internal/system"
)

func accessModeLabel(mode string) string {
	switch mode {
	case "", domain.AccessModeAuto:
		return "自动选择"
	case domain.AccessModeWhitelist:
		return "仅允许白名单访问"
	default:
		return "默认模式"
	}
}

func accessSwitchLabel(policy domain.AccessPolicy) string {
	if policy.Disabled {
		return "已关闭（保留模式和名单）"
	}
	return "已启用"
}

func accessEntryLabel(entry domain.AccessEntry) string {
	label := entry.Value
	if entry.Type == domain.AccessEntryDomain {
		if entry.IncludeSubdomains {
			label += "（包括子域名）"
		} else {
			label += "（精确域名）"
		}
	}
	ports := entry.Ports
	if ports == "" {
		ports = "全部"
	}
	return label + " · 端口：" + ports
}

func (c *commandSet) printAccessPolicy(policy domain.AccessPolicy) {
	c.printAccessPolicyCard(policy)
	c.printAccessEntries("白名单", policy.Whitelist)
	c.printAccessEntries("黑名单", policy.Blacklist)
}

func (c *commandSet) printAccessPolicyCard(policy domain.AccessPolicy) {
	var order []string
	if !policy.Disabled && len(policy.Whitelist) > 0 {
		order = append(order, "白名单放行")
	}
	if policy.BlacklistEnabled() && len(policy.Blacklist) > 0 {
		order = append(order, "黑名单拒绝")
	}
	order = append(order, "原有拦截")
	effectiveMode := "默认模式"
	defaultAction := "放行"
	finalAction := "其余放行"
	if !policy.Disabled && policy.EffectiveMode() == domain.AccessModeWhitelist {
		effectiveMode = "仅白名单模式"
		defaultAction = "未命中白名单则拒绝"
		if policy.Mode == domain.AccessModeWhitelist {
			defaultAction += "；白名单为空时全部拒绝"
		}
		finalAction = "其余拒绝"
	}
	order = append(order, finalAction)
	c.printLabeledCard("访问策略", [][2]string{
		{"访问控制", accessSwitchLabel(policy)},
		{"访问模式", accessModeLabel(policy.Mode)},
		{"生效模式", effectiveMode},
		{"规则顺序", strings.Join(order, " → ")},
		{"默认动作", defaultAction},
	})
}

func (c *commandSet) printAccessEntries(name string, entries []domain.AccessEntry) {
	fmt.Fprintf(c.out, "\n%s（%d 条）\n", name, len(entries))
	for i, entry := range entries {
		fmt.Fprintf(c.out, "%d. %s\n", i+1, accessEntryLabel(entry))
	}
}

func (c *commandSet) confirmAccessChange(policy domain.AccessPolicy) error {
	if c.yes {
		return nil
	}
	if !readerInteractive(c.in) {
		return fmt.Errorf("非交互修改访问策略必须显式提供 --yes")
	}
	c.printAccessPolicy(policy)
	message := "应用访问策略？白名单优先，可放行明确列出的私网目标。"
	if policy.Disabled {
		message = "保存关闭状态下的访问策略？保留模式和名单，仅执行原有拦截规则。"
	}
	ok, err := c.confirmCancelable(message)
	if err != nil {
		return err
	}
	if !ok {
		return errReturnToMenu
	}
	return nil
}

func (c *commandSet) accessCommand() *cobra.Command {
	cmd := &cobra.Command{Use: "access", Short: "管理目标域名、IP 和网段的访问白名单与黑名单"}
	cmd.AddCommand(&cobra.Command{Use: "list <sing-box|xray>", Short: "查看模式与两份名单", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		policy, err := c.app.AccessPolicy(args[0])
		if err != nil {
			return err
		}
		c.printAccessPolicy(policy)
		return nil
	}})
	cmd.AddCommand(&cobra.Command{Use: "mode <sing-box|xray> <auto|default|whitelist>", Short: "切换访问模式，保留名单", Args: cobra.ExactArgs(2), RunE: func(cmd *cobra.Command, args []string) error {
		policy, err := c.app.AccessPolicy(args[0])
		if err != nil {
			return err
		}
		previous := policy
		policy, err = policy.SelectMode(args[1])
		if err != nil {
			return err
		}
		if err = c.confirmAccessChange(policy); err != nil {
			if errors.Is(err, errReturnToMenu) {
				return nil
			}
			return err
		}
		if err = c.app.SetAccessPolicy(cmd.Context(), args[0], policy, previous); err != nil {
			return err
		}
		system.WriteDetail(c.out, "更新结果", "%s", "访问模式已更新："+accessModeLabel(policy.Mode))
		return nil
	}})
	for _, operation := range []string{"enable", "disable"} {
		cmd.AddCommand(&cobra.Command{Use: operation + " <sing-box|xray>", Short: map[string]string{"enable": "启用访问控制，应用保存的模式和名单", "disable": "关闭访问控制，保留模式和名单"}[operation], Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
			policy, err := c.app.AccessPolicy(args[0])
			if err != nil {
				return err
			}
			previous := policy
			policy.Disabled = operation == "disable"
			if err = c.confirmAccessChange(policy); err != nil {
				if errors.Is(err, errReturnToMenu) {
					return nil
				}
				return err
			}
			if err = c.app.SetAccessPolicy(cmd.Context(), args[0], policy, previous); err != nil {
				return err
			}
			system.WriteDetail(c.out, "更新结果", "%s", "访问控制"+accessSwitchLabel(policy)+"。")
			return nil
		}})
	}
	for _, operation := range []string{"add", "remove"} {
		var exact bool
		var ports string
		use := operation + " <sing-box|xray> <white|black> <域名|IP|CIDR>"
		validateArgs := cobra.ExactArgs(3)
		if operation == "add" {
			use += " [目标...]"
			validateArgs = cobra.MinimumNArgs(3)
		}
		child := &cobra.Command{Use: use, Short: map[string]string{"add": "批量添加允许或拒绝访问的目标", "remove": "删除目标规则"}[operation], Args: validateArgs, RunE: func(cmd *cobra.Command, args []string) error {
			if args[1] != "white" && args[1] != "black" {
				return fmt.Errorf("名单类型必须为 white 或 black")
			}
			var additions []domain.AccessEntry
			var removal domain.AccessEntry
			var err error
			if operation == "add" {
				additions, err = domain.ParseAccessEntries(strings.Join(args[2:], " "), exact)
			} else {
				removal, err = domain.ParseAccessEntry(args[2], exact)
			}
			if err != nil {
				return err
			}
			ports, err = domain.NormalizeAccessPorts(ports)
			if err != nil {
				return err
			}
			setPorts := func(entry *domain.AccessEntry) {
				entry.Ports = ports
				if !cmd.Flags().Changed("ports") && entry.Type == domain.AccessEntryDomain {
					entry.Ports = domain.DefaultAccessDomainPorts
				}
			}
			for i := range additions {
				setPorts(&additions[i])
			}
			setPorts(&removal)
			policy, err := c.app.AccessPolicy(args[0])
			if err != nil {
				return err
			}
			previous := policy
			entries := &policy.Whitelist
			if args[1] == "black" {
				entries = &policy.Blacklist
			}
			if operation == "add" {
				original := policy
				policy, err = policy.AddEntries(args[1], additions)
				if err != nil {
					return err
				}
				policy, err = policy.Normalize()
				if err != nil {
					return err
				}
				if reflect.DeepEqual(original, policy) {
					system.WriteDetail(c.out, "当前状态", "%s", "规则已存在，无需修改。")
					return nil
				}
			} else {
				found := -1
				for i, item := range *entries {
					if item == removal {
						found = i
						break
					}
				}
				if found < 0 {
					return fmt.Errorf("名单中不存在该规则")
				}
				copied := append([]domain.AccessEntry(nil), (*entries)...)
				*entries = append(copied[:found], copied[found+1:]...)
			}
			if err = c.confirmAccessChange(policy); err != nil {
				if errors.Is(err, errReturnToMenu) {
					return nil
				}
				return err
			}
			if err = c.app.SetAccessPolicy(cmd.Context(), args[0], policy, previous); err != nil {
				return err
			}
			system.WriteDetail(c.out, "更新结果", "%s", "访问名单已更新。")
			return nil
		}}
		child.Flags().StringVar(&ports, "ports", "", "目标端口，例如 80,443、80.443、8000-8100 或 all；域名默认 80,443，IP/CIDR 默认全部；删除时匹配原始记录")
		child.Flags().BoolVar(&exact, "exact", false, "仅匹配完整域名（默认包含域名及其子域名）")
		cmd.AddCommand(child)
	}
	cmd.AddCommand(&cobra.Command{Use: "clear <sing-box|xray> <white|black>", Short: "清空指定名单，保留模式和访问控制开关", Args: cobra.ExactArgs(2), RunE: func(cmd *cobra.Command, args []string) error {
		policy, err := c.app.AccessPolicy(args[0])
		if err != nil {
			return err
		}
		next, err := policy.ClearList(args[1])
		if err != nil {
			return err
		}
		if reflect.DeepEqual(policy, next) {
			system.WriteDetail(c.out, "当前状态", "%s", "当前名单已为空，无需修改。")
			return nil
		}
		if err = c.confirmAccessChange(next); err != nil {
			if errors.Is(err, errReturnToMenu) {
				return nil
			}
			return err
		}
		if err = c.app.SetAccessPolicy(cmd.Context(), args[0], next, policy); err != nil {
			return err
		}
		system.WriteDetail(c.out, "更新结果", "%s", "访问名单已清空。")
		return nil
	}})
	return cmd
}
