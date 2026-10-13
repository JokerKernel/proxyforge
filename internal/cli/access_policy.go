package cli

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"github.com/spf13/cobra"
	"proxyforge/internal/domain"
)

func accessModeLabel(mode string) string {
	switch mode {
	case domain.AccessModeWhitelist:
		return "仅允许白名单访问"
	case domain.AccessModeBlacklist:
		return "黑名单模式"
	default:
		return "默认模式"
	}
}

func accessEntryLabel(entry domain.AccessEntry) string {
	if entry.Type == domain.AccessEntryDomain {
		if entry.IncludeSubdomains {
			return entry.Value + "（包括子域名）"
		}
		return entry.Value + "（精确域名）"
	}
	return entry.Value
}

func (c *commandSet) printAccessPolicy(policy domain.AccessPolicy) {
	fmt.Fprintf(c.out, "访问模式：%s\n规则顺序：白名单放行 → 黑名单拒绝 → 原有拦截 → 默认动作\n", accessModeLabel(policy.EffectiveMode()))
	if policy.EffectiveMode() == domain.AccessModeWhitelist {
		fmt.Fprintln(c.out, "默认动作：拒绝；白名单为空时全部拒绝。")
	} else {
		fmt.Fprintln(c.out, "默认动作：放行；黑名单仅在黑名单模式生效。")
	}
	for _, item := range []struct {
		name    string
		entries []domain.AccessEntry
	}{{"白名单", policy.Whitelist}, {"黑名单", policy.Blacklist}} {
		fmt.Fprintf(c.out, "\n%s（%d 条）\n", item.name, len(item.entries))
		for i, entry := range item.entries {
			fmt.Fprintf(c.out, "%d. %s\n", i+1, accessEntryLabel(entry))
		}
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
	ok, err := c.confirmCancelable("应用访问策略？白名单优先，可放行明确列出的私网目标。")
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
	cmd.AddCommand(&cobra.Command{Use: "mode <sing-box|xray> <default|whitelist|blacklist>", Short: "切换访问模式，保留名单", Args: cobra.ExactArgs(2), RunE: func(cmd *cobra.Command, args []string) error {
		policy, err := c.app.AccessPolicy(args[0])
		if err != nil {
			return err
		}
		policy.Mode = args[1]
		policy, err = policy.Normalize()
		if err != nil {
			return err
		}
		if err = c.confirmAccessChange(policy); err != nil {
			if errors.Is(err, errReturnToMenu) {
				return nil
			}
			return err
		}
		if err = c.app.SetAccessPolicy(cmd.Context(), args[0], policy); err != nil {
			return err
		}
		fmt.Fprintln(c.out, "访问模式已更新："+accessModeLabel(policy.EffectiveMode()))
		return nil
	}})
	for _, operation := range []string{"add", "remove"} {
		operation := operation
		var exact bool
		child := &cobra.Command{Use: operation + " <sing-box|xray> <white|black> <域名|IP|CIDR>", Short: map[string]string{"add": "添加允许或拒绝访问的目标", "remove": "删除目标规则"}[operation], Args: cobra.ExactArgs(3), RunE: func(cmd *cobra.Command, args []string) error {
			entry, err := domain.ParseAccessEntry(args[2], exact)
			if err != nil {
				return err
			}
			if args[1] != "white" && args[1] != "black" {
				return fmt.Errorf("名单类型必须为 white 或 black")
			}
			policy, err := c.app.AccessPolicy(args[0])
			if err != nil {
				return err
			}
			entries := &policy.Whitelist
			if args[1] == "black" {
				entries = &policy.Blacklist
			}
			found := -1
			for i, item := range *entries {
				if item == entry {
					found = i
					break
				}
			}
			if operation == "add" {
				if found >= 0 {
					fmt.Fprintln(c.out, "规则已存在，无需修改。")
					return nil
				}
				*entries = append(*entries, entry)
			} else {
				if found < 0 {
					return fmt.Errorf("名单中不存在该规则")
				}
				*entries = append((*entries)[:found], (*entries)[found+1:]...)
			}
			if err = c.confirmAccessChange(policy); err != nil {
				if errors.Is(err, errReturnToMenu) {
					return nil
				}
				return err
			}
			if err = c.app.SetAccessPolicy(cmd.Context(), args[0], policy); err != nil {
				return err
			}
			fmt.Fprintln(c.out, "访问名单已更新。")
			return nil
		}}
		child.Flags().BoolVar(&exact, "exact", false, "仅匹配完整域名（默认包含域名及其子域名）")
		cmd.AddCommand(child)
	}
	return cmd
}

func (c *commandSet) accessPolicyMenu(ctx context.Context, core string) error {
	for {
		policy, err := c.app.AccessPolicy(core)
		if err != nil {
			return err
		}
		c.clearScreen()
		c.printPageHeader(core, "访问控制")
		c.printAccessPolicy(policy)
		fmt.Fprintln(c.out)
		c.printMenuChoice("1", "切换访问模式")
		c.printMenuChoice("2", "添加白名单")
		c.printMenuChoice("3", "删除白名单")
		c.printMenuChoice("4", "添加黑名单")
		c.printMenuChoice("5", "删除黑名单")
		c.printMenuChoice("0/q", "返回")
		choice, err := c.chooseNumber("请选择", 0, 5, 0)
		if err != nil {
			return err
		}
		if choice == 0 {
			return nil
		}
		next := policy
		if choice == 1 {
			modes := []string{domain.AccessModeDefault, domain.AccessModeWhitelist, domain.AccessModeBlacklist}
			defaultChoice := 1
			for i, mode := range modes {
				label := accessModeLabel(mode)
				if mode == policy.EffectiveMode() {
					label += "（当前）"
					defaultChoice = i + 1
				}
				c.printMenuChoice(strconv.Itoa(i+1), label)
			}
			selected, e := c.chooseNumberCancelable("请选择访问模式", 1, 3, defaultChoice)
			if errors.Is(e, errReturnToMenu) {
				continue
			}
			if e != nil {
				return e
			}
			next.Mode = modes[selected-1]
		} else {
			entries := &next.Whitelist
			if choice >= 4 {
				entries = &next.Blacklist
			}
			if choice == 2 || choice == 4 {
				value, e := c.askDefaultCancelable("域名、IP 或 CIDR 网段", "")
				if errors.Is(e, errReturnToMenu) {
					continue
				}
				if e != nil {
					return e
				}
				entry, e := domain.ParseAccessEntry(value, false)
				if e != nil {
					c.printMenuError(e)
					c.pauseForMenu()
					continue
				}
				if entry.Type == domain.AccessEntryDomain {
					c.printMenuChoice("1", "域名及其全部子域名（默认）")
					c.printMenuChoice("2", "仅完整域名")
					selected, e := c.chooseNumberCancelable("请选择域名匹配方式", 1, 2, 1)
					if errors.Is(e, errReturnToMenu) {
						continue
					}
					if e != nil {
						return e
					}
					entry.IncludeSubdomains = selected == 1
				}
				*entries = append(*entries, entry)
			} else {
				if len(*entries) == 0 {
					fmt.Fprintln(c.out, "当前名单为空。")
					c.pauseForMenu()
					continue
				}
				for i, entry := range *entries {
					c.printMenuChoice(strconv.Itoa(i+1), accessEntryLabel(entry))
				}
				selected, e := c.chooseNumberCancelable("请选择要删除的规则", 1, len(*entries), 1)
				if errors.Is(e, errReturnToMenu) {
					continue
				}
				if e != nil {
					return e
				}
				copied := append([]domain.AccessEntry(nil), (*entries)...)
				*entries = append(copied[:selected-1], copied[selected:]...)
			}
		}
		next, err = next.Normalize()
		if err != nil {
			c.printMenuError(err)
			c.pauseForMenu()
			continue
		}
		c.printAccessPolicy(next)
		ok, err := c.confirmCancelable("应用访问策略？白名单优先，明确允许的私网目标也会放行。")
		if errors.Is(err, errReturnToMenu) {
			continue
		}
		if err != nil {
			return err
		}
		if ok {
			if err := c.app.SetAccessPolicy(ctx, core, next); err != nil {
				c.printMenuError(err)
			} else {
				fmt.Fprintln(c.out, "访问策略已生效。")
			}
			c.pauseForMenu()
		}
	}
}
