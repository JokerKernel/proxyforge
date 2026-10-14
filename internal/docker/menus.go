package docker

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"proxyforge/internal/docker/model"
)

func (m *Manager) askGenerate(input io.Reader, opts model.GenerateOptions) (model.GenerateOptions, error) {
	return m.askGenerateUI(context.Background(), newUI(input, m.out), opts)
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
		u.clearScreen()
		u.pageHeader("Xray（Docker）")
		u.instanceCard(Container, "["+s.Label+"]", s.Version, s.Detail, m.instanceRows())
		u.menuChoice("1", "部署/升级（准备镜像或升级版本）")
		u.menuChoice("2", "服务端配置（生成、修改、查看与服务管理）")
		u.menuChoice("3", "客户端配置（显示普通节点或中转节点配置）")
		u.menuChoice("4", "移除容器（删除容器，保留配置和数据）")
		fmt.Fprintln(u.out)
		u.menuChoice("5", "清理卸载残留（永久删除配置、凭据、证书和备份）")
		u.menuChoice("6", "恢复未完成事务（继续中断的配置或升级）")
		u.menuChoice("0/q", "返回")
		choice, err := u.chooseNumber("请选择", 0, 6, -1)
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		if choice == 0 {
			return nil
		}
		u.clearScreen()
		shouldPause := true
		switch choice {
		case 1:
			err = m.deployInteractive(ctx, u)
		case 2:
			shouldPause = false
			err = m.configMenu(ctx, u)
		case 3:
			var showed bool
			showed, err = m.clientMenu(ctx, u)
			shouldPause = showed
		case 4:
			err = m.confirmRemove(ctx, u)
		case 5:
			err = m.confirmCleanup(ctx, u)
		case 6:
			err = m.Recover(ctx)
		}
		if errors.Is(err, io.EOF) {
			return nil
		}
		if errors.Is(err, errBack) {
			continue
		}
		cancelled := errors.Is(err, errCancelled)
		if cancelled {
			switch choice {
			case 1:
				fmt.Fprintln(m.out, "已取消部署/升级。")
			case 4:
				fmt.Fprintln(m.out, "已取消移除容器。")
			case 5:
				fmt.Fprintln(m.out, "已取消清理。")
			default:
				fmt.Fprintln(m.out, "已取消。")
			}
			err = nil
		} else if err != nil {
			m.printMenuError(err)
		}
		if shouldPause || cancelled || err != nil {
			if err := u.pause(); err != nil {
				if errors.Is(err, io.EOF) {
					return nil
				}
				return err
			}
		}
	}
}

func (m *Manager) deployInteractive(ctx context.Context, u *ui) error {
	selection := ""
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		u.clearScreen()
		u.pageHeader("Xray（Docker）", "部署/升级")
		u.labeledCard("当前内核", m.deployCardRows(selection))
		u.menuChoice("1", "部署/更新")
		u.menuChoice("2", "选择版本")
		u.menuChoice("3", "指定版本")
		u.menuChoice("0/q", "返回")
		choice, err := u.chooseNumber("请选择", 0, 3, -1)
		if err != nil {
			return err
		}
		switch choice {
		case 0:
			return errBack
		case 1:
			return m.confirmDeploy(ctx, u, selection)
		case 2:
			next, chooseErr := m.chooseDeployChannel(u, selection)
			if errors.Is(chooseErr, errCancelled) {
				continue
			}
			if chooseErr != nil {
				return chooseErr
			}
			selection = next
		case 3:
			version, askErr := m.askDeployVersion(u)
			if errors.Is(askErr, errCancelled) {
				continue
			}
			if askErr != nil {
				return askErr
			}
			selection = version
		}
	}
}

func (m *Manager) deployCardRows(selection string) [][2]string {
	version := "尚未部署"
	if md, err := m.metadata(); err == nil {
		switch {
		case strings.TrimSpace(md.Version) != "":
			version = strings.TrimSpace(md.Version)
		case md.RequestedVersion != "":
			version = md.RequestedVersion
		default:
			version = "无法读取"
		}
	}
	target := "稳定版（latest）"
	switch selection {
	case "pre-release", "prerelease":
		target = "pre-release"
	default:
		if selection != "" && selection != "stable" && selection != "latest" {
			target = "指定版本 " + selection
		}
	}
	return [][2]string{{"内核", "Xray（Docker）"}, {"版本号", version}, {"安装渠道", target}}
}

func (m *Manager) chooseDeployChannel(u *ui, selection string) (string, error) {
	u.clearScreen()
	u.pageHeader("Xray（Docker）", "部署/升级", "选择版本")
	u.menuChoice("1", "稳定版（latest）")
	u.menuChoice("2", "pre-release")
	u.menuChoice("0/q", "返回")
	def := 1
	if selection == "pre-release" || selection == "prerelease" {
		def = 2
	}
	choice, err := u.chooseNumberCancelable("请选择版本渠道", 1, 2, def)
	if err != nil {
		return "", err
	}
	if choice == 2 {
		return "pre-release", nil
	}
	return "latest", nil
}

func (m *Manager) askDeployVersion(u *ui) (string, error) {
	u.clearScreen()
	u.pageHeader("Xray（Docker）", "部署/升级", "指定版本")
	for {
		value, err := u.ask("版本号", "")
		if err != nil {
			return "", err
		}
		if strings.TrimSpace(value) == "" {
			fmt.Fprintln(u.out, "版本号不能为空。")
			continue
		}
		version, err := normalizeVersion(value)
		if err != nil {
			fmt.Fprintf(u.out, "%v。\n", err)
			continue
		}
		return version, nil
	}
}

func (m *Manager) confirmDeploy(ctx context.Context, u *ui, version string) error {
	if _, e := m.metadata(); e == nil {
		u.confirmationPanel("操作确认：升级镜像", []string{"目标实例：" + Container, "安装版本：" + shownVersion(version)}, confirmSection{title: "将执行", items: []string{"拉取选定镜像并记录 digest", "保留现有 UUID、密钥、证书和线路", "失败时回滚到当前镜像和运行状态"}})
		if err := u.confirm("升级 Xray Docker 镜像？"); err != nil {
			return err
		}
		if err := m.Upgrade(ctx, version); err != nil {
			return err
		}
		if _, stateErr := m.LoadState(); os.IsNotExist(stateErr) {
			opts, err := m.askGenerateUI(ctx, u, model.GenerateOptions{})
			if err != nil {
				return err
			}
			return m.Generate(ctx, opts, true)
		}
		return nil
	} else if !os.IsNotExist(e) {
		return e
	}
	u.confirmationPanel("操作确认：部署实例", []string{"目标实例：" + Container, "安装版本：" + shownVersion(version)}, confirmSection{title: "将执行", items: []string{"准备官方镜像和 Compose 文件", "生成 REALITY 或 TLS 节点配置", "启动容器并检查运行状态"}})
	if err := u.confirm("准备官方镜像并部署 Docker Xray？"); err != nil {
		return err
	}
	if err := m.Prepare(ctx, version); err != nil {
		return err
	}
	opts, err := m.askGenerateUI(ctx, u, model.GenerateOptions{})
	if err != nil {
		return err
	}
	return m.Generate(ctx, opts, true)
}

func shownVersion(version string) string {
	switch strings.ToLower(strings.TrimSpace(version)) {
	case "", "stable", "latest":
		return "稳定版（latest）"
	case "pre-release", "prerelease":
		return "pre-release"
	default:
		return "指定版本 " + version
	}
}

func (m *Manager) confirmRemove(ctx context.Context, u *ui) error {
	u.confirmationPanel("危险操作确认：移除容器", []string{"目标实例：" + Container}, confirmSection{title: "将执行", items: []string{"停止并移除本实例容器"}}, confirmSection{title: "保留", items: []string{"服务端配置、凭据、证书和备份", "落地接入和中转线路"}}, confirmSection{title: "重要影响", items: []string{"代理会立即停止，直到重新部署"}})
	if err := u.confirm("移除本实例容器，保留配置和数据？"); err != nil {
		return err
	}
	return m.Remove(ctx)
}

func (m *Manager) confirmCleanup(ctx context.Context, u *ui) error {
	u.confirmationPanel("危险操作确认：清理卸载残留", []string{"清理目标：" + Instance}, confirmSection{title: "永久删除", items: []string{"配置、凭据、证书和备份", "部署记录、访问控制、落地与中转数据"}}, confirmSection{title: "数据保护", items: []string{"此操作不会创建新备份"}})
	if err := u.confirm("永久删除 Docker Xray 配置、凭据、证书和备份？"); err != nil {
		return err
	}
	return m.Cleanup(ctx)
}

func (m *Manager) instanceRows() [][2]string {
	port := "未生成"
	if s, err := m.LoadState(); err == nil {
		if s.Node.Port >= 1 && s.Node.Port <= 65535 {
			port = strconv.Itoa(s.Node.Port)
		} else if s.Node.UUID != "" || s.Node.SNI != "" {
			port = "无法读取"
		}
	}
	rows := [][2]string{{"监听端口", port}, {"运行用户", "0:0"}}
	if md, err := m.metadata(); err == nil {
		rows = append(rows, [2]string{"更新渠道", "[" + channelLabel(md.Channel) + "]"})
	}
	return rows
}

func (m *Manager) configMenu(ctx context.Context, u *ui) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		u.clearScreen()
		u.pageHeader("Xray（Docker）", "服务端配置")
		m.printConfigCard(u)
		u.menuChoice("1", "生成/更新配置（完整覆盖现有配置，不合并原配置）")
		u.menuChoice("2", "查看配置（显示当前 JSON，含敏感凭据）")
		u.menuChoice("3", "修改配置（DNS、出站 IP、重置节点与 SNI 检测）")
		u.menuChoice("4", "编辑配置（vim / nano / vi）")
		u.menuChoice("5", "日志级别（调整内核日志详细程度）")
		u.menuChoice("6", "服务管理（启动、停止、状态与日志）")
		u.menuChoice("7", "中转与落地配置（同端口按用户分流）")
		u.menuChoice("0/q", "返回")
		choice, err := u.chooseNumber("请选择", 0, 7, 0)
		if err != nil {
			return err
		}
		if choice == 0 {
			return nil
		}
		u.clearScreen()
		shouldPause := true
		switch choice {
		case 1:
			err = m.generateInteractive(ctx, u, false)
			if errors.Is(err, errCancelled) {
				fmt.Fprintln(m.out, "已取消生成服务端配置。")
				err = nil
				shouldPause = false
			}
		case 2:
			err = m.showServerConfig(u)
		case 3:
			shouldPause = false
			err = m.modifyMenu(ctx, u)
		case 4:
			err = m.Edit(ctx, u.reader)
		case 5:
			err = m.logMenu(ctx, u)
		case 6:
			shouldPause = false
			err = m.serviceMenu(ctx, u)
		case 7:
			shouldPause = false
			err = m.linkMenu(ctx, u)
		}
		if errors.Is(err, io.EOF) {
			return err
		}
		if errors.Is(err, errCancelled) {
			continue
		}
		if err != nil {
			m.printMenuError(err)
		}
		if shouldPause || err != nil {
			if err := u.pause(); err != nil {
				return err
			}
		}
	}
}

func (m *Manager) generateInteractive(ctx context.Context, u *ui, deploy bool) error {
	if _, e := os.Stat(m.paths.Config()); e == nil {
		u.confirmationPanel("操作确认：生成服务端配置", []string{"目标实例：" + Container}, confirmSection{title: "将执行", items: []string{"备份当前服务端配置", "完整覆盖现有配置，不合并原配置", "使用当前镜像校验新配置"}}, confirmSection{title: "配置保护", items: []string{"未选择清除时，保留已有落地接入和中转线路"}})
		if err := u.confirm("完整生成会覆盖手动配置，确认继续？"); err != nil {
			return err
		}
	}
	opts, err := m.askGenerateUI(ctx, u, model.GenerateOptions{})
	if err != nil {
		return err
	}
	return m.Generate(ctx, opts, deploy)
}

func (m *Manager) showServerConfig(u *ui) error {
	u.pageHeader("Xray（Docker）", "查看服务端配置")
	b, err := m.ServerConfig()
	if err != nil {
		return err
	}
	fmt.Fprintln(u.out, "警告：当前服务端配置可能包含 UUID、REALITY 私钥等敏感信息，请勿泄露。")
	fmt.Fprintln(u.out, "----------------------------------------")
	fmt.Fprint(u.out, string(b))
	if len(b) == 0 || b[len(b)-1] != '\n' {
		fmt.Fprintln(u.out)
	}
	return nil
}

func (m *Manager) printConfigCard(u *ui) {
	service := "无法读取"
	statusCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	if st, err := m.Status(statusCtx); err == nil || st.Label != "" {
		service = st.Label
	}
	cancel()
	state, stateErr := m.LoadState()
	s, cfg, cfgErr := m.managedConfig()
	node := state.Node
	hasConfig := cfgErr == nil
	if hasConfig {
		node = s.Node
	}
	hasNode := stateErr == nil && (node.UUID != "" || node.SNI != "" || node.Port != 0 || node.Server != "")
	read := func(load func() (string, error)) string {
		if !hasConfig {
			if hasNode {
				return "无法读取"
			}
			return "未生成"
		}
		value, err := load()
		if err != nil || value == "" {
			return "无法读取"
		}
		return value
	}
	rows := [][2]string{{"运行用户", "0:0"}, {"日志级别", read(func() (string, error) {
		level, err := m.codec.CurrentLogLevel(cfg)
		return cardText(logLevelLabel(level)), err
	})}, {"服务状态", service}}
	relayTotal, relayOn := countEnabled(node.RelayLinks, func(item model.RelayLink) bool { return item.Enabled })
	landingTotal, landingOn := countEnabled(node.LandingAccesses, func(item model.LandingAccess) bool { return item.Enabled })
	rows = append(rows, [2]string{"中转", linkSummary(hasNode, stateErr == nil || hasConfig, relayTotal, relayOn)}, [2]string{"落地", linkSummary(hasNode, stateErr == nil || hasConfig, landingTotal, landingOn)}, [2]string{"DNS 设置", read(func() (string, error) {
		profile, err := m.codec.CurrentDNSProfile(cfg)
		return cardText(dnsLabel(profile)), err
	})}, [2]string{"出站 IP", read(func() (string, error) {
		strategy, err := m.codec.CurrentOutboundIPStrategy(cfg)
		return outboundLabel(strategy), err
	})})
	if hasNode && node.XrayFallbackGuard {
		rows = append(rows, [2]string{"回落 IP", read(func() (string, error) {
			strategy, err := m.codec.CurrentFallbackIPStrategy(cfg)
			return outboundLabel(strategy), err
		})})
	}
	if hasNode && node.IsTLS() {
		rows = append(rows, [2]string{"节点协议", "VLESS + RAW + TLS + Vision"}, [2]string{"证书验证", "固定指纹"})
	} else if hasNode {
		rows = append(rows,
			[2]string{"SNI 防护", guardText(node.XrayFallbackGuard, "按 SNI 域名规则限制回落", "不检查回落 SNI 域名")},
			[2]string{"严格模式", strictText(node)},
			[2]string{"HTTP Host", hostText(node)},
		)
	}
	if hasNode {
		rows = append(rows, [2]string{"访问模式", accessModeLabel(node.AccessPolicy.EffectiveMode())}, [2]string{"访问名单", fmt.Sprintf("白名单 %d 条 · 黑名单 %d 条", len(node.AccessPolicy.Whitelist), len(node.AccessPolicy.Blacklist))})
	}
	port := "未生成"
	if hasNode {
		if node.Port >= 1 && node.Port <= 65535 {
			port = strconv.Itoa(node.Port)
		} else {
			port = "无法读取"
		}
	}
	sni := "未生成"
	if hasNode {
		sni = strings.TrimSpace(node.SNI)
		if sni == "" {
			sni = "无法读取"
		}
	}
	rows = append(rows, [2]string{"端口", port}, [2]string{"SNI", sni})
	u.labeledCard("当前配置", rows)
}

func guardText(enabled bool, on, off string) string {
	if enabled {
		return "已开启  -- " + on
	}
	return "未开启  -- " + off
}

func strictText(node model.Node) string {
	if !node.XrayFallbackGuard {
		return "未开启  -- 未启用 SNI 防护"
	}
	if node.XrayFallbackExactDomain {
		return "已开启  -- 仅允许完整 SNI 域名"
	}
	return "未开启  -- 允许 SNI 域名及其子域名"
}

func hostText(node model.Node) string {
	if !node.XrayFallbackGuard {
		return "未开启  -- 未启用 SNI 防护"
	}
	if node.XrayFallbackHTTPDomain {
		return "已开启  -- 按 SNI 域名规则检查明文 Host"
	}
	return "不限制  -- 不检查明文 HTTP Host"
}

func (m *Manager) modifyMenu(ctx context.Context, u *ui) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		state, stateErr := m.LoadState()
		tlsMode := stateErr == nil && state.Node.IsTLS()
		hasFallback := stateErr == nil && state.Node.XrayFallbackGuard
		u.clearScreen()
		u.pageHeader("Xray（Docker）", "修改配置")
		m.printConfigCard(u)
		u.menuChoice("1", "访问控制（白名单、黑名单与仅允许白名单）")
		if tlsMode {
			u.menuChoice("2", "重置 UUID（保留 TLS 证书）")
			u.menuChoice("3", "重新生成 TLS 证书（保留 UUID）")
		} else {
			u.menuChoice("2", "重置节点凭证（UUID、REALITY 密钥和 short ID；保留 SNI/target）")
			u.menuChoice("3", "重置 SNI/target（保留节点凭证）")
		}
		u.menuChoice("4", "出站 IP（优先或仅使用 IPv4 / IPv6）")
		maxChoice := 4
		fallbackChoice := 0
		if hasFallback {
			maxChoice++
			fallbackChoice = maxChoice
			u.menuChoice(strconv.Itoa(fallbackChoice), "回落 IP（回落访问目标站的 IPv4 / IPv6）")
		}
		maxChoice++
		dnsChoice := maxChoice
		u.menuChoice(strconv.Itoa(dnsChoice), "DNS 设置（系统 DNS、明文 DNS 或 DoH）")
		sniChoice := 0
		if !tlsMode {
			maxChoice++
			sniChoice = maxChoice
			u.menuChoice(strconv.Itoa(sniChoice), "REALITY SNI 候选检测（重新测试，不修改配置）")
		}
		u.menuChoice("0/q", "返回")
		choice, err := u.chooseNumber("请选择", 0, maxChoice, 0)
		if err != nil {
			return err
		}
		if choice == 0 {
			return nil
		}
		u.clearScreen()
		shouldPause := true
		switch {
		case choice == 1:
			shouldPause = false
			err = m.accessMenu(ctx, u)
		case choice == 2 && tlsMode:
			err = m.confirmReset(ctx, u, model.ResetOptions{Credentials: true}, "重置 UUID 会使现有客户端失效，TLS 证书保持不变。")
		case choice == 2:
			err = m.confirmReset(ctx, u, model.ResetOptions{}, "轮换凭据会使现有客户端失效，SNI 和 target 保持不变。")
		case choice == 3 && tlsMode:
			err = m.confirmReset(ctx, u, model.ResetOptions{RotateCertificate: true}, "重新生成 TLS 证书后，需要用新指纹更新客户端。")
		case choice == 3:
			err = m.resetSNIInteractive(ctx, u)
		case choice == 4:
			err = m.strategyMenu(ctx, u, "outbound-ip")
		case choice == fallbackChoice:
			err = m.strategyMenu(ctx, u, "fallback-ip")
		case choice == dnsChoice:
			err = m.dnsMenu(ctx, u)
		case sniChoice > 0 && choice == sniChoice:
			err = m.retestSNICandidates(ctx, u)
		}
		if errors.Is(err, io.EOF) {
			return err
		}
		if errors.Is(err, errCancelled) {
			continue
		}
		if err != nil {
			m.printMenuError(err)
		}
		if shouldPause || err != nil {
			if err := u.pause(); err != nil {
				return err
			}
		}
	}
}

func (m *Manager) confirmReset(ctx context.Context, u *ui, opts model.ResetOptions, message string) error {
	u.confirmationPanel("操作确认：重置节点", []string{"目标实例：" + Container}, confirmSection{title: "重要影响", items: []string{message, "需要重新导出受影响的客户端配置"}})
	if err := u.confirm(message); err != nil {
		return err
	}
	return m.Reset(ctx, opts)
}

func (m *Manager) resetSNIInteractive(ctx context.Context, u *ui) error {
	state, err := m.LoadState()
	if err != nil {
		return err
	}
	if state.Node.IsTLS() {
		return fmt.Errorf("TLS 节点请通过轮换证书更新 SNI")
	}
	server := strings.TrimSpace(state.Node.Server)
	u.pageHeader("Xray（Docker）", "重置节点")
	sni, err := u.ask("新的 REALITY SNI（输入域名；直接回车自动测速候选）", "")
	if err != nil {
		return err
	}
	sni = strings.TrimSpace(sni)
	manual := sni != ""
	if sni == "" {
		selected, selectErr := m.selectSNICandidate(ctx, u, server)
		if selectErr != nil {
			return selectErr
		}
		sni = selected
		if u.interactive {
			u.clearScreen()
			u.pageHeader("Xray（Docker）", "重置节点")
			fmt.Fprintf(u.out, "已选择 REALITY SNI：%s\n", sni)
		}
	}
	if manual {
		if err = m.confirmManualSNI(ctx, u, sni, server); err != nil {
			return err
		}
	}
	target, err := u.ask("新的 REALITY target", net.JoinHostPort(sni, "443"))
	if err != nil {
		return err
	}
	u.confirmationPanel(
		"危险操作确认：重置节点",
		[]string{"目标实例：" + Container, "新 SNI：" + sni, "新 Target：" + target},
		confirmSection{title: "将更新", items: []string{"受管入站的 SNI 和 target"}},
		confirmSection{title: "将保留", items: []string{"UUID、REALITY 密钥和 short ID", "修改前会备份当前配置"}},
		confirmSection{title: "重要影响", items: []string{"所有旧客户端配置中的 SNI/target 会立即失效"}},
	)
	if err = u.confirm("重置节点 SNI 和 target？"); err != nil {
		return err
	}
	return m.Reset(ctx, model.ResetOptions{SNI: sni, Target: target, PreserveCredentials: true})
}

func (m *Manager) dnsMenu(ctx context.Context, u *ui) error {
	_, cfg, err := m.managedConfig()
	if err != nil {
		return err
	}
	current, err := m.codec.CurrentDNSProfile(cfg)
	if err != nil {
		return err
	}
	profiles := m.codec.DNSProfiles()
	u.pageHeader("Xray（Docker）", "DNS 设置")
	fmt.Fprintf(u.out, "当前配置：%s\n\n", cardText(dnsLabel(current)))
	defaultChoice := 1
	for index, profile := range profiles {
		u.menuChoice(strconv.Itoa(index+1), dnsLabel(profile))
		if profile == current || (profile == model.DNSProfilePublicCloudflare && current == model.DNSProfileCloudflare) || (profile == model.DNSProfilePublicGoogle && current == model.DNSProfileGoogle) {
			defaultChoice = index + 1
		}
	}
	u.menuChoice("0/q", "返回")
	choice, err := u.chooseNumber("请选择 DNS", 0, len(profiles), defaultChoice)
	if err != nil || choice == 0 {
		if err == nil {
			return errCancelled
		}
		return err
	}
	selected := profiles[choice-1]
	if selected == current {
		fmt.Fprintf(u.out, "[提示] DNS 已经是 %s，无需修改。\n", cardText(dnsLabel(selected)))
		return nil
	}
	u.confirmationPanel("操作确认：设置 DNS", []string{"目标实例：" + Container, "当前配置：" + cardText(dnsLabel(current)), "新的配置：" + cardText(dnsLabel(selected))}, confirmSection{title: "将执行", items: []string{"按 Xray 格式更新 DNS 配置", "使用当前镜像校验候选配置", "备份并原子更新当前服务端配置", "容器正在运行时重建使设置立即生效"}})
	if err = u.confirm("更新 DNS 设置？"); err != nil {
		return err
	}
	return m.PatchSetting(ctx, "dns", selected)
}

func (m *Manager) logMenu(ctx context.Context, u *ui) error {
	_, cfg, err := m.managedConfig()
	if err != nil {
		return err
	}
	current, err := m.codec.CurrentLogLevel(cfg)
	if err != nil {
		return err
	}
	levels := m.codec.LogLevels()
	u.pageHeader("Xray（Docker）", "服务端配置", "日志级别")
	fmt.Fprintf(u.out, "当前级别：%s\n\n", cardText(logLevelLabel(current)))
	defaultChoice := 1
	for index, level := range levels {
		u.menuChoice(strconv.Itoa(index+1), logLevelLabel(level))
		if level == current {
			defaultChoice = index + 1
		}
	}
	u.menuChoice("0/q", "返回")
	choice, err := u.chooseNumber("请选择日志级别", 0, len(levels), defaultChoice)
	if err != nil || choice == 0 {
		if err == nil {
			return errCancelled
		}
		return err
	}
	selected := levels[choice-1]
	if selected == current {
		fmt.Fprintf(u.out, "[提示] 日志级别已经是 %s，无需修改。\n", cardText(logLevelLabel(selected)))
		return nil
	}
	u.confirmationPanel("操作确认：设置日志级别", []string{"目标实例：" + Container, "当前级别：" + cardText(logLevelLabel(current)), "新的级别：" + cardText(logLevelLabel(selected))}, confirmSection{title: "将执行", items: []string{"只修改日志级别", "使用当前镜像校验候选配置", "备份并原子更新当前服务端配置"}})
	if err = u.confirm("更新日志级别？"); err != nil {
		return err
	}
	return m.PatchSetting(ctx, "log", selected)
}

func (m *Manager) strategyMenu(ctx context.Context, u *ui, setting string) error {
	_, cfg, err := m.managedConfig()
	if err != nil {
		return err
	}
	var current string
	if setting == "fallback-ip" {
		current, err = m.codec.CurrentFallbackIPStrategy(cfg)
	} else {
		current, err = m.codec.CurrentOutboundIPStrategy(cfg)
	}
	if err != nil {
		return err
	}
	strategies := m.codec.OutboundIPStrategies()
	title := "出站 IP"
	if setting == "fallback-ip" {
		title = "回落 IP"
	}
	u.pageHeader("Xray（Docker）", title)
	fmt.Fprintf(u.out, "当前生效：%s\n\n", outboundLabel(current))
	defaultChoice := -1
	for index, strategy := range strategies {
		label := outboundChoice(strategy)
		if strategy == current {
			label = "[当前] " + label
			defaultChoice = index + 1
		}
		u.menuChoice(strconv.Itoa(index+1), label)
	}
	u.menuChoice("0/q", "返回")
	choice, err := u.chooseNumber("请选择"+title, 0, len(strategies), defaultChoice)
	if err != nil || choice == 0 {
		if err == nil {
			return errCancelled
		}
		return err
	}
	selected := strategies[choice-1]
	if selected == current {
		fmt.Fprintf(u.out, "[提示] %s 已经是 %s，无需修改。\n", title, outboundLabel(selected))
		return nil
	}
	u.confirmationPanel("操作确认：设置"+title, []string{"目标实例：" + Container, "当前生效：" + outboundLabel(current), "新的策略：" + outboundLabel(selected)}, confirmSection{title: "将执行", items: []string{"更新 Xray 地址族策略", "使用当前镜像校验候选配置", "备份并原子更新当前服务端配置", "容器正在运行时重建使设置立即生效"}})
	if err = u.confirm("更新" + title + "？"); err != nil {
		return err
	}
	return m.PatchSetting(ctx, setting, selected)
}

func (m *Manager) serviceMenu(ctx context.Context, u *ui) error {
	actions := map[int]string{1: "start", 2: "stop", 3: "restart", 5: "logs"}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		u.clearScreen()
		u.pageHeader("Xray（Docker）", "服务端配置", "服务管理")
		u.menuChoice("1", "启动（启动容器）")
		u.menuChoice("2", "停止（停止容器）")
		u.menuChoice("3", "重启（重启使配置立即生效）")
		u.menuChoice("4", "状态（查看容器运行状态）")
		u.menuChoice("5", "最近日志（查看容器最近记录）")
		u.menuChoice("6", "实时日志（Ctrl+C 返回）")
		u.menuChoice("0/q", "返回")
		choice, err := u.chooseNumber("请选择", 0, 6, 4)
		if err != nil {
			return err
		}
		if choice == 0 {
			return nil
		}
		u.clearScreen()
		if choice == 6 {
			u.pageHeader("Xray（Docker）", "服务端配置", "服务管理", "实时日志")
			followCtx, stop := signal.NotifyContext(ctx, os.Interrupt)
			err = m.FollowLogs(followCtx)
			if followCtx.Err() != nil && ctx.Err() == nil {
				err = nil
			}
			stop()
			if err != nil {
				m.printMenuError(err)
				if err := u.pause(); err != nil {
					return err
				}
			}
			continue
		}
		if choice == 4 {
			st, e := m.Status(ctx)
			u.pageHeader("Xray（Docker）", "服务端配置", "服务管理", "状态")
			u.instanceCard(Container, "["+st.Label+"]", st.Version, st.Detail, m.instanceRows())
			err = e
		} else {
			b, e := m.Service(ctx, actions[choice])
			fmt.Fprint(m.out, string(b))
			if len(b) > 0 && b[len(b)-1] != '\n' {
				fmt.Fprintln(m.out)
			}
			err = e
		}
		if err != nil {
			m.printMenuError(err)
		}
		if errors.Is(err, io.EOF) {
			return err
		}
		if err := u.pause(); err != nil {
			return err
		}
	}
}

func (m *Manager) clientMenu(ctx context.Context, u *ui) (bool, error) {
	u.pageHeader("Xray（Docker）", "客户端配置")
	u.menuChoice("1", "普通节点 · Clash YAML（Mihomo/Clash Meta）")
	u.menuChoice("2", "普通节点 · 原生 JSON（sing-box / Xray 客户端）")
	u.menuChoice("3", "中转节点客户端配置（选择线路与格式）")
	u.menuChoice("0/q", "返回")
	choice, err := u.chooseNumber("请选择", 0, 3, 1)
	if err != nil || choice == 0 {
		return false, err
	}
	if choice == 3 {
		return m.relayClientMenu(ctx, u)
	}
	format := "clash"
	if choice == 2 {
		format = "native"
	}
	return true, m.printClient(ctx, u, format, "")
}

func (m *Manager) relayClientMenu(ctx context.Context, u *ui) (bool, error) {
	state, err := m.LoadState()
	if err != nil {
		return true, err
	}
	links := state.Node.RelayLinks
	u.clearScreen()
	u.pageHeader("Xray（Docker）", "客户端配置", "中转节点")
	if len(links) == 0 {
		fmt.Fprintln(u.out, "尚未配置中转线路，请先从服务端配置中添加中转线路。")
		return true, nil
	}
	for i, link := range links {
		title := link.Name
		if link.UserName != "" && link.UserName != link.Name {
			title += " · 用户 " + link.UserName
		}
		u.menuBadge(strconv.Itoa(i+1), title, "["+enabledLabel(link.Enabled)+"]")
	}
	u.menuChoice("0/q", "返回")
	choice, err := u.chooseNumber("请选择中转线路", 0, len(links), 1)
	if err != nil || choice == 0 {
		return false, err
	}
	selected := links[choice-1]
	u.clearScreen()
	u.pageHeader("Xray（Docker）", "客户端配置", "中转节点", selected.Name)
	u.menuChoice("1", "原生 JSON（sing-box / Xray 客户端）")
	u.menuChoice("2", "Clash YAML（Mihomo/Clash Meta）")
	u.menuChoice("0/q", "返回")
	formatChoice, err := u.chooseNumber("请选择客户端格式", 0, 2, 1)
	if err != nil || formatChoice == 0 {
		return false, err
	}
	format := "native"
	if formatChoice == 2 {
		format = "clash"
	}
	return true, m.printClient(ctx, u, format, selected.Name)
}

func (m *Manager) printClient(ctx context.Context, u *ui, format, name string) error {
	b, err := m.Client(ctx, format, name)
	if err != nil {
		return err
	}
	fmt.Fprint(u.out, string(b))
	if len(b) == 0 || b[len(b)-1] != '\n' {
		fmt.Fprintln(u.out)
	}
	return nil
}

func (m *Manager) accessMenu(ctx context.Context, u *ui) error {
	for {
		s, _, err := m.managedConfig()
		if err != nil {
			return err
		}
		policy := s.Node.AccessPolicy
		u.clearScreen()
		u.pageHeader("Xray（Docker）", "访问控制")
		m.printAccessCard(u, policy)
		u.menuChoice("1", "切换访问模式")
		u.menuChoice("2", fmt.Sprintf("白名单管理（%d 条）", len(policy.Whitelist)))
		u.menuChoice("3", fmt.Sprintf("黑名单管理（%d 条）", len(policy.Blacklist)))
		switchAction := "关闭访问控制（保留模式和名单）"
		if policy.Disabled {
			switchAction = "启用访问控制（应用保存的模式和名单）"
		}
		u.menuChoice("4", switchAction)
		u.menuChoice("0/q", "返回")
		choice, err := u.chooseNumber("请选择", 0, 4, 0)
		if err != nil {
			return err
		}
		switch choice {
		case 0:
			return nil
		case 1:
			err = m.accessModeMenu(ctx, u)
		case 2:
			err = m.accessListMenu(ctx, u, "white")
		case 3:
			err = m.accessListMenu(ctx, u, "black")
		case 4:
			next := policy
			next.Disabled = !policy.Disabled
			message := "关闭访问控制？保留模式和两份名单，恢复原有拦截规则。"
			if !next.Disabled {
				message = "启用访问控制？按保存的模式和两份名单应用规则。"
			}
			if err = u.confirm(message); err == nil {
				err = m.SetPolicy(ctx, next)
			}
		}
		if errors.Is(err, errCancelled) {
			continue
		}
		if err != nil {
			return err
		}
	}
}

func (m *Manager) printAccessCard(u *ui, policy model.AccessPolicy) {
	var order []string
	if !policy.Disabled && len(policy.Whitelist) > 0 {
		order = append(order, "白名单放行")
	}
	if policy.BlacklistEnabled() && len(policy.Blacklist) > 0 {
		order = append(order, "黑名单拒绝")
	}
	order = append(order, "原有拦截")
	defaultAction := "放行"
	finalAction := "其余放行"
	if !policy.Disabled && policy.EffectiveMode() == model.AccessModeWhitelist {
		defaultAction = "拒绝；白名单为空时全部拒绝。"
		finalAction = "其余拒绝"
	}
	order = append(order, finalAction)
	u.labeledCard("访问策略", [][2]string{{"访问控制", accessSwitchLabel(policy)}, {"访问模式", accessModeLabel(policy.EffectiveMode())}, {"规则顺序", strings.Join(order, " → ")}, {"默认动作", defaultAction}})
}

func (m *Manager) accessModeMenu(ctx context.Context, u *ui) error {
	s, _, err := m.managedConfig()
	if err != nil {
		return err
	}
	policy := s.Node.AccessPolicy
	u.clearScreen()
	u.pageHeader("Xray（Docker）", "访问控制", "切换访问模式")
	m.printAccessCard(u, policy)
	modes := []string{model.AccessModeDefault, model.AccessModeWhitelist}
	defaultChoice := 1
	for i, mode := range modes {
		label := accessModeLabel(mode)
		if mode == policy.EffectiveMode() {
			label += "（当前）"
			defaultChoice = i + 1
		}
		u.menuChoice(strconv.Itoa(i+1), label)
	}
	u.menuChoice("0/q", "返回")
	choice, err := u.chooseNumber("请选择访问模式", 0, len(modes), defaultChoice)
	if err != nil || choice == 0 {
		if err == nil {
			return errCancelled
		}
		return err
	}
	policy.Mode = modes[choice-1]
	if policy.Mode == policy.EffectiveMode() && modes[choice-1] == s.Node.AccessPolicy.EffectiveMode() {
		fmt.Fprintln(u.out, "[提示] 访问模式没有变化。")
		return nil
	}
	if err = u.confirm("将访问模式切换为" + accessModeLabel(policy.Mode) + "？"); err != nil {
		return err
	}
	return m.SetPolicy(ctx, policy)
}

func (m *Manager) accessListMenu(ctx context.Context, u *ui, listName string) error {
	name := "白名单"
	if listName == "black" {
		name = "黑名单"
	}
	for {
		s, _, err := m.managedConfig()
		if err != nil {
			return err
		}
		policy := s.Node.AccessPolicy
		entries := policy.Whitelist
		if listName == "black" {
			entries = policy.Blacklist
		}
		u.clearScreen()
		u.pageHeader("Xray（Docker）", "访问控制", name)
		u.labeledCard("当前名单", [][2]string{{"名单", name}, {"规则数量", strconv.Itoa(len(entries)) + " 条"}})
		u.menuChoice("1", "添加规则")
		u.menuChoice("2", "删除规则")
		u.menuChoice("3", "清空名单")
		u.menuChoice("0/q", "返回")
		choice, err := u.chooseNumber("请选择", 0, 3, 0)
		if err != nil {
			return err
		}
		if choice == 0 {
			return nil
		}
		switch choice {
		case 1:
			err = m.addAccessEntry(ctx, u, listName)
		case 2:
			err = m.removeAccessEntry(ctx, u, listName, entries)
		case 3:
			if err = u.confirm("清空" + name + "？"); err == nil {
				policy, err = policy.ClearList(listName)
				if err == nil {
					err = m.SetPolicy(ctx, policy)
				}
			}
		}
		if errors.Is(err, errCancelled) {
			continue
		}
		if err != nil {
			return err
		}
	}
}

func (m *Manager) addAccessEntry(ctx context.Context, u *ui, listName string) error {
	value, err := u.ask("域名、IP 或 CIDR", "")
	if err != nil {
		return err
	}
	entry, err := model.ParseAccessEntry(value, false)
	if err != nil {
		return err
	}
	if entry.Type == model.AccessEntryDomain {
		u.menuChoice("1", "域名及其全部子域名（默认）")
		u.menuChoice("2", "仅完整域名")
		u.menuChoice("0/q", "返回")
		choice, chooseErr := u.chooseNumberCancelable("请选择域名匹配方式", 1, 2, 1)
		if chooseErr != nil {
			return chooseErr
		}
		if choice == 2 {
			entry, err = model.ParseAccessEntry(value, true)
			if err != nil {
				return err
			}
		}
	}
	s, _, err := m.managedConfig()
	if err != nil {
		return err
	}
	policy := s.Node.AccessPolicy
	if listName == "black" {
		policy.Blacklist = append(policy.Blacklist, entry)
	} else {
		policy.Whitelist = append(policy.Whitelist, entry)
	}
	return m.SetPolicy(ctx, policy)
}

func (m *Manager) removeAccessEntry(ctx context.Context, u *ui, listName string, entries []model.AccessEntry) error {
	if len(entries) == 0 {
		fmt.Fprintln(u.out, "当前名单是空的。")
		return nil
	}
	for i, entry := range entries {
		u.menuChoice(strconv.Itoa(i+1), accessEntryLabel(entry))
	}
	u.menuChoice("0/q", "返回")
	choice, err := u.chooseNumberCancelable("请选择要删除的规则", 1, len(entries), 1)
	if err != nil {
		return err
	}
	s, _, err := m.managedConfig()
	if err != nil {
		return err
	}
	policy := s.Node.AccessPolicy
	target := entries[choice-1]
	filter := func(list []model.AccessEntry) []model.AccessEntry {
		filtered := make([]model.AccessEntry, 0, len(list))
		removed := false
		for _, item := range list {
			if !removed && item == target {
				removed = true
				continue
			}
			filtered = append(filtered, item)
		}
		return filtered
	}
	if listName == "black" {
		policy.Blacklist = filter(policy.Blacklist)
	} else {
		policy.Whitelist = filter(policy.Whitelist)
	}
	return m.SetPolicy(ctx, policy)
}

func (m *Manager) linkMenu(ctx context.Context, u *ui) error {
	for {
		state, err := m.LoadState()
		if err != nil {
			return err
		}
		node := state.Node
		u.clearScreen()
		u.pageHeader("Xray（Docker）", "中转与落地线路")
		relayTotal, relayOn := countEnabled(node.RelayLinks, func(item model.RelayLink) bool { return item.Enabled })
		landingTotal, landingOn := countEnabled(node.LandingAccesses, func(item model.LandingAccess) bool { return item.Enabled })
		port := "未生成"
		if node.Port >= 1 && node.Port <= 65535 {
			port = strconv.Itoa(node.Port)
		} else if strings.TrimSpace(node.SNI) != "" {
			port = "无法读取"
		}
		relaySecurity := make([]string, 0, len(node.RelayLinks))
		for _, link := range node.RelayLinks {
			relaySecurity = append(relaySecurity, link.Upstream.Security)
		}
		landingSecurity := make([]string, 0, len(node.LandingAccesses))
		for _, access := range node.LandingAccesses {
			landingSecurity = append(landingSecurity, access.Security)
		}
		u.labeledCard("当前线路", [][2]string{
			{"监听端口", port},
			{"中转", linkSummary(true, true, relayTotal, relayOn)},
			{"中转协议", linkProtocolDisplay(relaySecurity)},
			{"落地", linkSummary(true, true, landingTotal, landingOn)},
			{"落地协议", linkProtocolDisplay(landingSecurity)},
		})
		u.menuChoice("1", "添加中转线路（专用用户流量转发到远程落地）")
		u.menuChoice("2", "管理中转线路")
		u.menuChoice("3", "创建落地接入（允许中转机连接本机）")
		u.menuChoice("4", "管理落地接入")
		u.menuChoice("5", "查看流量关系")
		u.menuChoice("0/q", "返回")
		choice, err := u.chooseNumber("请选择", 0, 5, 0)
		if err != nil {
			return err
		}
		if choice == 0 {
			return nil
		}
		u.clearScreen()
		switch choice {
		case 1:
			err = m.addRelayInteractive(ctx, u)
		case 2:
			err = m.manageRelays(ctx, u)
		case 3:
			err = m.addLandingInteractive(ctx, u, node)
		case 4:
			err = m.manageLandings(ctx, u)
		case 5:
			err = m.printLinkGraph(u, node)
		}
		if errors.Is(err, errCancelled) {
			continue
		}
		if err != nil {
			return err
		}
	}
}

func (m *Manager) addLandingInteractive(ctx context.Context, u *ui, node model.Node) error {
	u.pageHeader("Xray（Docker）", "创建落地接入")
	protocol := "REALITY"
	if node.IsTLS() {
		protocol = "TLS"
	}
	u.menuChoice("1", fmt.Sprintf("使用当前协议（%s，复用端口 %d）", protocol, node.Port))
	u.menuChoice("2", "创建 VLESS + RAW + TLS + Vision（随机高位独立端口）")
	u.menuChoice("0/q", "返回")
	choice, err := u.chooseNumberCancelable("请选择落地接入协议", 1, 2, 1)
	if err != nil {
		return err
	}
	security := "reality"
	port := 0
	if choice == 2 {
		security = "tls"
	}
	name, err := u.ask("接入名称", "")
	if err != nil {
		return err
	}
	peer, err := m.AddLanding(ctx, name, security, port)
	if err != nil {
		return err
	}
	fmt.Fprintln(u.out, "落地连接信息：")
	b, _ := jsonBytes(peer)
	fmt.Fprint(u.out, string(b))
	return nil
}

func (m *Manager) addRelayInteractive(ctx context.Context, u *ui) error {
	u.pageHeader("Xray（Docker）", "添加中转线路")
	name, err := u.ask("线路名称", "")
	if err != nil {
		return err
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

func (m *Manager) manageRelays(ctx context.Context, u *ui) error {
	state, err := m.LoadState()
	if err != nil {
		return err
	}
	links := state.Node.RelayLinks
	u.pageHeader("Xray（Docker）", "管理中转线路")
	if len(links) == 0 {
		fmt.Fprintln(u.out, "尚未配置中转线路。")
		return nil
	}
	for i, link := range links {
		u.menuBadge(strconv.Itoa(i+1), link.Name, "["+enabledLabel(link.Enabled)+"]")
	}
	u.menuChoice("0/q", "返回")
	choice, err := u.chooseNumber("请选择中转线路", 0, len(links), 1)
	if err != nil || choice == 0 {
		if err == nil {
			return errCancelled
		}
		return err
	}
	return m.changeLinkInteractive(ctx, u, "relay", links[choice-1].Name, links[choice-1].Enabled)
}

func (m *Manager) manageLandings(ctx context.Context, u *ui) error {
	state, err := m.LoadState()
	if err != nil {
		return err
	}
	items := state.Node.LandingAccesses
	u.pageHeader("Xray（Docker）", "管理落地接入")
	if len(items) == 0 {
		fmt.Fprintln(u.out, "尚未配置落地接入。")
		return nil
	}
	for i, item := range items {
		u.menuBadge(strconv.Itoa(i+1), item.Name, "["+enabledLabel(item.Enabled)+"]")
	}
	u.menuChoice("0/q", "返回")
	choice, err := u.chooseNumber("请选择落地接入", 0, len(items), 1)
	if err != nil || choice == 0 {
		if err == nil {
			return errCancelled
		}
		return err
	}
	item := items[choice-1]
	u.clearScreen()
	u.pageHeader("Xray（Docker）", "管理落地接入", item.Name)
	u.menuChoice("1", "显示可复制的落地连接文本")
	action := "停用接入"
	if !item.Enabled {
		action = "启用接入"
	}
	u.menuChoice("2", action)
	u.menuChoice("3", "删除接入")
	u.menuChoice("0/q", "返回")
	selected, err := u.chooseNumber("请选择", 0, 3, 0)
	if err != nil || selected == 0 {
		if err == nil {
			return errCancelled
		}
		return err
	}
	if selected == 1 {
		peer, err := m.Peer(item.Name)
		if err != nil {
			return err
		}
		b, _ := jsonBytes(peer)
		fmt.Fprint(u.out, string(b))
		return nil
	}
	command := map[int]string{2: "disable", 3: "remove"}[selected]
	if item.Enabled && selected == 2 {
		command = "disable"
	} else if !item.Enabled && selected == 2 {
		command = "enable"
	}
	return m.ChangeLink(ctx, "landing", item.Name, command)
}

func (m *Manager) printLinkGraph(u *ui, node model.Node) error {
	u.pageHeader("Xray（Docker）", "流量关系")
	fmt.Fprintln(u.out, "普通用户\n  └─ direct（本机出口）")
	for _, link := range node.RelayLinks {
		fmt.Fprintf(u.out, "中转用户 %s [%s]\n  └─ %s:%d · %s\n", link.UserName, enabledLabel(link.Enabled), link.Upstream.Server, link.Upstream.Port, strings.ToUpper(model.NormalizeLandingSecurity(link.Upstream.Security)))
	}
	for _, access := range node.LandingAccesses {
		protocol := strings.ToUpper(model.NormalizeLandingSecurity(access.Security)) + "（当前端口）"
		if !access.UsesPrimaryInbound() {
			protocol = fmt.Sprintf("TLS :%d", access.Port)
		}
		fmt.Fprintf(u.out, "落地接入 %s [%s · %s]\n  └─ direct（本机出口）\n", access.UserName, enabledLabel(access.Enabled), protocol)
	}
	return u.pause()
}

func linkProtocolDisplay(securities []string) string {
	if len(securities) == 0 {
		return "未使用"
	}
	hasReality, hasTLS := false, false
	var others []string
	seenOther := map[string]struct{}{}
	for _, security := range securities {
		switch model.NormalizeLandingSecurity(security) {
		case model.LandingSecurityTLS:
			hasTLS = true
		case model.LandingSecurityReality:
			hasReality = true
		default:
			label := landingProtocolLabel(security)
			if _, exists := seenOther[label]; exists {
				continue
			}
			seenOther[label] = struct{}{}
			others = append(others, label)
		}
	}
	parts := make([]string, 0, 2+len(others))
	if hasReality {
		parts = append(parts, landingProtocolLabel(model.LandingSecurityReality))
	}
	if hasTLS {
		parts = append(parts, landingProtocolLabel(model.LandingSecurityTLS))
	}
	return strings.Join(append(parts, others...), " · ")
}

func landingProtocolLabel(security string) string {
	switch model.NormalizeLandingSecurity(security) {
	case model.LandingSecurityTLS:
		return "VLESS + RAW + TLS + Vision"
	case model.LandingSecurityReality:
		return "VLESS + RAW + REALITY + Vision"
	default:
		return strings.ToUpper(strings.TrimSpace(security))
	}
}

func (m *Manager) changeLinkInteractive(ctx context.Context, u *ui, kind, name string, enabled bool) error {
	u.clearScreen()
	title := "管理中转线路"
	if kind == "landing" {
		title = "管理落地接入"
	}
	u.pageHeader("Xray（Docker）", title, name)
	action := "停用线路"
	if !enabled {
		action = "启用线路"
	}
	u.menuChoice("1", action)
	u.menuChoice("2", "删除线路")
	u.menuChoice("0/q", "返回")
	choice, err := u.chooseNumber("请选择", 0, 2, 0)
	if err != nil || choice == 0 {
		if err == nil {
			return errCancelled
		}
		return err
	}
	command := "disable"
	if !enabled {
		command = "enable"
	}
	if choice == 2 {
		if err = u.confirm("删除线路 " + name + "？"); err != nil {
			return err
		}
		command = "remove"
	}
	return m.ChangeLink(ctx, kind, name, command)
}
