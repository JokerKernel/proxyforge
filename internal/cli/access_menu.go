package cli

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strconv"

	"proxyforge/internal/domain"
	"proxyforge/internal/system"
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
		c.printMenuChoice("1", "切换访问模式")
		c.printMenuChoice("2", fmt.Sprintf("白名单管理（%d 条）", len(policy.Whitelist)))
		c.printMenuChoice("3", fmt.Sprintf("黑名单管理（%d 条）", len(policy.Blacklist)))
		switchAction := "关闭访问控制（保留模式和名单）"
		if policy.Disabled {
			switchAction = "启用访问控制（应用保存的模式和名单）"
		}
		c.printMenuChoice("4", switchAction)
		c.printMenuChoice("0/q", "返回")
		choice, err := c.chooseNumber("请选择", 0, 4, 0)
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
		case 4:
			next := policy
			next.Disabled = !policy.Disabled
			confirmation := "关闭访问控制？保留模式和两份名单，恢复原有拦截规则。"
			if !next.Disabled {
				confirmation = "启用访问控制？按保存的模式和两份名单应用规则。"
			}
			success := "访问控制" + accessSwitchLabel(next) + "。"
			err = c.applyAccessPolicyFromMenu(ctx, core, "访问开关", next, policy, confirmation, success)
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
		c.printPageHeader(core, "访问控制", "切换访问模式")
		c.printAccessPolicyCard(policy)
		modes := []string{domain.AccessModeAuto, domain.AccessModeDefault, domain.AccessModeWhitelist}
		currentMode := policy.Mode
		if currentMode == "" {
			currentMode = domain.AccessModeAuto
		}
		defaultChoice := 1
		for i, mode := range modes {
			label := accessModeLabel(mode)
			if mode == domain.AccessModeAuto {
				if policy.Mode == "" {
					label += "（已启用）"
				} else {
					label += "（已关闭）"
				}
			} else if mode == policy.EffectiveMode() {
				label += "（当前）"
			}
			if mode == currentMode {
				defaultChoice = i + 1
			}
			c.printMenuChoice(strconv.Itoa(i+1), label)
		}
		c.printMenuChoice("0/q", "返回")
		choice, err := c.chooseNumber("请选择访问模式", 0, len(modes), defaultChoice)
		if err != nil {
			return err
		}
		if choice == 0 {
			return nil
		}
		next, err := policy.SelectMode(modes[choice-1])
		if err != nil {
			return err
		}
		confirmation := "应用访问模式？白名单优先，原有拦截规则兜底。"
		success := "访问模式已更新。"
		if next.Disabled {
			confirmation += "访问控制保持关闭，本次仅保存模式修改。"
		}
		if err := c.applyAccessPolicyFromMenu(ctx, core, "切换访问模式", next, policy, confirmation, success); err != nil {
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
			additions, entryErr := c.accessEntriesMenu(core, section, name)
			if errors.Is(entryErr, errReturnToMenu) {
				continue
			}
			if entryErr != nil {
				return entryErr
			}
			next, err = next.AddEntries(list, additions)
			if err != nil {
				return err
			}
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
				system.WriteDetail(c.out, "当前状态", "%s", "当前名单已为空，无需修改。")
				c.pauseForMenu()
				continue
			}
			confirmation = "清空" + name + "的全部 " + strconv.Itoa(count) + " 条规则并应用？"
			if list == "white" {
				if policy.Mode == domain.AccessModeWhitelist {
					confirmation += "仍保持仅允许白名单模式；白名单为空时全部拒绝。"
				} else if policy.Mode == "" {
					confirmation += "自动选择将切回默认模式，继续按黑名单和原有拦截规则处理。"
				}
			}
			success = name + "已清空。"
		}
		if next.Disabled {
			confirmation += "访问控制保持关闭，本次仅保存名单修改。"
		}
		if err := c.applyAccessPolicyFromMenu(ctx, core, section, next, policy, confirmation, success); err != nil {
			return err
		}
	}
}

func (c *commandSet) applyAccessPolicyFromMenu(ctx context.Context, core, section string, next, previous domain.AccessPolicy, confirmation, success string) error {
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
		if err := c.app.SetAccessPolicy(ctx, core, next, previous); err != nil {
			c.printMenuError(err)
		} else {
			system.WriteDetail(c.out, "更新结果", "%s", success)
		}
		c.pauseForMenu()
	}
	return nil
}

func (c *commandSet) accessEntriesMenu(core, section, name string) ([]domain.AccessEntry, error) {
	for {
		c.clearScreen()
		c.printPageHeader(core, "访问控制", section, "添加规则")
		c.printLabeledCard("规则说明", [][2]string{
			{"支持目标", "域名、单个 IP 或 CIDR 网段"},
			{"批量输入", "一行输入多个目标，用空格或逗号分隔"},
			{"域名匹配", "默认包括子域名，可选择精确匹配"},
			{"返回", "输入 0/q 返回" + section},
		})
		value, err := c.askDefaultCancelable("目标（多个用空格或逗号分隔）", "")
		if errors.Is(err, errReturnToMenu) || value == "0" {
			return nil, errReturnToMenu
		}
		if err != nil {
			return nil, err
		}
		additions, err := domain.ParseAccessEntries(value, false)
		if err != nil {
			c.printMenuError(err)
			c.pauseForMenu()
			return nil, errReturnToMenu
		}
		domainCount := 0
		for _, entry := range additions {
			if entry.Type == domain.AccessEntryDomain {
				domainCount++
			}
		}
		if domainCount > 0 {
			c.clearScreen()
			c.printPageHeader(core, "访问控制", section, "添加规则", "域名匹配")
			c.printLabeledCard("目标规则", [][2]string{
				{"名单", name}, {"目标数量", strconv.Itoa(len(additions)) + " 条"},
				{"域名匹配", "以下选择对本次所有域名生效，IP 和网段不受影响"},
			})
			for i, entry := range additions {
				fmt.Fprintf(c.out, "%d. %s\n", i+1, entry.Value)
			}
			fmt.Fprintln(c.out)
			c.printMenuChoice("1", "域名及其全部子域名（默认）")
			c.printMenuChoice("2", "仅完整域名")
			c.printMenuChoice("0/q", "返回")
			selected, err := c.chooseNumberCancelable("请选择域名匹配方式", 1, 2, 1)
			if errors.Is(err, errReturnToMenu) {
				continue
			}
			if err != nil {
				return nil, err
			}
			for i := range additions {
				if additions[i].Type == domain.AccessEntryDomain {
					additions[i].IncludeSubdomains = selected == 1
				}
			}
		}
		c.clearScreen()
		c.printPageHeader(core, "访问控制", section, "添加规则", "端口限制")
		fmt.Fprintln(c.out, "逐条设置目标端口；支持 80,443、80.443、中文逗号、8000-8100；all 表示全部端口，q 返回。")
		cancelled := false
		for i := range additions {
			defaultPorts := "all"
			if additions[i].Type == domain.AccessEntryDomain {
				defaultPorts = domain.DefaultAccessDomainPorts
			}
			value, askErr := c.askDefaultCancelable(additions[i].Value+" 的目标端口", defaultPorts)
			if errors.Is(askErr, errReturnToMenu) {
				cancelled = true
				break
			}
			if askErr != nil {
				return nil, askErr
			}
			additions[i].Ports, err = domain.NormalizeAccessPorts(value)
			if err != nil {
				c.printMenuError(err)
				c.pauseForMenu()
				cancelled = true
				break
			}
		}
		if cancelled {
			continue
		}

		return additions, nil
	}
}
