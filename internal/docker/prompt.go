package docker

import (
	"fmt"
	"io"
	"strconv"
	"strings"

	"proxyforge/internal/docker/model"
)

const (
	menuDescriptionColumn = 18
	menuStatusColumn      = 20
	pageHeaderRule        = "╰──────────────────────────────────────────────"
)

type confirmSection struct {
	title string
	items []string
}

func (u *ui) pageHeader(parts ...string) {
	clean := []string{"ProxyForge"}
	for _, part := range parts {
		if part = strings.TrimSpace(part); part != "" {
			clean = append(clean, part)
		}
	}
	fmt.Fprintf(u.out, "╭─ %s\n", strings.Join(clean, "  ›  "))
	fmt.Fprintln(u.out, pageHeaderRule)
	fmt.Fprintln(u.out)
}

func (u *ui) labeledCard(title string, rows [][2]string) {
	const labelWidth = 10
	fmt.Fprintf(u.out, "╭─ %s\n", title)
	for _, row := range rows {
		u.writeCardRow(row[0], row[1], labelWidth)
	}
	fmt.Fprintln(u.out, pageHeaderRule)
	fmt.Fprintln(u.out)
}

func (u *ui) instanceCard(name, badge, version, detail string, rows [][2]string) {
	fmt.Fprintln(u.out, "╭─ 当前实例")
	padding := menuStatusColumn - displayWidth(name)
	if padding < 2 {
		padding = 2
	}
	fmt.Fprintf(u.out, "│ %s%s%s\n", name, strings.Repeat(" ", padding), badge)
	if version != "" {
		fmt.Fprintf(u.out, "│ %s\n", version)
	}
	if detail != "" {
		fmt.Fprintf(u.out, "│ %s\n", detail)
	}
	for _, row := range rows {
		u.writeCardRow(row[0], row[1], 10)
	}
	fmt.Fprintln(u.out, pageHeaderRule)
	fmt.Fprintln(u.out)
}

func (u *ui) writeCardRow(label, value string, labelWidth int) {
	padding := labelWidth - displayWidth(label)
	if padding < 1 {
		padding = 1
	}
	fmt.Fprintf(u.out, "│ %s%s%s\n", label, strings.Repeat(" ", padding), value)
}

func (u *ui) menuChoice(key, label string) {
	title, description := splitMenuLabel(label)
	var line strings.Builder
	fmt.Fprintf(&line, "  %-3s %s", key, title)
	if description != "" {
		padding := menuDescriptionColumn - displayWidth(title)
		if padding < 2 {
			padding = 2
		}
		fmt.Fprintf(&line, "%s-- %s", strings.Repeat(" ", padding), description)
	}
	fmt.Fprintln(u.out, line.String())
}

func (u *ui) menuBadge(key, title, badge string) {
	padding := menuStatusColumn - displayWidth(title)
	if padding < 2 {
		padding = 2
	}
	fmt.Fprintf(u.out, "  %-3s %s%s%s\n", key, title, strings.Repeat(" ", padding), badge)
}

func (u *ui) chooseNumber(label string, min, max, def int) (int, error) {
	return u.chooseNumberInput(label, min, max, def, false)
}

func (u *ui) chooseNumberCancelable(label string, min, max, def int) (int, error) {
	return u.chooseNumberInput(label, min, max, def, true)
}

func (u *ui) chooseNumberInput(label string, min, max, def int, cancelable bool) (int, error) {
	invalidShown := false
	firstPrompt := true
	prompt := "❯ 请选择"
	if detailStart := strings.Index(label, "（"); detailStart >= 0 {
		prompt += label[detailStart:]
	}
	for {
		if firstPrompt {
			fmt.Fprintln(u.out)
			firstPrompt = false
		}
		if def >= min && def <= max {
			fmt.Fprintf(u.out, "%s [%d]：", prompt, def)
		} else {
			fmt.Fprintf(u.out, "%s：", prompt)
		}
		line, err := u.reader.ReadString('\n')
		if err != nil && len(line) == 0 {
			return 0, err
		}
		pasted := u.discardBufferedInput()
		value := strings.TrimSpace(line)
		if cancelable && (strings.EqualFold(value, "q") || (value == "0" && min > 0)) {
			return 0, errCancelled
		}
		if strings.EqualFold(value, "q") && min == 0 {
			return 0, nil
		}
		if value == "" && def >= min && def <= max {
			return def, nil
		}
		choice, parseErr := strconv.Atoi(value)
		if parseErr == nil && choice >= min && choice <= max {
			return choice, nil
		}
		if u.interactive {
			eraseChoiceRetry(u.out, false)
		}
		if pasted {
			fmt.Fprintln(u.out, "检测到粘贴了多行内容，已忽略多余输入。")
			invalidShown = true
			continue
		}
		if value == "" || invalidShown {
			continue
		}
		if cancelable {
			fmt.Fprintf(u.out, "无效选择，请输入 %d 到 %d 之间的数字，或输入 q/0 返回上级菜单。\n", min, max)
		} else {
			fmt.Fprintf(u.out, "无效选择，请输入 %d 到 %d 之间的数字。\n", min, max)
		}
		invalidShown = true
	}
}

func eraseChoiceRetry(w io.Writer, invalidShown bool) {
	erasePreviousLine := "\033[1A\r\033[2K"
	fmt.Fprint(w, erasePreviousLine)
	if invalidShown {
		fmt.Fprint(w, erasePreviousLine)
	}
}

func (u *ui) ask(label, def string) (string, error) {
	if def != "" {
		fmt.Fprintf(u.out, "%s [%s]: ", label, def)
	} else {
		fmt.Fprintf(u.out, "%s: ", label)
	}
	line, err := u.reader.ReadString('\n')
	if err != nil && len(line) == 0 {
		return "", err
	}
	u.discardBufferedInput()
	line = strings.TrimSpace(line)
	if strings.EqualFold(line, "q") {
		return "", errCancelled
	}
	if line == "" {
		line = def
	}
	return line, nil
}

func (u *ui) confirm(message string) error {
	return u.confirmChoice(message, false)
}

func (u *ui) confirmDefaultYes(message string) error {
	return u.confirmChoice(message, true)
}

func (u *ui) confirmChoice(message string, defaultYes bool) error {
	fmt.Fprintln(u.out, strings.TrimSpace(message))
	invalidShown := false
	for {
		if defaultYes {
			fmt.Fprint(u.out, "确认操作？[Y/1=确认，Q/0=返回，回车=确认]： ")
		} else {
			fmt.Fprint(u.out, "确认操作？[Y/1=确认，Q/0=返回]： ")
		}
		line, err := u.reader.ReadString('\n')
		if err != nil && len(line) == 0 {
			return err
		}
		pasted := u.discardBufferedInput()
		value := strings.TrimSpace(line)
		if strings.EqualFold(value, "q") || value == "0" {
			return errCancelled
		}
		if defaultYes && value == "" {
			return nil
		}
		if strings.EqualFold(value, "yes") || strings.EqualFold(value, "y") || value == "1" {
			return nil
		}
		if u.interactive {
			eraseChoiceRetry(u.out, false)
		}
		if pasted {
			fmt.Fprintln(u.out, "检测到粘贴了多行内容，已忽略多余输入。")
			invalidShown = true
			continue
		}
		if value == "" || invalidShown {
			continue
		}
		fmt.Fprintln(u.out, "输入无效，请输入 yes/y 或 1 确认，或输入 q/0 返回当前菜单。")
		invalidShown = true
	}
}

func (u *ui) confirmationPanel(title string, details []string, sections ...confirmSection) {
	u.clearScreen()
	u.pageHeader(title)
	for _, detail := range details {
		fmt.Fprintln(u.out, detail)
	}
	for _, section := range sections {
		if section.title == "" || len(section.items) == 0 {
			continue
		}
		fmt.Fprintf(u.out, "\n%s：\n", section.title)
		for _, item := range section.items {
			fmt.Fprintf(u.out, "  - %s\n", item)
		}
	}
	fmt.Fprintln(u.out, "----------------------------------------")
}

func (u *ui) discardBufferedInput() bool {
	if u.reader == nil || !u.interactive {
		return false
	}
	discarded := false
	for n := u.reader.Buffered(); n > 0; n = u.reader.Buffered() {
		buf := make([]byte, n)
		if _, err := io.ReadFull(u.reader, buf); err != nil {
			return discarded
		}
		discarded = true
	}
	return discarded
}

func (m *Manager) printMenuError(err error) {
	fmt.Fprintf(m.errOut, "[错误] 操作失败：%v\n", err)
}

func splitMenuLabel(label string) (string, string) {
	label = strings.TrimSpace(label)
	if !strings.HasSuffix(label, "）") {
		return label, ""
	}
	start := strings.Index(label, "（")
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

func cardText(label string) string {
	title, description := splitMenuLabel(label)
	if description == "" {
		return title
	}
	return title + "  -- " + description
}

func displayWidth(value string) int {
	width := 0
	for _, r := range value {
		if isWideRune(r) {
			width += 2
		} else {
			width++
		}
	}
	return width
}

func isWideRune(r rune) bool {
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

func channelLabel(channel string) string {
	switch channel {
	case "stable":
		return "稳定版"
	case "latest":
		return "latest"
	case "pinned":
		return "指定版本"
	default:
		return "无法读取"
	}
}

func dnsLabel(profile string) string {
	switch profile {
	case model.DNSProfileSystem:
		return "系统 DNS（推荐）"
	case model.DNSProfilePublicCloudflare:
		return "Cloudflare DNS（1.1.1.1 + 8.8.8.8）"
	case model.DNSProfilePublicGoogle:
		return "Google DNS（8.8.8.8 + 1.1.1.1）"
	case model.DNSProfileDoHCloudflare:
		return "Cloudflare DoH（同时配置 Google）"
	case model.DNSProfileDoHGoogle:
		return "Google DoH（同时配置 Cloudflare）"
	case model.DNSProfileCloudflare:
		return "Cloudflare DNS（仅 1.1.1.1，旧单地址配置）"
	case model.DNSProfileGoogle:
		return "Google DNS（仅 8.8.8.8，旧单地址配置）"
	case "none":
		return "未配置内核 DNS（简化模式）"
	case "implicit-system":
		return "隐式系统 DNS（尚未写入配置）"
	case "custom":
		return "自定义 DNS"
	default:
		return profile
	}
}

func logLevelLabel(level string) string {
	switch level {
	case "debug":
		return "debug（调试信息）"
	case "info":
		return "info（常规运行信息）"
	case "warning":
		return "warning（警告及错误（默认））"
	case "error":
		return "error（仅错误）"
	case "off":
		return "off（关闭访问日志和错误日志）"
	default:
		return level
	}
}

func outboundLabel(strategy string) string {
	switch strategy {
	case model.OutboundIPPreferIPv4:
		return "优先 IPv4"
	case model.OutboundIPPreferIPv6:
		return "优先 IPv6"
	case model.OutboundIPIPv4Only:
		return "仅 IPv4"
	case model.OutboundIPIPv6Only:
		return "仅 IPv6"
	case model.OutboundIPUnset:
		return "未设置（双栈）"
	case "custom":
		return "自定义出站 IP 策略"
	default:
		return strategy
	}
}

func outboundChoice(strategy string) string {
	hint := ""
	switch strategy {
	case model.OutboundIPPreferIPv4:
		hint = "有 IPv4 后连不上也不改走 IPv6"
	case model.OutboundIPPreferIPv6:
		hint = "有 IPv6 后连不上也不改走 IPv4"
	case model.OutboundIPIPv4Only:
		hint = "只使用 IPv4"
	case model.OutboundIPIPv6Only:
		hint = "只使用 IPv6"
	case model.OutboundIPUnset:
		hint = "先 IPv4，300ms 后竞速 IPv6"
	}
	title := outboundLabel(strategy)
	if strategy == model.OutboundIPUnset {
		title = "默认"
	}
	if hint == "" {
		return title
	}
	return title + "（" + hint + "）"
}

func enabledLabel(enabled bool) string {
	if enabled {
		return "已启用"
	}
	return "已停用"
}

func accessModeLabel(mode string) string {
	if mode == model.AccessModeWhitelist {
		return "仅允许白名单访问"
	}
	return "默认模式"
}

func accessSwitchLabel(policy model.AccessPolicy) string {
	if policy.Disabled {
		return "已关闭（保留模式和名单）"
	}
	return "已启用"
}

func accessEntryLabel(entry model.AccessEntry) string {
	if entry.Type == model.AccessEntryDomain {
		if entry.IncludeSubdomains {
			return entry.Value + "（包括子域名）"
		}
		return entry.Value + "（精确域名）"
	}
	return entry.Value
}

func linkSummary(hasNode, known bool, total, enabled int) string {
	if !hasNode {
		return "未生成"
	}
	if !known {
		return "无法读取"
	}
	if total < 0 {
		total = 0
	}
	if enabled < 0 {
		enabled = 0
	}
	if enabled > total {
		enabled = total
	}
	if enabled == 0 {
		if total == 0 {
			return "未开启  -- 0 条规则"
		}
		return fmt.Sprintf("未开启  -- 0 条启用 · %d 条已停用", total)
	}
	if enabled == total {
		return fmt.Sprintf("已开启  -- %d 条规则", enabled)
	}
	return fmt.Sprintf("已开启  -- %d 条规则 · %d 条已停用", enabled, total-enabled)
}

func countEnabled[T any](items []T, enabled func(T) bool) (int, int) {
	on := 0
	for _, item := range items {
		if enabled(item) {
			on++
		}
	}
	return len(items), on
}
