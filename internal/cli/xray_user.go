package cli

import (
	"context"
	"fmt"
)

func (c *commandSet) dedicatedXrayServiceUser(ctx context.Context) error {
	current, err := c.app.XrayServiceUser(ctx)
	if err != nil {
		return err
	}
	c.printConfirmationPanel(
		"操作确认：启用 Xray 专用运行用户",
		[]string{"当前运行用户：" + current, "新的运行用户：xray（专用低权限系统用户）"},
		confirmationSection{title: "将执行", items: []string{
			"通过 systemd-sysusers 创建专用 xray 系统用户和组（不存在时）",
			"更新官方 xray.service 与 xray@.service 中的 User=，消除 nobody 解析警告",
			"写入 ProxyForge systemd drop-in，后续官方升级仍保持专用用户",
			"同步权限并以 xray 身份预检配置，服务运行时自动重启",
		}},
	)
	confirmed, err := c.confirmCancelable("应用专用运行用户设置？")
	if err != nil {
		return err
	}
	if !confirmed {
		fmt.Fprintln(c.out, "已取消运行用户修改。")
		return nil
	}
	change, err := c.app.UseDedicatedXrayServiceUser(ctx)
	if err != nil {
		return err
	}
	effect := "服务当前未运行，将在下次启动时生效"
	if change.Restarted {
		effect = "服务已重启并生效"
	}
	created := "复用了已有 xray 系统用户"
	if change.UserCreated {
		created = "已创建 xray 系统用户"
	}
	if !change.Changed {
		fmt.Fprintf(c.out, "[结果] Xray 已使用专用系统用户 %s，账号、配置权限和 systemd 设置核验通过，无需修改。\n", change.Current)
		return nil
	}
	fmt.Fprintf(c.out, "[结果] Xray 运行用户已从 %s 修改为 %s；%s；%s。\n",
		change.Previous, change.Current, created, effect)
	return nil
}
