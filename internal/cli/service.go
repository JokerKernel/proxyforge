package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"strconv"

	"github.com/spf13/cobra"

	"proxyforge/internal/domain"
)

func (c *commandSet) serviceCommand() *cobra.Command {
	return &cobra.Command{
		Use: "service <sing-box|xray> <start|stop|restart|status|logs>", Short: "管理单个内核的 systemd 服务", Args: cobra.ExactArgs(2),
		ValidArgsFunction: func(_ *cobra.Command, _ []string, _ string) ([]string, cobra.ShellCompDirective) {
			return []string{"sing-box", "xray", "start", "stop", "restart", "status", "logs"}, cobra.ShellCompDirectiveNoFileComp
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			b, err := c.app.Service(cmd.Context(), args[0], args[1])
			if len(b) > 0 {
				fmt.Fprint(c.out, string(b))
				if b[len(b)-1] != '\n' {
					fmt.Fprintln(c.out)
				}
			}
			return err
		},
	}
}

func (c *commandSet) serviceMenu(ctx context.Context, core string) error {
	actions := []string{"", "start", "stop", "restart", "status", "logs"}
	for {
		c.clearScreen()
		c.printPageHeader(core, "服务管理")
		c.printMenuChoice("1", "启动")
		c.printMenuChoice("2", "停止")
		c.printMenuChoice("3", "重启")
		c.printMenuChoice("4", "状态")
		c.printMenuChoice("5", "最近日志")
		c.printMenuChoice("6", "实时日志（Ctrl+C 返回）")
		c.printMenuChoice("7", "日志级别")
		c.printMenuChoice("0/q", "返回")
		choice, chooseErr := c.chooseNumber("请选择", 0, 7, 4)
		if chooseErr != nil {
			return chooseErr
		}
		if choice == 0 {
			return nil
		}
		if choice == 6 {
			c.clearScreen()
			if followErr := c.followServiceLogs(ctx, core); followErr != nil {
				c.printMenuError(followErr)
				c.pauseForMenu()
			}
			continue
		}
		if choice == 7 {
			c.clearScreen()
			logErr := c.logLevelMenu(ctx, core)
			if errors.Is(logErr, errReturnToMenu) {
				continue
			}
			if logErr != nil {
				c.printMenuError(logErr)
			}
			c.pauseForMenu()
			continue
		}
		b, actionErr := c.app.Service(ctx, core, actions[choice])
		if len(b) > 0 {
			fmt.Fprint(c.out, string(b))
			if b[len(b)-1] != '\n' {
				fmt.Fprintln(c.out)
			}
		}
		if actionErr != nil {
			c.printMenuError(actionErr)
		}
		c.pauseForMenu()
	}
}

func (c *commandSet) logLevelMenu(ctx context.Context, core string) error {
	settings, err := c.app.LogLevelSettings(ctx, core)
	if err != nil {
		return err
	}
	c.clearScreen()
	c.printPageHeader(core, "日志级别")
	fmt.Fprintf(c.out, "当前级别：%s\n\n", logLevelDisplay(core, settings.Current))
	defaultChoice := 1
	for index, level := range settings.Levels {
		c.printMenuChoice(strconv.Itoa(index+1), logLevelDisplay(core, level))
		if level == settings.Current {
			defaultChoice = index + 1
		}
	}
	c.printMenuChoice("0/q", "返回")
	choice, err := c.chooseNumber("请选择日志级别", 0, len(settings.Levels), defaultChoice)
	if err != nil {
		return err
	}
	if choice == 0 {
		return errReturnToMenu
	}
	selected := settings.Levels[choice-1]
	if selected == settings.Current {
		fmt.Fprintf(c.out, "[提示] 日志级别已经是 %s，无需修改。\n", logLevelDisplay(core, selected))
		return nil
	}

	sections := []confirmationSection{{title: "将执行", items: []string{
		"使用内核原生命令校验候选配置",
		"备份并原子更新当前服务端配置",
		"服务正在运行时自动重启使设置立即生效",
	}}}
	if selected == "off" {
		sections = append(sections, confirmationSection{title: "注意", items: []string{
			"关闭后将无法通过 systemd journal 查看内核运行日志",
			"排查故障时需要重新开启日志",
		}})
	}
	c.printConfirmationPanel(
		"操作确认：设置日志级别",
		[]string{
			"目标内核：" + core,
			"当前级别：" + logLevelDisplay(core, settings.Current),
			"新的级别：" + logLevelDisplay(core, selected),
		},
		sections...,
	)
	confirmed, err := c.confirmCancelable("应用新的日志级别？")
	if err != nil {
		return err
	}
	if !confirmed {
		fmt.Fprintln(c.out, "已取消日志级别修改。")
		return nil
	}
	change, err := c.app.SetLogLevel(ctx, core, selected)
	if err != nil {
		return err
	}
	if !change.Changed {
		fmt.Fprintf(c.out, "[提示] 日志级别已经是 %s，无需修改。\n", logLevelDisplay(core, change.Current))
		return nil
	}
	effect := "服务当前未运行，将在下次启动时生效"
	if change.Restarted {
		effect = "服务已重启，设置已生效"
	}
	fmt.Fprintf(c.out, "[结果] %s 日志级别已从 %s 修改为 %s；%s。\n",
		core, logLevelDisplay(core, change.Previous), logLevelDisplay(core, change.Current), effect)
	return nil
}

func logLevelDisplay(core, level string) string {
	if level == "default" {
		return "未显式设置（使用内核默认值）"
	}
	descriptions := map[string]string{
		"trace":   "trace（最详细跟踪）",
		"debug":   "debug（调试信息）",
		"info":    "info（常规运行信息）",
		"warn":    "warn（警告及错误）",
		"warning": "warning（警告及错误）",
		"error":   "error（仅错误）",
		"fatal":   "fatal（仅致命错误）",
		"panic":   "panic（仅崩溃信息）",
		"off":     "关闭日志",
	}
	display := descriptions[level]
	if display == "" {
		return level
	}
	if (core == domain.CoreSingBox && level == "info") || (core == domain.CoreXray && level == "warning") {
		return display + "（ProxyForge 默认）"
	}
	if core == domain.CoreXray && level == "off" {
		return "关闭访问日志和错误日志"
	}
	return display
}

func (c *commandSet) followServiceLogs(ctx context.Context, core string) error {
	interruptContext := c.interruptContext
	if interruptContext == nil {
		interruptContext = func(parent context.Context) (context.Context, context.CancelFunc) {
			return signal.NotifyContext(parent, os.Interrupt)
		}
	}
	followCtx, stop := interruptContext(ctx)
	defer stop()

	c.printPageHeader(core, "实时日志")
	fmt.Fprintln(c.out, "按 Ctrl+C 返回服务管理")
	fmt.Fprintln(c.out, "----------------------------------------")
	err := c.app.FollowServiceLogs(followCtx, core, c.out)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if followCtx.Err() != nil {
		fmt.Fprintln(c.out, "\n已停止实时日志，返回服务管理菜单。")
		return nil
	}
	return err
}
