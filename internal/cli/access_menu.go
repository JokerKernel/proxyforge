package cli

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strconv"

	"proxyforge/internal/domain"
)

func (c *commandSet) accessPolicyMenu(ctx context.Context, core string) error {
	for {
		policy, err := c.app.AccessPolicy(core)
		if err != nil {
			return err
		}
		c.clearScreen()
		c.printPageHeader(core, "访问控制")
		c.printAccessPolicyCard(policy)
		c.printMenuChoice("1", "模式与开关")
		c.printMenuChoice("2", fmt.Sprintf("白名单管理（%d 条）", len(policy.Whitelist)))
		c.printMenuChoice("3", fmt.Sprintf("黑名单管理（%d 条）", len(policy.Blacklist)))
		c.printMenuChoice("0/q", "返回")
		choice, err := c.chooseNumber("请选择", 0, 3, 0)
		if err != nil {
			return err
		}
		switch choice {
		case 0:
			return nil
		case 1:
			err = c.accessModeMenu(ctx, core)
		case 2:
			err = c.accessListMenu(ctx, core, "white")
		case 3:
			err = c.accessListMenu(ctx, core, "black")
		}
		if err != nil {
			return err
		}
	}
}

func (c *commandSet) accessModeMenu(ctx context.Context, core string) error {
	for {
		policy, err := c.app.AccessPolicy(core)
		if err != nil {
			return err
		}
		c.clearScreen()
		c.printPageHeader(core, "访问控制", "模式与开关")
		c.printAccessPolicyCard(policy)
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
		switchAction := "关闭访问控制（保留模式和名单）"
		if policy.Disabled {
			switchAction = "启用访问控制（应用保存的模式和名单）"
		}
		c.printMenuChoice("3", switchAction)
		c.printMenuChoice("0/q", "返回")
		choice, err := c.chooseNumber("请选择访问模式或开关", 0, len(modes)+1, defaultChoice)
		if err != nil {
			return err
		}
		if choice == 0 {
			return nil
		}
		next := policy
		confirmation := "应用访问模式？白名单优先，原有拦截规则兜底。"
		success := "访问模式已更新。"
		if choice == len(modes)+1 {
			next.Disabled = !policy.Disabled
			confirmation = "关闭访问控制？保留模式和两份名单，恢复原有拦截规则。"
			if !next.Disabled {
				confirmation = "启用访问控制？按保存的模式和两份名单应用规则。"
			}
			success = "访问控制" + accessSwitchLabel(next) + "。"
		} else {
			next.Mode = modes[choice-1]
			if next.Disabled {
				confirmation += "访问控制保持关闭，本次仅保存模式修改。"
			}
		}
		if err := c.applyAccessPolicyFromMenu(ctx, core, "模式与开关", next, confirmation, success); err != nil {
			return err
		}
	}
}

func (c *commandSet) accessListMenu(ctx context.Context, core, list string) error {
	name := "白名单"
	if list == "black" {
		name = "黑名单"
	} else if list != "white" {
		return fmt.Errorf("名单类型无效（可选 white 或 black）")
	}
	section := name + "管理"
	for {
		policy, err := c.app.AccessPolicy(core)
		if err != nil {
			return err
		}
		next := policy
		entries := &next.Whitelist
		if list == "black" {
			entries = &next.Blacklist
		}
		c.clearScreen()
		c.printPageHeader(core, "访问控制", section)
		c.printAccessPolicyCard(policy)
		c.printAccessEntries(name, *entries)
		fmt.Fprintln(c.out)
		c.printMenuChoice("1", "添加规则")
		c.printMenuChoice("2", "删除规则")
		c.printMenuChoice("3", "清空名单")
		c.printMenuChoice("0/q", "返回")
		choice, err := c.chooseNumber("请选择", 0, 3, 0)
		if err != nil {
			return err
		}
		if choice == 0 {
			return nil
		}
		confirmation := "应用" + name + "修改？白名单优先，原有拦截规则兜底。"
		success := name + "已更新。"
		c.clearScreen()
		switch choice {
		case 1:
			c.printPageHeader(core, "访问控制", section, "添加规则")
			c.printLabeledCard("规则说明", [][2]string{
				{"支持目标", "域名、单个 IP 或 CIDR 网段"},
				{"域名匹配", "默认包括子域名，可选择精确匹配"},
				{"返回", "输入 0/q 返回" + section},
			})
			value, err := c.askDefaultCancelable("域名、IP 或 CIDR 网段", "")
			if errors.Is(err, errReturnToMenu) || value == "0" {
				continue
			}
			if err != nil {
				return err
			}
			entry, err := domain.ParseAccessEntry(value, false)
			if err != nil {
				c.printMenuError(err)
				c.pauseForMenu()
				continue
			}
			if entry.Type == domain.AccessEntryDomain {
				c.clearScreen()
				c.printPageHeader(core, "访问控制", section, "添加规则", "域名匹配")
				c.printLabeledCard("目标规则", [][2]string{{"名单", name}, {"域名", entry.Value}})
				c.printMenuChoice("1", "域名及其全部子域名（默认）")
				c.printMenuChoice("2", "仅完整域名")
				c.printMenuChoice("0/q", "返回")
				selected, err := c.chooseNumberCancelable("请选择域名匹配方式", 1, 2, 1)
				if errors.Is(err, errReturnToMenu) {
					continue
				}
				if err != nil {
					return err
				}
				entry.IncludeSubdomains = selected == 1
			}
			*entries = append(*entries, entry)
		case 2:
			c.printPageHeader(core, "访问控制", section, "删除规则")
			c.printLabeledCard("当前名单", [][2]string{{"名单", name}, {"规则数量", strconv.Itoa(len(*entries)) + " 条"}})
			if len(*entries) == 0 {
				fmt.Fprintln(c.out, "当前名单为空。")
				c.pauseForMenu()
				continue
			}
			for i, entry := range *entries {
				c.printMenuChoice(strconv.Itoa(i+1), accessEntryLabel(entry))
			}
			c.printMenuChoice("0/q", "返回")
			selected, err := c.chooseNumberCancelable("请选择要删除的规则", 1, len(*entries), 1)
			if errors.Is(err, errReturnToMenu) {
				continue
			}
			if err != nil {
				return err
			}
			copied := append([]domain.AccessEntry(nil), (*entries)...)
			*entries = append(copied[:selected-1], copied[selected:]...)
		case 3:
			count := len(*entries)
			next, err = policy.ClearList(list)
			if err != nil {
				return err
			}
			if reflect.DeepEqual(policy, next) {
				c.printPageHeader(core, "访问控制", section, "清空名单")
				fmt.Fprintln(c.out, "当前名单已为空，无需修改。")
				c.pauseForMenu()
				continue
			}
			confirmation = "清空" + name + "的全部 " + strconv.Itoa(count) + " 条规则并应用？"
			if policy.EffectiveMode() == domain.AccessModeWhitelist && list == "white" {
				confirmation += "将退出仅允许白名单模式，切回默认模式。"
			}
			success = name + "已清空。"
		}
		if next.Disabled {
			confirmation += "访问控制保持关闭，本次仅保存名单修改。"
		}
		if err := c.applyAccessPolicyFromMenu(ctx, core, section, next, confirmation, success); err != nil {
			return err
		}
	}
}

func (c *commandSet) applyAccessPolicyFromMenu(ctx context.Context, core, section string, next domain.AccessPolicy, confirmation, success string) error {
	next, err := next.Normalize()
	if err != nil {
		c.printMenuError(err)
		c.pauseForMenu()
		return nil
	}
	c.clearScreen()
	c.printPageHeader(core, "访问控制", section, "确认修改")
	c.printAccessPolicy(next)
	ok, err := c.confirmCancelable(confirmation)
	if errors.Is(err, errReturnToMenu) {
		return nil
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
	return nil
}
