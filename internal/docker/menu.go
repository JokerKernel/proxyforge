package docker

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"proxyforge/internal/docker/model"
)

var errCancelled = errors.New("用户取消")

type ui struct {
	reader *bufio.Reader
	out    io.Writer
}

func newUI(input io.Reader, out io.Writer) *ui {
	r, ok := input.(*bufio.Reader)
	if !ok {
		r = bufio.NewReader(input)
	}
	return &ui{reader: r, out: out}
}
func (u *ui) ask(label, def string) (string, error) {
	if def != "" {
		fmt.Fprintf(u.out, "%s [%s]：", label, def)
	} else {
		fmt.Fprintf(u.out, "%s：", label)
	}
	line, err := u.reader.ReadString('\n')
	if err != nil && len(line) == 0 {
		return "", err
	}
	line = strings.TrimSpace(line)
	if strings.EqualFold(line, "q") {
		return "", errCancelled
	}
	if line == "" {
		line = def
	}
	return line, nil
}
func (u *ui) choose(max int) (int, error) {
	for {
		value, err := u.ask("❯ 请选择（0/q 返回）", "0")
		if errors.Is(err, errCancelled) {
			return 0, nil
		}
		if err != nil {
			return 0, err
		}
		n, err := strconv.Atoi(value)
		if err == nil && n >= 0 && n <= max {
			return n, nil
		}
		fmt.Fprintln(u.out, "无效选择")
	}
}
func (u *ui) confirm(text string) error {
	answer, err := u.ask(text+" [y/1 确认，其他取消]", "")
	if err != nil {
		return err
	}
	if answer == "1" || strings.EqualFold(answer, "y") || strings.EqualFold(answer, "yes") {
		return nil
	}
	return errCancelled
}
func terminalInput(r io.Reader) bool {
	f, ok := r.(*os.File)
	if !ok {
		return false
	}
	info, err := f.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}
func (m *Manager) confirmInput(input io.Reader, yes bool, text string) error {
	if yes {
		return nil
	}
	if !terminalInput(input) {
		return fmt.Errorf("非交互操作必须显式提供 --yes 或 --force")
	}
	return newUI(input, m.out).confirm(text)
}
func (m *Manager) askGenerate(input io.Reader, opts model.GenerateOptions) (model.GenerateOptions, error) {
	u := newUI(input, m.out)
	current, _ := m.LoadState()
	n := current.Node
	var err error
	if opts.Security == "" {
		opts.Security, err = u.ask("协议 reality/tls", model.NormalizeLandingSecurity(n.Security))
		if err != nil {
			return opts, err
		}
	}
	if opts.Server == "" {
		opts.Server, err = u.ask("服务器公网 IP/域名", n.Server)
		if err != nil {
			return opts, err
		}
	}
	if opts.Port == 0 {
		def := n.Port
		if def == 0 {
			def = 443
		}
		value, err := u.ask("监听端口", strconv.Itoa(def))
		if err != nil {
			return opts, err
		}
		opts.Port, err = strconv.Atoi(value)
		if err != nil || opts.Port < 1 || opts.Port > 65535 {
			return opts, fmt.Errorf("端口无效")
		}
	}
	if opts.Security != "tls" {
		if opts.SNI == "" {
			opts.SNI, err = u.ask("REALITY SNI", n.SNI)
			if err != nil {
				return opts, err
			}
		}
		if opts.Target == "" {
			def := n.Target
			if def == "" {
				def = opts.SNI + ":443"
			}
			opts.Target, err = u.ask("REALITY target", def)
			if err != nil {
				return opts, err
			}
		}
	}
	return opts, nil
}
func (m *Manager) Menu(ctx context.Context, input io.Reader) error {
	if err := m.rootCheck(); err != nil {
		return err
	}
	u := newUI(input, m.out)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		statusCtx, cancel := context.WithTimeout(ctx, 4*time.Second)
		s, _ := m.Status(statusCtx)
		cancel()
		fmt.Fprintf(m.out, "\n╭─ Xray（Docker）\n│ 容器 %s  [%s]\n", Container, s.Label)
		if s.Version != "" {
			fmt.Fprintf(m.out, "│ %s\n", s.Version)
		}
		if s.Detail != "" {
			fmt.Fprintf(m.out, "│ %s\n", s.Detail)
		}
		fmt.Fprintln(m.out, "╰──────────────────────────────────────────────\n\n  1   部署/升级\n  2   服务端配置\n  3   客户端配置\n  4   服务管理\n  5   移除容器（保留配置和数据）\n  6   清理卸载残留\n  7   恢复未完成事务\n  0/q 返回")
		choice, err := u.choose(7)
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		if choice == 0 {
			return nil
		}
		switch choice {
		case 1:
			var version string
			version, err = u.ask("镜像版本（stable 为最新稳定版）", "stable")
			if err == nil {
				if version == "stable" {
					version = ""
				}
				if _, e := m.metadata(); e == nil {
					if err = u.confirm("升级 Xray Docker 镜像？"); err == nil {
						err = m.Upgrade(ctx, version)
						if _, stateErr := m.LoadState(); err == nil && os.IsNotExist(stateErr) {
							var opts model.GenerateOptions
							opts, err = m.askGenerate(u.reader, opts)
							if err == nil {
								err = m.Generate(ctx, opts, true)
							}
						}
					}
				} else if !os.IsNotExist(e) {
					err = e
				} else {
					if err = u.confirm("准备官方镜像并部署 Docker Xray？"); err == nil {
						if err = m.Prepare(ctx, version); err == nil {
							var opts model.GenerateOptions
							opts, err = m.askGenerate(u.reader, opts)
							if err == nil {
								err = m.Generate(ctx, opts, true)
							}
						}
					}
				}
			}
		case 2:
			err = m.configMenu(ctx, u)
		case 3:
			var format, name string
			format, err = u.ask("导出格式 native/clash", "native")
			if err == nil {
				name, err = u.ask("线路名称（空值为主节点）", "")
			}
			if err == nil {
				var b []byte
				b, err = m.Client(ctx, format, name)
				if err == nil {
					fmt.Fprint(m.out, string(b))
				}
			}
		case 4:
			err = m.serviceMenu(ctx, u)
		case 5:
			if err = u.confirm("移除本实例容器，保留配置和数据？"); err == nil {
				err = m.Remove(ctx)
			}
		case 6:
			if err = u.confirm("永久删除 Docker Xray 配置、凭据、证书和备份？"); err == nil {
				err = m.Cleanup(ctx)
			}
		case 7:
			err = m.Recover(ctx)
		}
		if errors.Is(err, io.EOF) {
			return nil
		}
		if errors.Is(err, errCancelled) {
			fmt.Fprintln(m.out, "已取消")
		} else if err != nil {
			fmt.Fprintf(m.errOut, "[Docker/错误] %v\n", err)
		}
	}
}
func (m *Manager) configMenu(ctx context.Context, u *ui) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		fmt.Fprintln(m.out, "\nDocker 服务端配置\n  1  生成/更新配置\n  2  查看配置（含凭据）\n  3  编辑配置副本\n  4  校验配置\n  5  DNS/日志/出站设置\n  6  重置节点凭据\n  7  访问控制\n  8  落地接入\n  9  中转线路\n  0/q 返回")
		choice, err := u.choose(9)
		if err != nil {
			return err
		}
		if choice == 0 {
			return nil
		}
		switch choice {
		case 1:
			if _, e := os.Stat(m.paths.Config()); e == nil {
				err = u.confirm("完整生成会覆盖手动配置，确认继续？")
			}
			if err == nil {
				var opts model.GenerateOptions
				opts, err = m.askGenerate(u.reader, opts)
				if err == nil {
					err = m.Generate(ctx, opts, false)
				}
			}
		case 2:
			var b []byte
			b, err = m.ServerConfig()
			if err == nil {
				fmt.Fprintln(m.out, "配置包含 UUID 和私钥，请妥善保管")
				fmt.Fprint(m.out, string(b))
			}
		case 3:
			err = m.Edit(ctx, u.reader)
		case 4:
			err = m.Validate(ctx)
			if err == nil {
				fmt.Fprintln(m.out, "配置校验通过")
			}
		case 5:
			var setting, value string
			setting, err = u.ask("设置 dns/log/outbound-ip/fallback-ip", "")
			if err == nil {
				fmt.Fprintln(m.out, "DNS: system/public-cloudflare/public-google/doh-cloudflare/doh-google；日志: none/error/warning/info/debug；地址族: prefer-ipv4/prefer-ipv6/ipv4-only/ipv6-only/unset")
				value, err = u.ask("设置值", "")
			}
			if err == nil {
				err = m.PatchSetting(ctx, setting, value)
			}
		case 6:
			if err = u.confirm("轮换凭据会使现有客户端失效，确认继续？"); err == nil {
				err = m.Reset(ctx, model.ResetOptions{})
			}
		case 7:
			err = m.accessMenu(ctx, u)
		case 8:
			err = m.linkMenu(ctx, u, "landing")
		case 9:
			err = m.linkMenu(ctx, u, "relay")
		}
		if errors.Is(err, io.EOF) {
			return err
		}
		if errors.Is(err, errCancelled) {
			fmt.Fprintln(m.out, "已取消")
		} else if err != nil {
			fmt.Fprintf(m.errOut, "[Docker/错误] %v\n", err)
		}
	}
}
func (m *Manager) serviceMenu(ctx context.Context, u *ui) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		fmt.Fprintln(m.out, "\nDocker 服务管理\n  1  启动\n  2  停止\n  3  重启\n  4  状态\n  5  日志\n  6  实时日志（Ctrl+C 返回）\n  0/q 返回")
		n, err := u.choose(6)
		if err != nil {
			return err
		}
		if n == 0 {
			return nil
		}
		if n == 4 {
			s, e := m.Status(ctx)
			b, _ := jsonBytes(s)
			fmt.Fprint(m.out, string(b))
			err = e
		} else if n == 6 {
			followCtx, stop := signal.NotifyContext(ctx, os.Interrupt)
			err = m.FollowLogs(followCtx)
			if followCtx.Err() != nil && ctx.Err() == nil {
				err = nil
			}
			stop()
		} else {
			action := map[int]string{1: "start", 2: "stop", 3: "restart", 5: "logs"}[n]
			b, e := m.Service(ctx, action)
			fmt.Fprint(m.out, string(b))
			err = e
		}
		if err != nil {
			fmt.Fprintf(m.errOut, "[Docker/错误] %v\n", err)
		}
	}
}
func (m *Manager) accessMenu(ctx context.Context, u *ui) error {
	s, _, err := m.managedConfig()
	if err != nil {
		return err
	}
	b, _ := jsonBytes(s.Node.AccessPolicy)
	fmt.Fprint(m.out, string(b))
	fmt.Fprintln(m.out, "  1 添加白名单\n  2 添加黑名单\n  3 移除白名单\n  4 移除黑名单\n  5 切换模式\n  6 启用/禁用访问策略\n  7 清空白名单\n  8 清空黑名单\n  0/q 返回")
	choice, err := u.choose(8)
	if err != nil || choice == 0 {
		return err
	}
	p := s.Node.AccessPolicy
	if choice <= 4 {
		v, e := u.ask("域名/IP/CIDR（可空格分隔多个）", "")
		if e != nil {
			return e
		}
		match, e := u.ask("域名匹配 domain/exact", "domain")
		if e != nil {
			return e
		}
		entries, e := model.ParseAccessEntries(v, match == "exact")
		if e != nil {
			return e
		}
		list := &p.Whitelist
		if choice == 2 || choice == 4 {
			list = &p.Blacklist
		}
		for _, entry := range entries {
			if choice <= 2 {
				*list = append(*list, entry)
			} else {
				filtered := (*list)[:0]
				for _, value := range *list {
					if value != entry {
						filtered = append(filtered, value)
					}
				}
				*list = filtered
			}
		}
	} else if choice == 5 {
		p.Mode, err = u.ask("访问模式 default/whitelist", p.EffectiveMode())
	} else if choice == 6 {
		var v string
		v, err = u.ask("访问策略 on/off", "on")
		if v != "on" && v != "off" {
			return fmt.Errorf("请输入 on 或 off")
		}
		p.Disabled = v == "off"
	} else {
		list := "white"
		if choice == 8 {
			list = "black"
		}
		p, err = p.ClearList(list)
	}
	if err != nil {
		return err
	}
	return m.SetPolicy(ctx, p)
}
func (m *Manager) linkMenu(ctx context.Context, u *ui, kind string) error {
	s, _, err := m.managedConfig()
	if err != nil {
		return err
	}
	var b []byte
	if kind == "landing" {
		b, _ = jsonBytes(s.Node.LandingAccesses)
	} else {
		b, _ = jsonBytes(s.Node.RelayLinks)
	}
	fmt.Fprint(m.out, string(b))
	fmt.Fprintln(m.out, "  1 添加\n  2 启用\n  3 禁用\n  4 删除\n  5 导出落地 peer\n  0/q 返回")
	choice, err := u.choose(5)
	if err != nil || choice == 0 {
		return err
	}
	name, err := u.ask("线路名称", "")
	if err != nil {
		return err
	}
	if choice == 5 {
		if kind != "landing" {
			return fmt.Errorf("中转使用客户端配置导出入口")
		}
		p, err := m.Peer(name)
		if err != nil {
			return err
		}
		b, _ := jsonBytes(p)
		fmt.Fprint(m.out, string(b))
		return nil
	}
	if choice > 1 {
		return m.ChangeLink(ctx, kind, name, map[int]string{2: "enable", 3: "disable", 4: "remove"}[choice])
	}
	if kind == "landing" {
		security, err := u.ask("接入协议 reality/tls", "reality")
		if err != nil {
			return err
		}
		var port int
		if security == "tls" {
			v, e := u.ask("TLS 接入端口（0 为自动）", "0")
			if e != nil {
				return e
			}
			port, e = strconv.Atoi(v)
			if e != nil {
				return e
			}
		}
		peer, err := m.AddLanding(ctx, name, security, port)
		if err != nil {
			return err
		}
		b, _ := jsonBytes(peer)
		fmt.Fprint(m.out, string(b))
		return nil
	}
	path, err := u.ask("上游 peer JSON 文件（0600 权限）", "")
	if err != nil {
		return err
	}
	path, err = filepath.Abs(path)
	if err != nil {
		return err
	}
	var peer model.LandingPeer
	if err = loadJSON(path, &peer); err != nil {
		return err
	}
	return m.AddRelay(ctx, name, peer)
}
func (m *Manager) Edit(ctx context.Context, input io.Reader) error {
	if !terminalInput(input) && !terminalInput(os.Stdin) {
		return fmt.Errorf("编辑配置需要交互终端")
	}
	return m.withLock(ctx, func() error {
		md, _, err := m.readOperation(ctx)
		if err != nil {
			return err
		}
		b, err := readPrivate(m.paths.Config())
		if err != nil {
			return err
		}
		editor := ""
		for _, name := range []string{"vim", "nano", "vi"} {
			if path, e := m.lookPath(name); e == nil {
				editor = path
				break
			}
		}
		if editor == "" {
			return fmt.Errorf("未找到 vim/nano/vi")
		}
		if err = privateDir(m.paths.Staging()); err != nil {
			return err
		}
		f, err := os.CreateTemp(m.paths.Staging(), "edit-*.json")
		if err != nil {
			return err
		}
		path := f.Name()
		defer os.Remove(path)
		if err = f.Chmod(0600); err == nil {
			_, err = f.Write(b)
		}
		if e := f.Close(); err == nil {
			err = e
		}
		if err != nil {
			return err
		}
		u := newUI(input, m.out)
		for {
			cmd := exec.CommandContext(ctx, editor, path)
			cmd.Stdin = os.Stdin
			cmd.Stdout = m.out
			cmd.Stderr = m.errOut
			if err = cmd.Run(); err != nil {
				return err
			}
			edited, err := readPrivate(path)
			if err != nil {
				return err
			}
			if string(edited) == string(b) {
				m.progress("配置未变化")
				return nil
			}
			if err = m.validateBytes(ctx, md.ImageID, edited); err == nil {
				return m.applyEditedLocked(ctx, edited)
			}
			fmt.Fprintf(m.errOut, "校验失败：%v\n", err)
			if err = u.confirm("重新编辑？"); err != nil {
				return err
			}
		}
	})
}
