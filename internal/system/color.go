package system

import (
	"io"
	"os"
	"strings"
	"sync"
)

const (
	ansiReset       = "\x1b[0m"
	ansiBoldOrange  = "\x1b[1;38;5;208m"
	ansiOrange      = "\x1b[38;5;208m"
	ansiBoldGreen   = "\x1b[1;32m"
	ansiBoldYellow  = "\x1b[1;33m"
	ansiBoldRed     = "\x1b[1;31m"
	ansiDim         = "\x1b[2m"
	ansiYellow      = "\x1b[33m"
	ansiBlue        = "\x1b[34m"
	ansiMagenta     = "\x1b[35m"
	ansiBrightBlack = "\x1b[90m"
)

type colorWriter struct {
	mu          sync.Mutex
	output      io.Writer
	enabled     bool
	atLineStart bool
}

// NewTerminalColorWriter adds semantic colors only when output is a terminal.
// PROXYFORGE_COLOR=always/never overrides automatic color detection, while
// NO_COLOR always disables colors unless PROXYFORGE_COLOR=always is explicit.
func NewTerminalColorWriter(output io.Writer) io.Writer {
	return NewColorWriter(output, terminalColorEnabled(output))
}

// NewColorWriter is primarily useful when the caller already knows whether
// colors should be enabled. Disabled writers are returned unchanged.
func NewColorWriter(output io.Writer, enabled bool) io.Writer {
	if output == nil {
		output = io.Discard
	}
	if !enabled {
		return output
	}
	if existing, ok := output.(*colorWriter); ok {
		return existing
	}
	return &colorWriter{output: output, enabled: true, atLineStart: true}
}

func terminalColorEnabled(output io.Writer) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("PROXYFORGE_COLOR"))) {
	case "always", "1", "true", "yes":
		return true
	case "never", "0", "false", "no":
		return false
	}
	if _, disabled := os.LookupEnv("NO_COLOR"); disabled {
		return false
	}
	if strings.EqualFold(strings.TrimSpace(os.Getenv("TERM")), "dumb") {
		return false
	}
	return WriterInteractive(output)
}

// WriterInteractive reports whether a writer ultimately targets a character
// device, including when it is wrapped by the terminal color writer.
func WriterInteractive(output io.Writer) bool {
	if reporter, ok := output.(interface{ IsTerminal() bool }); ok {
		return reporter.IsTerminal()
	}
	file, ok := output.(*os.File)
	if !ok {
		return false
	}
	info, err := file.Stat()
	return err == nil && (info.Mode()&os.ModeCharDevice) != 0
}

func (w *colorWriter) IsTerminal() bool {
	return WriterInteractive(w.output)
}

func (w *colorWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	if !w.enabled || len(p) == 0 {
		return w.output.Write(p)
	}

	text := string(p)
	var decorated strings.Builder
	for len(text) > 0 {
		end := strings.IndexByte(text, '\n')
		if end < 0 {
			decorated.WriteString(decorateOutputFragment(text, w.atLineStart))
			w.atLineStart = outputPromptEndsLine(text)
			break
		}
		fragment := text[:end+1]
		decorated.WriteString(decorateOutputFragment(fragment, w.atLineStart))
		w.atLineStart = true
		text = text[end+1:]
	}
	if strings.Contains(string(p), "\x1b[H\x1b[2J") {
		w.atLineStart = true
	}
	decoratedText := decorated.String()
	written, err := io.WriteString(w.output, decoratedText)
	if err != nil {
		return 0, err
	}
	if written != len(decoratedText) {
		return 0, io.ErrShortWrite
	}
	return len(p), nil
}

func decorateOutputFragment(fragment string, atLineStart bool) string {
	lineEnding := ""
	body := fragment
	if strings.HasSuffix(body, "\n") {
		lineEnding = "\n"
		body = strings.TrimSuffix(body, "\n")
		if strings.HasSuffix(body, "\r") {
			body = strings.TrimSuffix(body, "\r")
			lineEnding = "\r\n"
		}
	}

	if atLineStart {
		trimmed := strings.TrimSpace(body)
		switch {
		case strings.HasPrefix(trimmed, "╭─ "), strings.HasPrefix(trimmed, "╰─"):
			body = wrapANSI(ansiBoldOrange, body)
		case strings.HasPrefix(trimmed, "│ "):
			if strings.Contains(trimmed, "[版本 ") {
				body = decorateHomeSubtitle(body)
			} else {
				body = decorateConfigCardLine(body)
			}
		case isRepeated(trimmed, '='):
			body = wrapANSI(ansiBoldOrange, body)
		case isRepeated(trimmed, '-'):
			body = wrapANSI(ansiOrange, body)
		case strings.HasPrefix(trimmed, "错误:") || strings.HasPrefix(trimmed, "错误："):
			body = wrapANSI(ansiBoldRed, body)
		case strings.HasPrefix(trimmed, "警告："):
			body = wrapANSI(ansiBoldYellow, body)
		case strings.HasPrefix(trimmed, "提示："):
			body = wrapANSI(ansiBlue, body)
		case isConfirmationPrompt(trimmed):
			body = wrapANSI(ansiBoldYellow, body)
		case isDisplayHeading(trimmed):
			body = wrapANSI(ansiBoldOrange, body)
		case isInputPrompt(trimmed):
			body = wrapANSI(ansiBlue, body)
		case strings.HasPrefix(trimmed, "- "):
			indent := strings.Index(body, "-")
			body = body[:indent] + wrapANSI(ansiOrange, "-") + body[indent+1:]
		default:
			body = decorateNumberedChoice(body)
		}
	}
	body = decorateMenuDescription(body)
	body = decorateSourceLabels(body)
	return body + lineEnding
}

func decorateConfigCardLine(value string) string {
	const prefix = "│ "
	start := strings.Index(value, prefix)
	if start < 0 {
		return value
	}
	labels := []string{
		"HTTP Host", "DNS 设置", "SNI 防护", "严格模式", "服务状态", "日志级别", "运行用户", "出站 IP", "回落 IP", "SNI",
	}
	rest := value[start+len(prefix):]
	for _, label := range labels {
		if !strings.HasPrefix(rest, label) {
			continue
		}
		after := rest[len(label):]
		valueStart := 0
		for valueStart < len(after) && after[valueStart] == ' ' {
			valueStart++
		}
		if valueStart == 0 {
			return value
		}
		spaces := after[:valueStart]
		field := after[valueStart:]
		return value[:start] + wrapANSI(ansiOrange, "│") + " " + label + spaces + decorateConfigCardValue(label, field)
	}
	return value
}

func decorateConfigCardValue(label, value string) string {
	switch value {
	case "运行中", "已开启", "仅限 SNI":
		return wrapANSI(ansiOrange, value)
	case "已停止":
		return wrapANSI(ansiBoldYellow, value)
	case "已失败", "无法读取":
		return wrapANSI(ansiBoldRed, value)
	case "未开启", "未生成", "不限制":
		return wrapANSI(ansiBrightBlack, value)
	}
	if label == "SNI" {
		return wrapANSI(ansiBoldOrange, value)
	}
	return value
}

func decorateHomeSubtitle(value string) string {
	badgeStart := strings.Index(value, "[版本 ")
	if badgeStart < 0 {
		return wrapANSI(ansiOrange, value)
	}
	return wrapANSI(ansiOrange, value[:badgeStart]) + wrapANSI(ansiBoldGreen, value[badgeStart:])
}

func decorateSourceLabels(value string) string {
	labels := []struct {
		text  string
		color string
	}{
		{"[步骤]", ansiBoldOrange},
		{"[信息]", ansiBlue},
		{"[提示]", ansiBlue},
		{"[警告]", ansiBoldYellow},
		{"[结果]", ansiBoldGreen},
		{"[错误]", ansiBoldRed},
		{"[系统命令/输出]", ansiBrightBlack},
		{"[官方脚本/信息]", ansiBlue},
		{"[官方脚本/风险]", ansiBoldYellow},
		{"[默认]", ansiBoldYellow},
		{"[已安装]", ansiOrange},
		{"[未安装]", ansiBrightBlack},
		{"[当前]", ansiOrange},
	}
	value = colorizeLabel(value, "[命令]", commandLabelColor(value))
	value = colorizeLabel(value, "[状态]", statusLabelColor(value))
	for _, label := range labels {
		value = colorizeLabel(value, label.text, label.color)
	}
	value = colorizeLabel(value, "[Bash]", ansiBlue)
	for start := strings.Index(value, "[服务日志/"); start >= 0; {
		relativeEnd := strings.IndexByte(value[start:], ']')
		if relativeEnd < 0 {
			break
		}
		end := start + relativeEnd + 1
		label := value[start:end]
		colored := wrapANSI(ansiMagenta, label)
		value = value[:start] + colored + value[end:]
		next := start + len(colored)
		relativeStart := strings.Index(value[next:], "[服务日志/")
		if relativeStart < 0 {
			break
		}
		start = next + relativeStart
	}
	return value
}

func colorizeLabel(value, label, color string) string {
	return strings.ReplaceAll(value, label, wrapANSI(color, label))
}

func commandLabelColor(value string) string {
	switch {
	case strings.Contains(value, "命令完成"):
		return ansiBoldGreen
	case strings.Contains(value, "命令失败"):
		return ansiBoldRed
	case strings.Contains(value, "命令已停止"):
		return ansiBoldYellow
	default:
		return ansiBlue
	}
}

func statusLabelColor(value string) string {
	switch {
	case strings.Contains(value, "执行完成"):
		return ansiBoldGreen
	case strings.Contains(value, "失败") || strings.Contains(value, "错误"):
		return ansiBoldRed
	case strings.Contains(value, "停止") || strings.Contains(value, "取消"):
		return ansiBoldYellow
	default:
		return ansiMagenta
	}
}

func decorateNumberedChoice(value string) string {
	trimmedLeft := strings.TrimLeft(value, " \t")
	indent := len(value) - len(trimmedLeft)
	digitEnd := 0
	for digitEnd < len(trimmedLeft) && trimmedLeft[digitEnd] >= '0' && trimmedLeft[digitEnd] <= '9' {
		digitEnd++
	}
	if digitEnd == 0 {
		return value
	}
	prefixEnd := digitEnd
	if strings.HasPrefix(strings.ToLower(trimmedLeft[prefixEnd:]), "/q") {
		prefixEnd += len("/q")
	}
	remainderStart := prefixEnd
	if remainderStart < len(trimmedLeft) && trimmedLeft[remainderStart] == ')' {
		remainderStart++
	}
	if remainderStart >= len(trimmedLeft) || (trimmedLeft[remainderStart] != ' ' && trimmedLeft[remainderStart] != '\t') {
		return value
	}
	prefix := trimmedLeft[:prefixEnd]
	remainder := trimmedLeft[remainderStart:]
	trimmedRemainder := strings.TrimLeft(remainder, " \t")
	spacing := remainder[:len(remainder)-len(trimmedRemainder)]
	if isExitChoice(trimmedRemainder) {
		return value[:indent] + wrapANSI(ansiBoldYellow, prefix) + spacing + wrapANSI(ansiDim, trimmedRemainder)
	}
	if isDangerousChoice(trimmedRemainder) {
		return value[:indent] + wrapANSI(ansiYellow, prefix) + spacing + decorateMenuDescription(trimmedRemainder)
	}
	if isReturnChoice(trimmedRemainder) {
		return value[:indent] + wrapANSI(ansiBoldYellow, prefix) + spacing + wrapANSI(ansiDim, trimmedRemainder)
	}
	if fields := strings.Fields(trimmedRemainder); len(fields) > 0 && strings.Contains(fields[0], ".") && !strings.ContainsAny(fields[0], "/:") {
		domain := fields[0]
		trimmedRemainder = wrapANSI(ansiBoldOrange, domain) + trimmedRemainder[len(domain):]
	}
	return value[:indent] + wrapANSI(ansiBlue, prefix) + spacing + decorateMenuDescription(trimmedRemainder)
}

func decorateMenuDescription(value string) string {
	separator := "  -- "
	start := strings.Index(value, separator)
	if start < 0 {
		return value
	}
	return value[:start] + "  " + wrapANSI(ansiBrightBlack, value[start+2:])
}

func isExitChoice(value string) bool {
	return value == "退出" || strings.HasPrefix(value, "退出 ")
}

func isReturnChoice(value string) bool {
	return value == "返回" || strings.HasPrefix(value, "返回") || strings.Contains(value, "返回主菜单")
}

func isDangerousChoice(value string) bool {
	return strings.Contains(value, "卸载") || strings.Contains(value, "删除") || strings.Contains(value, "清理数据")
}

func isRepeated(value string, expected byte) bool {
	if len(value) < 3 {
		return false
	}
	for index := range value {
		if value[index] != expected {
			return false
		}
	}
	return true
}

func isDisplayHeading(value string) bool {
	if value == "请选择要管理的内核" || value == "REALITY SNI 候选检测" || value == "公网地址获取方式" || value == "配置模式" {
		return true
	}
	for _, marker := range []string{"管理菜单", "服务管理：", "服务端配置管理：", "DNS 设置：", "查看客户端配置：", "生成服务端配置：", "安装/升级内核：", "实时日志：", "设置日志级别：", "操作确认：", "危险操作确认：", "配置确认："} {
		if strings.Contains(value, marker) {
			return true
		}
	}
	return strings.HasSuffix(value, "：") && !strings.HasPrefix(value, "[")
}

func isConfirmationPrompt(value string) bool {
	return strings.HasPrefix(value, "请输入 yes/y") || strings.HasPrefix(value, "确认") || strings.Contains(value, "确认？")
}

func isInputPrompt(value string) bool {
	return !strings.Contains(value, "\x1b[") && (strings.HasSuffix(value, ":") || strings.HasSuffix(value, ": ") || strings.HasSuffix(value, "：") || strings.HasSuffix(value, "： "))
}

func outputPromptEndsLine(value string) bool {
	trimmed := strings.TrimRight(value, " \t")
	return strings.HasSuffix(trimmed, ":") || strings.HasSuffix(trimmed, "：") || strings.HasSuffix(trimmed, "……")
}

func wrapANSI(color, value string) string {
	return color + value + ansiReset
}
