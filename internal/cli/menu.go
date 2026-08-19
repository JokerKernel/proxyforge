package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"proxyforge/internal/domain"
	"proxyforge/internal/install"
)

const proxyForgeHeaderRule = "╰──────────────────────────────────────────────"

const menuDescriptionColumn = 18

const menuStatusColumn = 20

func (c *commandSet) menu(ctx context.Context) error {
	for {
		core, selected, err := c.selectCore(ctx)
		if err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
		if !selected {
			fmt.Fprintln(c.out, "已退出 ProxyForge。")
			return nil
		}
		if err := c.coreMenu(ctx, core); err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
	}
}

func (c *commandSet) coreMenu(ctx context.Context, core string) error {
	for {
		c.clearScreen()
		c.printCoreMenu(core)
		choice, err := c.chooseNumber("请选择", 0, 5, -1)
		if err != nil {
			return err
		}
		if choice == 0 {
			return nil
		}

		c.clearScreen()
		shouldPause := true
		switch choice {
		case 1:
			var confirmed bool
			confirmed, err = c.confirmInstall(core)
			if err == nil && confirmed {
				err = c.app.Install(ctx, core, install.Options{Confirm: c.confirm})
			} else if err == nil {
				fmt.Fprintln(c.out, "已取消安装/升级。")
			}
		case 2:
			shouldPause = false
			err = c.serverConfigMenu(ctx, core)
		case 3:
			shouldPause, err = c.clientMenu(ctx, core)
		case 4:
			shouldPause = false
			err = c.serviceMenu(ctx, core)
		case 5:
			var confirmed bool
			confirmed, err = c.confirmUninstall(core)
			if err == nil && confirmed {
				err = c.app.Uninstall(ctx, core, install.Options{Confirm: c.confirm})
			} else if err == nil {
				fmt.Fprintln(c.out, "已取消卸载。")
			}
		}
		if errors.Is(err, errReturnToMenu) {
			err = nil
			if choice == 1 {
				fmt.Fprintln(c.out, "已取消安装/升级。")
			} else if choice == 5 {
				fmt.Fprintln(c.out, "已取消卸载。")
			}
		}
		if err != nil {
			c.printMenuError(err)
		}
		if shouldPause {
			c.pauseForMenu()
		}
	}
}

func (c *commandSet) serverConfigMenu(ctx context.Context, core string) error {
	for {
		c.clearScreen()
		c.printPageHeader(core, "服务端配置")
		c.printMenuChoice("1", "生成/更新配置（完整覆盖现有配置，不合并原配置）")
		c.printMenuChoice("2", "查看配置")
		c.printMenuChoice("3", "编辑配置（vim / nano / vi）")
		c.printMenuChoice("4", "修改配置（DNS、重置节点与 SNI 检测）")
		maxChoice := 4
		if core == domain.CoreXray {
			c.printMenuChoice("5", "专用运行用户（修复 systemd 的 nobody 安全警告）")
			maxChoice = 5
		}
		c.printMenuChoice("0/q", "返回")
		choice, err := c.chooseNumber("请选择", 0, maxChoice, 0)
		if err != nil {
			return err
		}
		if choice == 0 {
			return nil
		}

		c.clearScreen()
		shouldPause := true
		switch choice {
		case 1:
			o := domain.GenerateOptions{}
			err = c.app.CheckCoreInstalled(ctx, core)
			if err == nil {
				err = c.confirmServerConfigOverwrite(core, true)
			}
			if errors.Is(err, errReturnToMenu) {
				fmt.Fprintln(c.out, "已取消生成服务端配置，返回服务端配置菜单。")
				continue
			}
			if err == nil {
				err = c.fillGenerate(ctx, core, &o)
			}
			if err == nil {
				err = c.runGenerate(ctx, core, o)
			}
			if errors.Is(err, errReturnToMenu) {
				fmt.Fprintln(c.out, "已取消生成服务端配置。")
				return nil
			}
		case 2:
			var b []byte
			b, err = c.app.ServerConfig(core)
			if err == nil {
				// ServerConfig may emit progress on stderr. Clear once more after the
				// read so merged terminal streams cannot split the displayed JSON.
				c.clearScreen()
				c.printPageHeader(core, "查看服务端配置")
				fmt.Fprintln(c.out, "警告：当前服务端配置可能包含 UUID、REALITY 私钥等敏感信息，请勿泄露。")
				fmt.Fprintln(c.out, "----------------------------------------")
				_, err = c.out.Write(b)
				if err == nil && len(b) > 0 && b[len(b)-1] != '\n' {
					fmt.Fprintln(c.out)
				}
			}
		case 3:
			err = c.editServerConfig(core)
		case 4:
			shouldPause = false
			err = c.modifyConfigMenu(ctx, core)
		case 5:
			err = c.dedicatedXrayServiceUser(ctx)
			if errors.Is(err, errReturnToMenu) {
				continue
			}
		}
		if err != nil {
			c.printMenuError(err)
		}
		if shouldPause {
			c.pauseForMenu()
		}
	}
}

func (c *commandSet) modifyConfigMenu(ctx context.Context, core string) error {
	for {
		c.clearScreen()
		c.printPageHeader(core, "修改配置")
		c.printMenuChoice("1", "DNS 设置")
		c.printMenuChoice("2", "重置 SNI/target（保留节点凭证）")
		c.printMenuChoice("3", "重置节点凭证（UUID、REALITY 密钥和 short ID；保留 SNI/target）")
		c.printMenuChoice("4", "REALITY SNI 候选检测（重新测试，不修改配置）")
		c.printMenuChoice("0/q", "返回")
		choice, err := c.chooseNumber("请选择", 0, 4, 0)
		if err != nil {
			return err
		}
		if choice == 0 {
			return nil
		}

		c.clearScreen()
		switch choice {
		case 1:
			err = c.dnsSettingsMenu(ctx, core)
			if errors.Is(err, errReturnToMenu) {
				continue
			}
		case 2:
			err = c.resetChoice(ctx, core, 1)
		case 3:
			err = c.resetChoice(ctx, core, 2)
		case 4:
			err = c.retestSNICandidates(ctx, core)
		}
		if err != nil {
			c.printMenuError(err)
		}
		c.pauseForMenu()
	}
}

func (c *commandSet) selectCore(ctx context.Context) (string, bool, error) {
	c.clearScreen()
	c.printProxyForgeHeader()
	c.printMenuStatusChoice("1", "Xray-core", c.coreInstalled(ctx, domain.CoreXray))
	c.printMenuStatusChoice("2", "sing-box", c.coreInstalled(ctx, domain.CoreSingBox))
	c.printMenuChoice("0/q", "退出")
	choice, err := c.chooseNumber("请选择", 0, 2, 1)
	if err != nil {
		return "", false, err
	}
	switch choice {
	case 1:
		return domain.CoreXray, true, nil
	case 2:
		return domain.CoreSingBox, true, nil
	default:
		return "", false, nil
	}
}

func (c *commandSet) coreInstalled(ctx context.Context, core string) bool {
	if c.app == nil {
		return false
	}
	return c.app.CheckCoreInstalled(ctx, core) == nil
}

func (c *commandSet) printCoreMenu(core string) {
	c.printPageHeader(core)
	fmt.Fprintf(c.out, "当前内核：%s\n\n", core)
	c.printMenuChoice("1", "安装/升级（安装内核或升级版本）")
	c.printMenuChoice("2", "服务端配置（生成、查看、DNS 与节点重置）")
	c.printMenuChoice("3", "客户端配置（导出原生 JSON 或 Clash YAML）")
	c.printMenuChoice("4", "服务管理（启动、停止、状态与日志）")
	c.printMenuChoice("5", "卸载内核（同时清理配置和运行数据）")
	c.printMenuChoice("0/q", "返回")
}

func (c *commandSet) printProxyForgeHeader() {
	fmt.Fprintln(c.out, "╭─ ProxyForge")
	fmt.Fprintf(c.out, "│ 双内核代理管理器  [版本 %s]\n", displayCurrentVersion(c.currentVersion))
	fmt.Fprintln(c.out, proxyForgeHeaderRule)
	fmt.Fprintln(c.out)
}

func (c *commandSet) printPageHeader(parts ...string) {
	clean := make([]string, 0, len(parts)+1)
	clean = append(clean, "ProxyForge")
	for _, part := range parts {
		if part = strings.TrimSpace(part); part != "" {
			clean = append(clean, part)
		}
	}
	fmt.Fprintf(c.out, "╭─ %s\n", strings.Join(clean, "  ›  "))
	fmt.Fprintln(c.out, proxyForgeHeaderRule)
	fmt.Fprintln(c.out)
}

func (c *commandSet) printMenuChoice(key, label string) {
	title, description := splitMenuChoiceLabel(label)
	var line strings.Builder
	fmt.Fprintf(&line, "  %-3s %s", key, title)
	if description != "" {
		padding := menuDescriptionColumn - menuDisplayWidth(title)
		if padding < 2 {
			padding = 2
		}
		fmt.Fprintf(&line, "%s-- %s", strings.Repeat(" ", padding), description)
	}
	fmt.Fprintln(c.out, line.String())
}

func (c *commandSet) printMenuStatusChoice(key, title string, installed bool) {
	status := "[未安装]"
	if installed {
		status = "[已安装]"
	}
	padding := menuStatusColumn - menuDisplayWidth(title)
	if padding < 2 {
		padding = 2
	}
	fmt.Fprintf(c.out, "  %-3s %s%s%s\n", key, title, strings.Repeat(" ", padding), status)
}

func menuDisplayWidth(value string) int {
	width := 0
	for _, r := range value {
		if isWideMenuRune(r) {
			width += 2
		} else {
			width++
		}
	}
	return width
}

func isWideMenuRune(r rune) bool {
	return r >= 0x1100 && (r <= 0x115f ||
		r == 0x2329 || r == 0x232a ||
		(r >= 0x2e80 && r <= 0xa4cf && r != 0x303f) ||
		(r >= 0xac00 && r <= 0xd7a3) ||
		(r >= 0xf900 && r <= 0xfaff) ||
		(r >= 0xfe10 && r <= 0xfe19) ||
		(r >= 0xfe30 && r <= 0xfe6f) ||
		(r >= 0xff00 && r <= 0xff60) ||
		(r >= 0xffe0 && r <= 0xffe6) ||
		(r >= 0x1f300 && r <= 0x1faff) ||
		(r >= 0x20000 && r <= 0x3fffd))
}

func splitMenuChoiceLabel(label string) (string, string) {
	label = strings.TrimSpace(label)
	if !strings.HasSuffix(label, "）") {
		return label, ""
	}
	start := strings.LastIndex(label, "（")
	if start <= 0 {
		return label, ""
	}
	title := strings.TrimSpace(label[:start])
	description := strings.TrimSpace(strings.TrimSuffix(label[start+len("（"):], "）"))
	if title == "" || description == "" {
		return label, ""
	}
	return title, description
}

func displayCurrentVersion(version string) string {
	version = strings.TrimSpace(version)
	if version == "" {
		return "unknown"
	}
	return version
}
