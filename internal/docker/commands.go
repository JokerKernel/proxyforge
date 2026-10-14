package docker

import (
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/spf13/cobra"
	"proxyforge/internal/docker/model"
)

func (m *Manager) Command() *cobra.Command {
	var yes bool
	cmd := &cobra.Command{Use: "docker", Short: "独立管理 Xray Docker Compose 实例", SilenceUsage: true, SilenceErrors: true, PersistentPreRunE: func(cmd *cobra.Command, _ []string) error {
		m.out = cmd.OutOrStdout()
		m.errOut = cmd.ErrOrStderr()
		return m.rootCheck()
	}, RunE: func(cmd *cobra.Command, _ []string) error { return m.Menu(cmd.Context(), cmd.InOrStdin()) }}
	cmd.PersistentFlags().BoolVarP(&yes, "yes", "y", false, "确认 Docker 配置覆盖、容器移除和残留清理")
	for _, action := range []string{"prepare", "upgrade"} {
		action := action
		var version string
		sub := &cobra.Command{Use: action, Short: map[string]string{"prepare": "准备官方镜像和 Compose，不启动代理", "upgrade": "升级镜像，保留凭据并支持回滚"}[action], Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
			if action == "prepare" {
				return m.Prepare(cmd.Context(), version)
			}
			return m.Upgrade(cmd.Context(), version)
		}}
		sub.Flags().StringVar(&version, "version", "", "stable（缺省最新稳定版）、latest（官方镜像标签）或具体版本号")
		cmd.AddCommand(sub)
	}
	deploy := m.generateCommand("deploy", &yes)
	var version string
	deploy.Flags().StringVar(&version, "version", "", "首次部署镜像：stable（缺省）、latest 或具体版本号")
	runGenerate := deploy.RunE
	deploy.RunE = func(cmd *cobra.Command, args []string) error {
		if _, err := m.metadata(); os.IsNotExist(err) {
			if err = m.Prepare(cmd.Context(), version); err != nil {
				return err
			}
		} else if err != nil {
			return err
		} else if version != "" {
			md, err := m.metadata()
			if err != nil {
				return err
			}
			if err = checkPreparedVersion(md, version); err != nil {
				return err
			}
		}
		return runGenerate(cmd, args)
	}
	cmd.AddCommand(deploy)
	cmd.AddCommand(&cobra.Command{Use: "status", Short: "查看 Docker 实例状态", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		s, err := m.Status(cmd.Context())
		b, e := jsonBytes(s)
		if e != nil {
			return e
		}
		fmt.Fprint(m.out, string(b))
		return err
	}})
	for _, action := range []string{"remove", "cleanup", "recover"} {
		action := action
		cmd.AddCommand(&cobra.Command{Use: action, Short: map[string]string{"remove": "移除容器，保留配置和数据", "cleanup": "容器移除后清理本模块数据", "recover": "恢复未完成事务"}[action], Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
			if action != "recover" {
				if err := m.confirmInput(cmd.InOrStdin(), yes, "确认执行 Docker "+action+"？"); err != nil {
					return err
				}
			}
			switch action {
			case "remove":
				return m.Remove(cmd.Context())
			case "cleanup":
				return m.Cleanup(cmd.Context())
			default:
				return m.Recover(cmd.Context())
			}
		}})
	}
	service := &cobra.Command{Use: "service <start|stop|restart|status|logs>", Short: "管理 Docker Xray 服务", Args: cobra.ExactArgs(1)}
	var follow bool
	service.Flags().BoolVarP(&follow, "follow", "f", false, "持续查看日志")
	service.RunE = func(cmd *cobra.Command, args []string) error {
		if follow && args[0] != "logs" {
			return fmt.Errorf("--follow 仅用于日志")
		}
		if args[0] == "status" {
			s, err := m.Status(cmd.Context())
			b, _ := jsonBytes(s)
			fmt.Fprint(m.out, string(b))
			return err
		}
		if follow {
			return m.FollowLogs(cmd.Context())
		}
		b, err := m.Service(cmd.Context(), args[0])
		fmt.Fprint(m.out, string(b))
		return err
	}
	cmd.AddCommand(service)
	config := &cobra.Command{Use: "config", Short: "独立管理 Docker 服务端和客户端配置"}
	config.AddCommand(m.generateCommand("generate", &yes))
	config.AddCommand(&cobra.Command{Use: "show", Short: "显示含敏感凭据的服务端配置", Args: cobra.NoArgs, RunE: func(*cobra.Command, []string) error {
		b, err := m.ServerConfig()
		if err != nil {
			return err
		}
		fmt.Fprintln(m.errOut, "[Docker] 当前配置包含 UUID 和私钥，请妥善保管")
		fmt.Fprint(m.out, string(b))
		return nil
	}})
	config.AddCommand(&cobra.Command{Use: "validate", Short: "使用固定镜像校验正式配置", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error { return m.Validate(cmd.Context()) }})
	config.AddCommand(&cobra.Command{Use: "edit", Short: "编辑配置副本并事务应用", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error { return m.Edit(cmd.Context(), cmd.InOrStdin()) }})
	settings := &cobra.Command{Use: "set <dns|log|outbound-ip|fallback-ip> <value>", Short: "修改 DNS、日志或地址族策略", Args: cobra.ExactArgs(2), RunE: func(cmd *cobra.Command, args []string) error { return m.PatchSetting(cmd.Context(), args[0], args[1]) }}
	config.AddCommand(settings)
	var reset model.ResetOptions
	resetCmd := &cobra.Command{Use: "reset", Short: "轮换主节点凭据，按需轮换 TLS 证书", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		if err := m.confirmInput(cmd.InOrStdin(), yes, "重置凭据会使原客户端失效，确认继续？"); err != nil {
			return err
		}
		return m.Reset(cmd.Context(), reset)
	}}
	resetCmd.Flags().BoolVar(&reset.RotateCertificate, "rotate-certificate", false, "轮换自签 TLS 证书")
	resetCmd.Flags().BoolVar(&reset.Credentials, "credentials", false, "轮换证书时同时轮换 UUID；普通 reset 默认轮换凭据")
	resetCmd.Flags().StringVar(&reset.SNI, "sni", "", "更新 REALITY SNI")
	resetCmd.Flags().StringVar(&reset.Target, "target", "", "更新 REALITY target")
	config.AddCommand(resetCmd)
	var format, name, path string
	var force bool
	client := &cobra.Command{Use: "client", Short: "导出普通、落地或中转客户端配置", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		b, err := m.Client(cmd.Context(), format, name)
		if err != nil {
			return err
		}
		if path != "" {
			return m.WriteExport(path, b, force)
		}
		fmt.Fprint(m.out, string(b))
		return nil
	}}
	client.Flags().StringVar(&format, "format", "native", "native 或 clash")
	client.Flags().StringVar(&name, "name", "", "落地/中转线路名称，空值为主节点")
	client.Flags().StringVar(&path, "output", "", "导出文件，空值输出到 stdout")
	client.Flags().BoolVar(&force, "force", false, "覆盖导出文件")
	config.AddCommand(client, m.accessCommand(), m.landingCommand(), m.relayCommand())
	cmd.AddCommand(config)
	bindCancellation(cmd)
	return cmd
}

func bindCancellation(cmd *cobra.Command) {
	for _, child := range cmd.Commands() {
		bindCancellation(child)
	}
	if cmd.RunE == nil {
		return
	}
	run := cmd.RunE
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		parent := cmd.Context()
		ctx, stop := signal.NotifyContext(parent, os.Interrupt, syscall.SIGTERM)
		defer stop()
		defer cmd.SetContext(parent)
		cmd.SetContext(ctx)
		return run(cmd, args)
	}
}
func (m *Manager) generateCommand(use string, yes *bool) *cobra.Command {
	var opts model.GenerateOptions
	var force bool
	cmd := &cobra.Command{Use: use, Short: "生成 REALITY/TLS 配置，保留现有凭据与线路", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		if _, err := os.Stat(m.paths.Config()); err == nil && !force {
			if err = m.confirmInput(cmd.InOrStdin(), *yes, "完整生成会覆盖手动配置，确认继续？"); err != nil {
				return err
			}
		}
		if opts.Server == "" && terminalInput(cmd.InOrStdin()) {
			var err error
			opts, err = m.askGenerate(cmd.InOrStdin(), opts)
			if err != nil {
				return err
			}
		}
		return m.Generate(cmd.Context(), opts, use == "deploy")
	}}
	flags := cmd.Flags()
	flags.StringVar(&opts.Security, "security", "", "reality 或 tls，缺省沿用当前协议")
	flags.StringVar(&opts.Server, "server", "", "对外服务器地址")
	flags.IntVar(&opts.Port, "port", 0, "监听端口，首次缺省 443")
	flags.StringVar(&opts.SNI, "sni", "", "REALITY SNI 域名")
	flags.StringVar(&opts.Target, "target", "", "REALITY 回落目标 host:port")
	flags.StringVar(&opts.UserName, "user-name", "", "主节点用户名")
	flags.StringVar(&opts.InboundTag, "inbound-tag", "", "入站标签")
	flags.BoolVar(&opts.StandardConfig, "standard-config", false, "关闭默认回落防护")
	flags.IntVar(&opts.XrayFallbackPort, "fallback-port", 0, "回落防护内部端口")
	flags.BoolVar(&opts.XrayFallbackHTTPDomain, "fallback-http-domain", false, "限制回落 HTTP Host")
	flags.BoolVar(&opts.XrayFallbackExactDomain, "fallback-exact-domain", false, "严格匹配回落域名")
	flags.BoolVar(&opts.RotateCredentials, "rotate-credentials", false, "轮换 UUID 和 REALITY 密钥")
	flags.BoolVar(&opts.RotateCertificate, "rotate-certificate", false, "轮换 TLS 证书")
	flags.BoolVar(&opts.DropLinks, "drop-links", false, "丢弃已有落地/中转线路")
	flags.BoolVar(&force, "force", false, "确认完整覆盖服务端配置")
	return cmd
}
func (m *Manager) accessCommand() *cobra.Command {
	var exact bool
	cmd := &cobra.Command{Use: "access <list|mode|switch|add|remove|clear> [args]", Short: "白名单、黑名单和访问模式", Args: cobra.MinimumNArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		s, _, err := m.managedConfig()
		if err != nil {
			return err
		}
		policy := s.Node.AccessPolicy
		switch args[0] {
		case "list":
			if len(args) != 1 {
				return fmt.Errorf("list 不接受参数")
			}
			b, _ := jsonBytes(policy)
			fmt.Fprint(m.out, string(b))
			return nil
		case "mode":
			if len(args) != 2 {
				return fmt.Errorf("mode <default|whitelist>")
			}
			policy.Mode = args[1]
		case "switch":
			if len(args) != 2 || args[1] != "on" && args[1] != "off" {
				return fmt.Errorf("switch <on|off>")
			}
			policy.Disabled = args[1] == "off"
		case "clear":
			if len(args) != 2 {
				return fmt.Errorf("clear <white|black>")
			}
			policy, err = policy.ClearList(args[1])
			if err != nil {
				return err
			}
		case "add", "remove":
			if len(args) < 3 {
				return fmt.Errorf("add/remove <white|black> <域名/IP/CIDR...>")
			}
			entries, err := model.ParseAccessEntries(strings.Join(args[2:], " "), exact)
			if err != nil {
				return err
			}
			var list *[]model.AccessEntry
			switch args[1] {
			case "white":
				list = &policy.Whitelist
			case "black":
				list = &policy.Blacklist
			default:
				return fmt.Errorf("名单类型无效")
			}
			for _, e := range entries {
				if args[0] == "add" {
					*list = append(*list, e)
				} else {
					filtered := (*list)[:0]
					for _, v := range *list {
						if v != e {
							filtered = append(filtered, v)
						}
					}
					*list = filtered
				}
			}
		default:
			return fmt.Errorf("访问策略操作无效")
		}
		return m.SetPolicy(cmd.Context(), policy)
	}}
	cmd.Flags().BoolVar(&exact, "exact", false, "域名精确匹配")
	return cmd
}
func (m *Manager) landingCommand() *cobra.Command {
	var security string
	var port int
	var output string
	cmd := &cobra.Command{Use: "landing <list|add|peer|enable|disable|remove> [name]", Short: "管理落地接入", Args: cobra.RangeArgs(1, 2), RunE: func(cmd *cobra.Command, args []string) error {
		if args[0] == "list" {
			if len(args) != 1 {
				return fmt.Errorf("list 不接受名称")
			}
			s, _, err := m.managedConfig()
			if err != nil {
				return err
			}
			b, _ := jsonBytes(s.Node.LandingAccesses)
			fmt.Fprint(m.out, string(b))
			return nil
		}
		if len(args) != 2 {
			return fmt.Errorf("需要接入名称")
		}
		var peer model.LandingPeer
		var err error
		switch args[0] {
		case "add":
			peer, err = m.AddLanding(cmd.Context(), args[1], security, port)
		case "peer":
			peer, err = m.Peer(args[1])
		case "enable", "disable", "remove":
			return m.ChangeLink(cmd.Context(), "landing", args[1], args[0])
		default:
			return fmt.Errorf("落地操作无效")
		}
		if err != nil {
			return err
		}
		b, _ := jsonBytes(peer)
		if output != "" {
			return m.WriteExport(output, b, false)
		}
		fmt.Fprint(m.out, string(b))
		return nil
	}}
	cmd.Flags().StringVar(&security, "security", "reality", "reality 或 tls")
	cmd.Flags().IntVar(&port, "port", 0, "独立 TLS 接入端口")
	cmd.Flags().StringVar(&output, "output", "", "peer JSON 导出路径")
	return cmd
}
func (m *Manager) relayCommand() *cobra.Command {
	var peerFile string
	cmd := &cobra.Command{Use: "relay <list|add|enable|disable|remove> [name]", Short: "管理中转线路", Args: cobra.RangeArgs(1, 2), RunE: func(cmd *cobra.Command, args []string) error {
		if args[0] == "list" {
			if len(args) != 1 {
				return fmt.Errorf("list 不接受名称")
			}
			s, _, err := m.managedConfig()
			if err != nil {
				return err
			}
			b, _ := jsonBytes(s.Node.RelayLinks)
			fmt.Fprint(m.out, string(b))
			return nil
		}
		if len(args) != 2 {
			return fmt.Errorf("需要线路名称")
		}
		switch args[0] {
		case "add":
			if peerFile == "" {
				return fmt.Errorf("需要 --peer-file")
			}
			absolute, err := filepath.Abs(peerFile)
			if err != nil {
				return err
			}
			var peer model.LandingPeer
			if err = loadJSON(absolute, &peer); err != nil {
				return err
			}
			return m.AddRelay(cmd.Context(), args[1], peer)
		case "enable", "disable", "remove":
			return m.ChangeLink(cmd.Context(), "relay", args[1], args[0])
		default:
			return fmt.Errorf("中转操作无效")
		}
	}}
	cmd.Flags().StringVar(&peerFile, "peer-file", "", "落地接入导出的 0600 peer JSON")
	return cmd
}
