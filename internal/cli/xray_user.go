package cli

import (
	"context"

	"proxyforge/internal/system"
)

func (c *commandSet) dedicatedXrayServiceUser(ctx context.Context) error {
	current, err := c.app.XrayServiceUser(ctx)
	if err != nil {
		return err
	}
	exists, err := c.app.XrayServiceAccountExists(ctx)
	if err != nil {
		return err
	}
	accountStatus := "账号状态：尚未创建 xray 系统用户"
	accountAction := "通过 systemd-sysusers 创建专用 xray 系统用户和组"
	confirmation := "创建 xray 专用用户并应用服务与权限设置？"
	if exists {
		accountStatus = "账号状态：已存在 xray 系统用户，确认后核验并复用，保留原 home"
		accountAction = "核验并复用已有 xray 系统用户"
		confirmation = "是否复用已有 xray 用户，并覆盖 systemd 运行用户设置及同步文件权限？"
	}
	c.printConfirmationPanel(
		"操作确认：启用 Xray 专用运行用户",
		[]string{"当前运行用户：" + current, "目标运行用户：xray（专用低权限系统用户）", accountStatus},
		confirmationSection{title: "将执行", items: []string{
			accountAction,
			"更新 xray.service 与已存在的 xray@.service 中的运行用户",
			"写入或覆盖 ProxyForge 的 systemd 用户设置：User=xray、Group=xray",
			"同步服务端配置、受管证书及默认日志的属主和权限",
			"以 xray 身份预检配置，服务运行中且设置变化时重启",
		}},
		confirmationSection{title: "保留内容", items: []string{
			"保留节点配置内容，包括 UUID、路由及其他代理用户",
		}},
	)
	confirmed, err := c.confirmCancelable(confirmation)
	if err != nil {
		return err
	}
	if !confirmed {
		system.WriteDetail(c.out, "取消操作", "%s", "已取消运行用户修改。")
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
		system.WriteDetail(c.out, "操作结果", "Xray 已使用专用系统用户 %s，账号、配置权限和 systemd 设置核验通过，无需修改。", change.Current)
		return nil
	}
	system.WriteDetail(c.out, "操作结果", "Xray 运行用户已从 %s 修改为 %s；%s；%s。",
		change.Previous, change.Current, created, effect)
	return nil
}
