package cli

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
	"proxyforge/internal/domain"
)

func accessModeLabel(mode string) string {
	switch mode {
	case domain.AccessModeWhitelist:
		return "仅允许白名单访问"
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
	var order []string
	if len(policy.Whitelist) > 0 {
		order = append(order, "白名单放行")
	}
	if policy.BlacklistEnabled() && len(policy.Blacklist) > 0 {
		order = append(order, "黑名单拒绝")
	}
	order = append(order, "原有拦截")
	defaultAction := "放行"
	finalAction := "其余放行"
	if policy.EffectiveMode() == domain.AccessModeWhitelist {
		defaultAction = "拒绝；白名单为空时全部拒绝。"
		finalAction = "其余拒绝"
	}
	order = append(order, finalAction)
	c.printLabeledCard("访问策略", [][2]string{
		{"访问模式", accessModeLabel(policy.EffectiveMode())},
		{"规则顺序", strings.Join(order, " → ")},
		{"默认动作", defaultAction},
	})
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
	cmd.AddCommand(&cobra.Command{Use: "mode <sing-box|xray> <default|whitelist>", Short: "切换访问模式，保留名单", Args: cobra.ExactArgs(2), RunE: func(cmd *cobra.Command, args []string) error {
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
	cmd.AddCommand(&cobra.Command{Use: "clear <sing-box|xray> <white|black>", Short: "清空指定名单；清空白名单时退出仅允许白名单模式", Args: cobra.ExactArgs(2), RunE: func(cmd *cobra.Command, args []string) error {
		policy, err := c.app.AccessPolicy(args[0])
		if err != nil {
			return err
		}
		next, err := policy.ClearList(args[1])
		if err != nil {
			return err
		}
		if reflect.DeepEqual(policy, next) {
			fmt.Fprintln(c.out, "当前名单已为空，无需修改。")
			return nil
		}
		if err = c.confirmAccessChange(next); err != nil {
			if errors.Is(err, errReturnToMenu) {
				return nil
			}
			return err
		}
		if err = c.app.SetAccessPolicy(cmd.Context(), args[0], next); err != nil {
			return err
		}
		fmt.Fprintln(c.out, "访问名单已清空。")
		return nil
	}})
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
		c.printMenuChoice("6", "清空白名单（同时退出仅允许白名单模式）")
		c.printMenuChoice("7", "清空黑名单")
		c.printMenuChoice("0/q", "返回")
		choice, err := c.chooseNumber("请选择", 0, 7, 0)
		if err != nil {
			return err
		}
		if choice == 0 {
			return nil
		}
		next := policy
		confirmation := "应用访问策略？白名单优先，明确允许的私网目标也会放行。"
		success := "访问策略已生效。"
		if choice == 1 {
			c.clearScreen()
			c.printPageHeader(core, "访问控制", "切换访问模式")
			c.printLabeledCard("当前模式", [][2]string{{"访问模式", accessModeLabel(policy.EffectiveMode())}})
			modes := []string{domain.AccessModeDefault, domain.AccessModeWhitelist}
			defaultChoice := 1
			for i, mode := range modes {
				label := accessModeLabel(mode)
				if mode == policy.EffectiveMode() {
					label += "（当前）"
					defaultChoice = i + 1
				}
				c.printMenuChoice(strconv.Itoa(i+1), label)
			}
			c.printMenuChoice("0/q", "返回")
			selected, e := c.chooseNumberCancelable("请选择访问模式", 1, len(modes), defaultChoice)
			if errors.Is(e, errReturnToMenu) {
				continue
			}
			if e != nil {
				return e
			}
			next.Mode = modes[selected-1]
		} else {
			entries := &next.Whitelist
			listName := "白名单"
			if choice == 4 || choice == 5 || choice == 7 {
				entries = &next.Blacklist
				listName = "黑名单"
			}
			c.clearScreen()
			if choice == 6 || choice == 7 {
				key := "white"
				if choice == 7 {
					key = "black"
				}
				count := len(*entries)
				next, err = policy.ClearList(key)
				if err != nil {
					return err
				}
				if reflect.DeepEqual(policy, next) {
					c.printPageHeader(core, "访问控制", "清空"+listName)
					fmt.Fprintln(c.out, "当前名单已为空，无需修改。")
					c.pauseForMenu()
					continue
				}
				confirmation = "清空" + listName + "的全部 " + strconv.Itoa(count) + " 条规则并应用？"
				if policy.EffectiveMode() == domain.AccessModeWhitelist && choice == 6 {
					confirmation += "将退出仅允许白名单模式，切回默认模式。"
				}
				success = listName + "已清空。"
			} else if choice == 2 || choice == 4 {
				c.printPageHeader(core, "访问控制", "添加"+listName)
				c.printLabeledCard("规则说明", [][2]string{
					{"支持目标", "域名、单个 IP 或 CIDR 网段"},
					{"域名匹配", "默认包括子域名，可选择精确匹配"},
					{"返回", "输入 0/q 返回访问控制"},
				})
				value, e := c.askDefaultCancelable("域名、IP 或 CIDR 网段", "")
				if errors.Is(e, errReturnToMenu) || value == "0" {
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
					c.clearScreen()
					c.printPageHeader(core, "访问控制", "添加"+listName, "域名匹配")
					c.printLabeledCard("目标规则", [][2]string{{"名单", listName}, {"域名", entry.Value}})
					c.printMenuChoice("1", "域名及其全部子域名（默认）")
					c.printMenuChoice("2", "仅完整域名")
					c.printMenuChoice("0/q", "返回")
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
				c.printPageHeader(core, "访问控制", "删除"+listName)
				c.printLabeledCard("当前名单", [][2]string{{"名单", listName}, {"规则数量", strconv.Itoa(len(*entries)) + " 条"}})
				if len(*entries) == 0 {
					fmt.Fprintln(c.out, "当前名单为空。")
					c.pauseForMenu()
					continue
				}
				for i, entry := range *entries {
					c.printMenuChoice(strconv.Itoa(i+1), accessEntryLabel(entry))
				}
				c.printMenuChoice("0/q", "返回")
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
		c.clearScreen()
		c.printPageHeader(core, "访问控制", "确认修改")
		c.printAccessPolicy(next)
		ok, err := c.confirmCancelable(confirmation)
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
				fmt.Fprintln(c.out, success)
			}
			c.pauseForMenu()
		}
	}
}
