package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"proxyforge/internal/app"
	"proxyforge/internal/domain"
)

func (c *commandSet) landingCommand() *cobra.Command {
	cmd := &cobra.Command{Use: "landing", Short: "管理供中转机连接的落地接入"}
	cmd.AddCommand(c.landingAddCommand(), c.landingListCommand(), c.landingExportCommand(),
		c.landingToggleCommand("enable", true), c.landingToggleCommand("disable", false),
		c.landingRotateCommand(), c.landingRemoveCommand())
	return cmd
}

func (c *commandSet) landingAddCommand() *cobra.Command {
	var output string
	var force bool
	var security, sni, certFile, keyFile string
	var port int
	cmd := &cobra.Command{Use: "add <sing-box|xray> <name>", Short: "创建落地接入并输出可复制的连接文本", Args: cobra.ExactArgs(2), RunE: func(cmd *cobra.Command, args []string) error {
		opts := app.LandingAddOptions{Security: security, Port: port, SNI: sni, CertificateFile: certFile, KeyFile: keyFile}
		if _, err := c.app.AddLandingAccessWithOptions(cmd.Context(), args[0], args[1], opts); err != nil {
			return err
		}
		b, err := c.app.ExportLandingBundle(args[0], args[1], output, force)
		if err != nil {
			return fmt.Errorf("落地接入已创建，但生成连接文本失败（可稍后执行 landing export）：%w", err)
		}
		if output == "" {
			_, err = c.out.Write(b)
		} else {
			fmt.Fprintf(c.out, "落地连接文件已安全写入 %s（0600）\n", output)
		}
		return err
	}}
	cmd.Flags().StringVarP(&output, "output", "o", "", "兼容选项：将连接文本写入文件（默认输出到终端）")
	cmd.Flags().BoolVar(&force, "force", false, "覆盖已有输出文件")
	cmd.Flags().StringVar(&security, "security", domain.LandingSecurityReality, "落地安全协议：reality 或 tls")
	cmd.Flags().IntVar(&port, "port", 0, "TLS 落地独立监听端口（默认随机选择 30000-65000）")
	cmd.Flags().StringVar(&sni, "server-name", "", "TLS 证书域名")
	cmd.Flags().StringVar(&certFile, "cert-file", "", "TLS 证书链文件绝对路径")
	cmd.Flags().StringVar(&keyFile, "key-file", "", "TLS 私钥文件绝对路径")
	return cmd
}

func (c *commandSet) landingListCommand() *cobra.Command {
	return &cobra.Command{Use: "list <sing-box|xray>", Short: "列出落地接入", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		items, err := c.app.LandingAccesses(args[0])
		if err != nil {
			return err
		}
		if len(items) == 0 {
			fmt.Fprintln(c.out, "尚未配置落地接入。")
			return nil
		}
		for _, item := range items {
			security := domain.NormalizeLandingSecurity(item.Security)
			endpoint := "当前 REALITY 入站"
			if security == domain.LandingSecurityTLS {
				endpoint = fmt.Sprintf("TLS :%d", item.Port)
			}
			fmt.Fprintf(c.out, "%s\t%s\t%s\t%s\t%s\n", item.Name, item.UserName, security, endpoint, enabledLabel(item.Enabled))
		}
		return nil
	}}
}

func (c *commandSet) landingExportCommand() *cobra.Command {
	var output string
	var force bool
	cmd := &cobra.Command{Use: "export <sing-box|xray> <name>", Aliases: []string{"show"}, Short: "显示可复制的落地连接文本", Args: cobra.ExactArgs(2), RunE: func(cmd *cobra.Command, args []string) error {
		b, err := c.app.ExportLandingBundle(args[0], args[1], output, force)
		if err != nil {
			return err
		}
		if output == "" {
			_, err = c.out.Write(b)
		} else {
			fmt.Fprintf(c.out, "落地连接文件已安全写入 %s（0600）\n", output)
		}
		return err
	}}
	cmd.Flags().StringVarP(&output, "output", "o", "", "兼容选项：将连接文本写入文件（默认输出到终端）")
	cmd.Flags().BoolVar(&force, "force", false, "覆盖已有输出文件")
	return cmd
}

func (c *commandSet) landingToggleCommand(action string, enabled bool) *cobra.Command {
	label := "启用"
	if !enabled {
		label = "停用"
	}
	return &cobra.Command{Use: action + " <sing-box|xray> <name>", Short: label + "落地接入", Args: cobra.ExactArgs(2), RunE: func(cmd *cobra.Command, args []string) error {
		if err := c.app.SetLandingAccessEnabled(cmd.Context(), args[0], args[1], enabled); err != nil {
			return err
		}
		fmt.Fprintf(c.out, "落地接入 %s 已%s。\n", args[1], label)
		return nil
	}}
}

func (c *commandSet) landingRotateCommand() *cobra.Command {
	return &cobra.Command{Use: "rotate <sing-box|xray> <name>", Short: "轮换落地接入 UUID", Args: cobra.ExactArgs(2), RunE: func(cmd *cobra.Command, args []string) error {
		if err := c.requireYes("轮换后使用旧连接文本的中转机将断开"); err != nil {
			return err
		}
		item, err := c.app.RotateLandingAccess(cmd.Context(), args[0], args[1])
		if err == nil {
			fmt.Fprintf(c.out, "落地接入 %s 的 UUID 已轮换为 %s，请重新生成并复制连接文本。\n", args[1], item.UUID)
		}
		return err
	}}
}

func (c *commandSet) landingRemoveCommand() *cobra.Command {
	return &cobra.Command{Use: "remove <sing-box|xray> <name>", Short: "删除落地接入", Args: cobra.ExactArgs(2), RunE: func(cmd *cobra.Command, args []string) error {
		if err := c.requireYes("删除会立即拒绝使用该 UUID 的中转机"); err != nil {
			return err
		}
		if err := c.app.RemoveLandingAccess(cmd.Context(), args[0], args[1]); err != nil {
			return err
		}
		fmt.Fprintf(c.out, "落地接入 %s 已删除。\n", args[1])
		return nil
	}}
}

func (c *commandSet) relayCommand() *cobra.Command {
	cmd := &cobra.Command{Use: "relay", Short: "管理同端口按用户分流的中转线路"}
	cmd.AddCommand(c.relayAddCommand(), c.relayListCommand(), c.relayShowCommand(), c.relayClientCommand(), c.relayUpdateCommand(), c.relayTestCommand(),
		c.relayToggleCommand("enable", true), c.relayToggleCommand("disable", false), c.relayRotateCommand(), c.relayRemoveCommand())
	return cmd
}

func (c *commandSet) relayShowCommand() *cobra.Command {
	return &cobra.Command{Use: "show <sing-box|xray> <name>", Short: "显示中转线路详情（含敏感凭据）", Args: cobra.ExactArgs(2), RunE: func(cmd *cobra.Command, args []string) error {
		items, err := c.app.RelayLinks(args[0])
		if err != nil {
			return err
		}
		for _, item := range items {
			if item.Name != args[1] {
				continue
			}
			b, err := json.MarshalIndent(item, "", "  ")
			if err == nil {
				b = append(b, '\n')
				_, err = c.out.Write(b)
			}
			return err
		}
		return fmt.Errorf("找不到中转线路 %q", args[1])
	}}
}

func (c *commandSet) relayAddCommand() *cobra.Command {
	var upstream string
	var upstreamStdin bool
	var allow bool
	cmd := &cobra.Command{Use: "add <sing-box|xray> <name>", Short: "导入落地连接文本并添加中转线路", Args: cobra.ExactArgs(2), RunE: func(cmd *cobra.Command, args []string) error {
		peer, err := c.readLandingPeerInput(upstream, upstreamStdin)
		if err != nil {
			return err
		}
		link, err := c.app.AddRelayLink(cmd.Context(), args[0], args[1], peer, app.RelayAddOptions{AllowUnreachable: allow})
		if err == nil {
			fmt.Fprintf(c.out, "中转线路 %s 已启用；客户端用户为 %s。\n", link.Name, link.UserName)
		}
		return err
	}}
	cmd.Flags().StringVar(&upstream, "upstream", "", "兼容选项：从落地连接文件读取")
	cmd.Flags().BoolVar(&upstreamStdin, "upstream-stdin", false, "从标准输入读取落地连接文本")
	cmd.Flags().BoolVar(&allow, "allow-unreachable", false, "落地当前不可达时仍保存配置")
	return cmd
}

func (c *commandSet) relayListCommand() *cobra.Command {
	return &cobra.Command{Use: "list <sing-box|xray>", Short: "列出中转线路", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		items, err := c.app.RelayLinks(args[0])
		if err != nil {
			return err
		}
		if len(items) == 0 {
			fmt.Fprintln(c.out, "尚未配置中转线路。")
			return nil
		}
		for _, item := range items {
			fmt.Fprintf(c.out, "%s\t%s\t%s:%d\t%s\t%s\n", item.Name, item.UserName, item.Upstream.Server, item.Upstream.Port,
				domain.NormalizeLandingSecurity(item.Upstream.Security), enabledLabel(item.Enabled))
		}
		return nil
	}}
}

func (c *commandSet) relayClientCommand() *cobra.Command {
	var output, format string
	var force bool
	cmd := &cobra.Command{Use: "client <sing-box|xray> <name>", Short: "导出中转线路客户端配置", Args: cobra.ExactArgs(2), RunE: func(cmd *cobra.Command, args []string) error {
		b, err := c.app.RelayClientConfig(cmd.Context(), args[0], args[1], format, output, force)
		if err != nil {
			return err
		}
		if output == "" {
			_, err = c.out.Write(b)
		} else {
			fmt.Fprintf(c.out, "客户端配置已安全写入 %s（0600）\n", output)
		}
		return err
	}}
	cmd.Flags().StringVar(&format, "format", app.ClientFormatNative, "客户端格式：native 或 clash")
	cmd.Flags().StringVarP(&output, "output", "o", "", "写入文件（默认 stdout）")
	cmd.Flags().BoolVar(&force, "force", false, "覆盖已有输出文件")
	return cmd
}

func (c *commandSet) relayUpdateCommand() *cobra.Command {
	var upstream string
	var upstreamStdin bool
	var allow bool
	cmd := &cobra.Command{Use: "update <sing-box|xray> <name>", Short: "使用新的落地连接文本更新线路", Args: cobra.ExactArgs(2), RunE: func(cmd *cobra.Command, args []string) error {
		peer, err := c.readLandingPeerInput(upstream, upstreamStdin)
		if err != nil {
			return err
		}
		if err = c.app.UpdateRelayLink(cmd.Context(), args[0], args[1], peer, app.RelayAddOptions{AllowUnreachable: allow}); err == nil {
			fmt.Fprintf(c.out, "中转线路 %s 已更新。\n", args[1])
		}
		return err
	}}
	cmd.Flags().StringVar(&upstream, "upstream", "", "兼容选项：从新的落地连接文件读取")
	cmd.Flags().BoolVar(&upstreamStdin, "upstream-stdin", false, "从标准输入读取新的落地连接文本")
	cmd.Flags().BoolVar(&allow, "allow-unreachable", false, "落地当前不可达时仍保存配置")
	return cmd
}

func (c *commandSet) relayTestCommand() *cobra.Command {
	return &cobra.Command{Use: "test <sing-box|xray> <name>", Short: "测试落地 TCP 连通性", Args: cobra.ExactArgs(2), RunE: func(cmd *cobra.Command, args []string) error {
		if err := c.app.TestRelayLink(cmd.Context(), args[0], args[1]); err != nil {
			return err
		}
		fmt.Fprintf(c.out, "中转线路 %s 的落地端点 TCP 可达。\n", args[1])
		return nil
	}}
}

func (c *commandSet) relayToggleCommand(action string, enabled bool) *cobra.Command {
	label := "启用"
	if !enabled {
		label = "停用"
	}
	return &cobra.Command{Use: action + " <sing-box|xray> <name>", Short: label + "中转线路", Args: cobra.ExactArgs(2), RunE: func(cmd *cobra.Command, args []string) error {
		if err := c.app.SetRelayLinkEnabled(cmd.Context(), args[0], args[1], enabled); err != nil {
			return err
		}
		fmt.Fprintf(c.out, "中转线路 %s 已%s。\n", args[1], label)
		return nil
	}}
}

func (c *commandSet) relayRotateCommand() *cobra.Command {
	return &cobra.Command{Use: "rotate <sing-box|xray> <name>", Short: "轮换中转客户端 UUID", Args: cobra.ExactArgs(2), RunE: func(cmd *cobra.Command, args []string) error {
		if err := c.requireYes("轮换后该线路的旧客户端将失效"); err != nil {
			return err
		}
		item, err := c.app.RotateRelayLink(cmd.Context(), args[0], args[1])
		if err == nil {
			fmt.Fprintf(c.out, "中转线路 %s 的客户端 UUID 已轮换为 %s。\n", args[1], item.UUID)
		}
		return err
	}}
}

func (c *commandSet) relayRemoveCommand() *cobra.Command {
	return &cobra.Command{Use: "remove <sing-box|xray> <name>", Short: "删除中转线路", Args: cobra.ExactArgs(2), RunE: func(cmd *cobra.Command, args []string) error {
		if err := c.requireYes("删除会立即使该线路客户端失效"); err != nil {
			return err
		}
		if err := c.app.RemoveRelayLink(cmd.Context(), args[0], args[1]); err != nil {
			return err
		}
		fmt.Fprintf(c.out, "中转线路 %s 已删除。\n", args[1])
		return nil
	}}
}

func (c *commandSet) requireYes(message string) error {
	if c.yes {
		return nil
	}
	return fmt.Errorf("%s；确认后请添加 --yes", message)
}

func enabledLabel(enabled bool) string {
	if enabled {
		return "已启用"
	}
	return "已停用"
}

func (c *commandSet) readLandingPeerInput(path string, fromStdin bool) (domain.LandingPeer, error) {
	path = strings.TrimSpace(path)
	if path != "" && fromStdin {
		return domain.LandingPeer{}, fmt.Errorf("--upstream 和 --upstream-stdin 不能同时使用")
	}
	if fromStdin {
		if c.in == nil {
			return domain.LandingPeer{}, fmt.Errorf("标准输入不可用")
		}
		b, err := io.ReadAll(c.in)
		if err != nil {
			return domain.LandingPeer{}, fmt.Errorf("读取落地连接文本: %w", err)
		}
		return app.ParseLandingBundle(b)
	}
	if path != "" {
		return app.ReadLandingBundle(path)
	}
	return domain.LandingPeer{}, fmt.Errorf("必须提供 --upstream-stdin（推荐）或兼容参数 --upstream")
}

func (c *commandSet) linkMenu(ctx context.Context, core string) error {
	for {
		c.clearScreen()
		c.printPageHeader(core, "中转与落地线路")
		relays, relayErr := c.app.RelayLinks(core)
		landings, landingErr := c.app.LandingAccesses(core)
		if relayErr != nil || landingErr != nil {
			if relayErr != nil {
				return relayErr
			}
			return landingErr
		}
		fmt.Fprintf(c.out, "当前监听端口保持不变；中转线路 %d 条，落地接入 %d 个。\n\n", len(relays), len(landings))
		c.printMenuChoice("1", "添加中转线路（专用用户流量转发到远程落地）")
		c.printMenuChoice("2", "管理中转线路")
		c.printMenuChoice("3", "创建落地接入（允许中转机连接本机）")
		c.printMenuChoice("4", "管理落地接入")
		c.printMenuChoice("5", "查看流量关系")
		c.printMenuChoice("0/q", "返回")
		choice, err := c.chooseNumber("请选择", 0, 5, 0)
		if err != nil || choice == 0 {
			return err
		}
		c.clearScreen()
		switch choice {
		case 1:
			err = c.addRelayInteractive(ctx, core)
		case 2:
			err = c.manageRelayInteractive(ctx, core)
		case 3:
			err = c.addLandingInteractive(ctx, core)
		case 4:
			err = c.manageLandingInteractive(ctx, core)
		case 5:
			err = c.printLinkGraph(core)
		}
		if errors.Is(err, errReturnToMenu) {
			continue
		}
		if err != nil {
			c.printMenuError(err)
		}
		c.pauseForMenu()
	}
}

func (c *commandSet) addLandingInteractive(ctx context.Context, core string) error {
	c.printPageHeader(core, "创建落地接入")
	node, err := c.app.Store.Load(core)
	if err != nil {
		return err
	}
	c.printMenuChoice("1", fmt.Sprintf("使用当前协议（VLESS + RAW + REALITY + Vision，复用端口 %d）", node.Port))
	c.printMenuChoice("2", "创建 VLESS + RAW + TLS + Vision（随机高位独立端口）")
	protocolChoice, err := c.chooseNumberCancelable("请选择落地接入协议", 1, 2, 1)
	if err != nil {
		return err
	}
	name, err := c.askDefaultCancelable("接入名称", "relay-1")
	if err != nil {
		return err
	}
	if err = c.app.ValidateLinkNameAvailable(core, name); err != nil {
		return err
	}
	opts := app.LandingAddOptions{Security: domain.LandingSecurityReality}
	confirmMessage := "将向当前 REALITY 入站添加独立接入用户；不新增端口，普通用户路由保持不变，应用时会重启当前服务。"
	if protocolChoice == 2 {
		opts.Security = domain.LandingSecurityTLS
		randomPort, e := c.app.PickLandingTLSPort(core)
		if e != nil {
			return e
		}
		rawPort, e := c.askDefaultCancelable("独立 TLS 监听端口（已随机选择高位可用端口）", strconv.Itoa(randomPort))
		if e != nil {
			return e
		}
		opts.Port, e = strconv.Atoi(rawPort)
		if e != nil {
			return fmt.Errorf("TLS 落地端口无效: %w", e)
		}
		opts.SNI, e = c.askDefaultCancelable("TLS 证书域名", "")
		if e != nil {
			return e
		}
		defaultCert := "/etc/letsencrypt/live/" + opts.SNI + "/fullchain.pem"
		defaultKey := "/etc/letsencrypt/live/" + opts.SNI + "/privkey.pem"
		opts.CertificateFile, e = c.askDefaultCancelable("TLS 证书链文件", defaultCert)
		if e != nil {
			return e
		}
		opts.KeyFile, e = c.askDefaultCancelable("TLS 私钥文件", defaultKey)
		if e != nil {
			return e
		}
		confirmMessage = fmt.Sprintf("将新增独立 TLS 入站端口 %d；证书须受中转机系统信任，普通用户和当前 REALITY 入站保持不变，应用时会重启当前服务。", opts.Port)
	}
	ok, err := c.confirmCancelable(confirmMessage)
	if err != nil || !ok {
		return errReturnToMenu
	}
	if _, err = c.app.AddLandingAccessWithOptions(ctx, core, name, opts); err != nil {
		return err
	}
	b, err := c.app.ExportLandingBundle(core, name, "", false)
	if err == nil {
		fmt.Fprintln(c.out, "落地接入已创建。请复制下面完整的 JSON 文本，在中转机的粘贴编辑器中使用：")
		fmt.Fprintln(c.out)
		_, err = c.out.Write(b)
	}
	return err
}

func (c *commandSet) addRelayInteractive(ctx context.Context, core string) error {
	c.printPageHeader(core, "添加中转线路")
	c.printMenuChoice("1", "打开临时编辑文件并粘贴连接文本（推荐）")
	c.printMenuChoice("2", "手动输入落地连接信息")
	choice, err := c.chooseNumberCancelable("请选择落地信息来源", 1, 2, 1)
	if err != nil {
		return err
	}
	var peer domain.LandingPeer
	if choice == 1 {
		peer, err = c.pasteLandingBundle()
		if err != nil {
			return err
		}
	} else {
		peer, err = c.askLandingPeer()
		if err != nil {
			return err
		}
	}
	name, err := c.askDefaultCancelable("线路名称", peer.Name)
	if err != nil {
		return err
	}
	if err = c.app.ValidateLinkNameAvailable(core, name); err != nil {
		return err
	}
	fmt.Fprintf(c.out, "\n落地：%s:%d · %s · SNI %s\n", peer.Server, peer.Port, strings.ToUpper(domain.NormalizeLandingSecurity(peer.Security)), peer.SNI)
	ok, err := c.confirmCancelable("该线路使用独立 UUID；原有用户继续 direct。落地不可用时线路将失败，不回退本机出口。")
	if err != nil || !ok {
		return errReturnToMenu
	}
	link, err := c.app.AddRelayLink(ctx, core, name, peer, app.RelayAddOptions{})
	if err != nil && strings.Contains(err.Error(), "落地端点不可达") {
		fmt.Fprintln(c.out, err)
		ok, confirmErr := c.confirmCancelable("落地当前不可达，是否仍保存线路配置？")
		if confirmErr != nil || !ok {
			return errReturnToMenu
		}
		link, err = c.app.AddRelayLink(ctx, core, name, peer, app.RelayAddOptions{AllowUnreachable: true})
	}
	if err == nil {
		fmt.Fprintf(c.out, "中转线路 %s 已启用，客户端用户：%s。\n", link.Name, link.UserName)
	}
	return err
}

func (c *commandSet) pasteLandingBundle() (domain.LandingPeer, error) {
	if c.runEditor == nil && !c.interactiveUI() {
		return domain.LandingPeer{}, fmt.Errorf("粘贴落地连接文本需要交互式终端")
	}
	editor, err := c.findConfigEditor()
	if err != nil {
		return domain.LandingPeer{}, err
	}
	f, err := os.CreateTemp("", "proxyforge-landing-paste-*.json")
	if err != nil {
		return domain.LandingPeer{}, fmt.Errorf("创建临时粘贴文件: %w", err)
	}
	path := f.Name()
	defer os.Remove(path)
	if err = f.Chmod(0600); err == nil {
		err = f.Close()
	} else {
		_ = f.Close()
	}
	if err != nil {
		return domain.LandingPeer{}, err
	}
	fmt.Fprintf(c.out, "将打开 %s。请粘贴落地服务器生成的完整 JSON，保存并退出。\n", filepath.Base(editor))
	if err := c.runConfigEditor(editor, path); err != nil {
		return domain.LandingPeer{}, fmt.Errorf("编辑器退出异常: %w", err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return domain.LandingPeer{}, fmt.Errorf("读取粘贴的落地连接文本: %w", err)
	}
	if strings.TrimSpace(string(b)) == "" {
		return domain.LandingPeer{}, fmt.Errorf("没有粘贴落地连接文本")
	}
	peer, err := app.ParseLandingBundle(b)
	if err != nil {
		return domain.LandingPeer{}, fmt.Errorf("粘贴的落地连接文本无效: %w", err)
	}
	fmt.Fprintln(c.out, "连接文本解析成功，临时文件将在导入后自动删除。")
	return peer, nil
}

func (c *commandSet) askLandingPeer() (domain.LandingPeer, error) {
	var peer domain.LandingPeer
	var err error
	peer.Name, err = c.askDefaultCancelable("落地接入名称", "landing")
	if err != nil {
		return peer, err
	}
	c.printMenuChoice("1", "Xray-core")
	c.printMenuChoice("2", "sing-box")
	coreChoice, err := c.chooseNumberCancelable("请选择落地内核", 1, 2, 1)
	if err != nil {
		return peer, err
	}
	peer.Core = domain.CoreXray
	if coreChoice == 2 {
		peer.Core = domain.CoreSingBox
	}
	c.printMenuChoice("1", "VLESS + RAW + REALITY + Vision")
	c.printMenuChoice("2", "VLESS + RAW + TLS + Vision")
	securityChoice, err := c.chooseNumberCancelable("请选择落地安全协议", 1, 2, 1)
	if err != nil {
		return peer, err
	}
	peer.Security = domain.LandingSecurityReality
	if securityChoice == 2 {
		peer.Security = domain.LandingSecurityTLS
	}
	peer.Server, err = c.askDefaultCancelable("落地公网 IP 或域名", "")
	if err != nil {
		return peer, err
	}
	rawPort, err := c.askDefaultCancelable("落地端口", "443")
	if err != nil {
		return peer, err
	}
	peer.Port, err = strconv.Atoi(rawPort)
	if err != nil {
		return peer, fmt.Errorf("落地端口无效: %w", err)
	}
	sniLabel := "落地 REALITY SNI"
	if peer.Security == domain.LandingSecurityTLS {
		sniLabel = "落地 TLS 证书域名"
	}
	peer.SNI, err = c.askDefaultCancelable(sniLabel, "")
	if err != nil {
		return peer, err
	}
	peer.UUID, err = c.askDefaultCancelable("落地接入 UUID", "")
	if err != nil {
		return peer, err
	}
	if peer.Security == domain.LandingSecurityReality {
		peer.PublicKey, err = c.askDefaultCancelable("落地 REALITY 公钥", "")
		if err != nil {
			return peer, err
		}
		peer.ShortID, err = c.askDefaultCancelable("落地 short ID", "")
		if err != nil {
			return peer, err
		}
	}
	peer.Flow = domain.VisionFlow
	return peer, nil
}

func (c *commandSet) manageRelayInteractive(ctx context.Context, core string) error {
	items, err := c.app.RelayLinks(core)
	if err != nil {
		return err
	}
	if len(items) == 0 {
		fmt.Fprintln(c.out, "尚未配置中转线路。")
		return nil
	}
	c.printPageHeader(core, "管理中转线路")
	for i, item := range items {
		title := fmt.Sprintf("%s · %s:%d", item.Name, item.Upstream.Server, item.Upstream.Port)
		c.printMenuBadgeChoice(strconv.Itoa(i+1), title, "["+enabledLabel(item.Enabled)+"]")
	}
	c.printMenuChoice("0/q", "返回")
	selectedNumber, err := c.chooseNumber("请选择中转线路", 0, len(items), 1)
	if err != nil {
		return err
	}
	if selectedNumber == 0 {
		return errReturnToMenu
	}
	selected := &items[selectedNumber-1]
	name := selected.Name
	c.clearScreen()
	c.printPageHeader(core, "管理中转线路", name)
	fmt.Fprintf(c.out, "当前落地：%s:%d · %s · %s\n\n", selected.Upstream.Server, selected.Upstream.Port,
		strings.ToUpper(domain.NormalizeLandingSecurity(selected.Upstream.Security)), enabledLabel(selected.Enabled))
	c.printMenuChoice("1", "导出原生客户端配置")
	c.printMenuChoice("2", "测试落地 TCP 连通性")
	c.printMenuChoice("3", "粘贴新的落地连接文本")
	c.printMenuChoice("4", "轮换中转客户端 UUID")
	if selected.Enabled {
		c.printMenuChoice("5", "停用线路")
	} else {
		c.printMenuChoice("5", "启用线路")
	}
	c.printMenuChoice("6", "删除线路")
	c.printMenuChoice("0/q", "返回")
	choice, err := c.chooseNumber("请选择", 0, 6, 0)
	if err != nil || choice == 0 {
		return errReturnToMenu
	}
	switch choice {
	case 1:
		c.printMenuChoice("1", "原生 JSON")
		c.printMenuChoice("2", "Clash/Mihomo YAML")
		formatChoice, e := c.chooseNumberCancelable("请选择客户端格式", 1, 2, 1)
		if e != nil {
			return e
		}
		format := app.ClientFormatNative
		if formatChoice == 2 {
			format = app.ClientFormatClash
		}
		b, e := c.app.RelayClientConfig(ctx, core, name, format, "", false)
		if e == nil {
			_, e = c.out.Write(b)
		}
		return e
	case 2:
		if e := c.app.TestRelayLink(ctx, core, name); e != nil {
			return e
		}
		fmt.Fprintln(c.out, "落地端点 TCP 可达。")
		return nil
	case 3:
		peer, e := c.pasteLandingBundle()
		if e != nil {
			return e
		}
		return c.app.UpdateRelayLink(ctx, core, name, peer, app.RelayAddOptions{})
	case 4:
		ok, e := c.confirmCancelable("轮换后该线路的旧客户端会立即失效。")
		if e != nil || !ok {
			return errReturnToMenu
		}
		_, e = c.app.RotateRelayLink(ctx, core, name)
		return e
	case 5:
		return c.app.SetRelayLinkEnabled(ctx, core, name, !selected.Enabled)
	case 6:
		ok, e := c.confirmCancelable("删除会移除该用户、路由和落地出站，旧客户端立即失效。")
		if e != nil || !ok {
			return errReturnToMenu
		}
		return c.app.RemoveRelayLink(ctx, core, name)
	}
	return nil
}

func (c *commandSet) manageLandingInteractive(ctx context.Context, core string) error {
	items, err := c.app.LandingAccesses(core)
	if err != nil {
		return err
	}
	if len(items) == 0 {
		fmt.Fprintln(c.out, "尚未配置落地接入。")
		return nil
	}
	c.printPageHeader(core, "管理落地接入")
	for i, item := range items {
		title := item.Name
		if item.UserName != "" && item.UserName != item.Name {
			title += " · " + item.UserName
		}
		if domain.NormalizeLandingSecurity(item.Security) == domain.LandingSecurityTLS {
			title += fmt.Sprintf(" · TLS :%d", item.Port)
		} else {
			title += " · 当前 REALITY"
		}
		c.printMenuBadgeChoice(strconv.Itoa(i+1), title, "["+enabledLabel(item.Enabled)+"]")
	}
	c.printMenuChoice("0/q", "返回")
	selectedNumber, err := c.chooseNumber("请选择落地接入", 0, len(items), 1)
	if err != nil {
		return err
	}
	if selectedNumber == 0 {
		return errReturnToMenu
	}
	selected := &items[selectedNumber-1]
	name := selected.Name
	c.clearScreen()
	c.printPageHeader(core, "管理落地接入", name)
	security := domain.NormalizeLandingSecurity(selected.Security)
	endpoint := "复用当前 REALITY 入站"
	if security == domain.LandingSecurityTLS {
		endpoint = fmt.Sprintf("独立 TLS 端口 %d · %s", selected.Port, selected.SNI)
	}
	fmt.Fprintf(c.out, "当前状态：%s · %s\n\n", enabledLabel(selected.Enabled), endpoint)
	c.printMenuChoice("1", "显示可复制的落地连接文本")
	c.printMenuChoice("2", "轮换接入 UUID")
	if selected.Enabled {
		c.printMenuChoice("3", "停用接入")
	} else {
		c.printMenuChoice("3", "启用接入")
	}
	c.printMenuChoice("4", "删除接入")
	c.printMenuChoice("0/q", "返回")
	choice, err := c.chooseNumber("请选择", 0, 4, 0)
	if err != nil || choice == 0 {
		return errReturnToMenu
	}
	switch choice {
	case 1:
		b, e := c.app.ExportLandingBundle(core, name, "", false)
		if e == nil {
			fmt.Fprintln(c.out, "请复制下面完整的 JSON 文本：")
			fmt.Fprintln(c.out)
			_, e = c.out.Write(b)
		}
		return e
	case 2:
		ok, e := c.confirmCancelable("轮换后使用旧连接文本的所有中转机都会断开。")
		if e != nil || !ok {
			return errReturnToMenu
		}
		_, e = c.app.RotateLandingAccess(ctx, core, name)
		return e
	case 3:
		return c.app.SetLandingAccessEnabled(ctx, core, name, !selected.Enabled)
	case 4:
		ok, e := c.confirmCancelable("删除后使用该 UUID 的中转机会立即断开。")
		if e != nil || !ok {
			return errReturnToMenu
		}
		return c.app.RemoveLandingAccess(ctx, core, name)
	}
	return nil
}

func (c *commandSet) printLinkGraph(core string) error {
	relays, err := c.app.RelayLinks(core)
	if err != nil {
		return err
	}
	landings, err := c.app.LandingAccesses(core)
	if err != nil {
		return err
	}
	c.printPageHeader(core, "流量关系")
	fmt.Fprintln(c.out, "普通用户\n  └─ direct（本机出口）")
	for _, link := range relays {
		fmt.Fprintf(c.out, "中转用户 %s [%s]\n  └─ %s:%d · %s\n", link.UserName, enabledLabel(link.Enabled), link.Upstream.Server, link.Upstream.Port,
			strings.ToUpper(domain.NormalizeLandingSecurity(link.Upstream.Security)))
	}
	for _, access := range landings {
		protocol := "REALITY（当前端口）"
		if domain.NormalizeLandingSecurity(access.Security) == domain.LandingSecurityTLS {
			protocol = fmt.Sprintf("TLS :%d", access.Port)
		}
		fmt.Fprintf(c.out, "落地接入 %s [%s · %s]\n  └─ direct（本机出口）\n", access.UserName, enabledLabel(access.Enabled), protocol)
	}
	return nil
}
