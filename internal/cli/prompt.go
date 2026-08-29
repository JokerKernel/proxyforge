package cli

import (
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"proxyforge/internal/system"
)

type confirmationSection struct {
	title string
	items []string
}

func (c *commandSet) chooseNumber(label string, min, max, def int) (int, error) {
	return c.chooseNumberInput(label, min, max, def, false)
}

func (c *commandSet) chooseNumberCancelable(label string, min, max, def int) (int, error) {
	return c.chooseNumberInput(label, min, max, def, true)
}

func (c *commandSet) chooseNumberInput(label string, min, max, def int, cancelable bool) (int, error) {
	invalidShown := false
	firstPrompt := true
	prompt := "❯ 请选择"
	if detailStart := strings.Index(label, "（"); detailStart >= 0 {
		prompt += label[detailStart:]
	}
	for {
		if firstPrompt {
			fmt.Fprintln(c.out)
			firstPrompt = false
		}
		if def >= min && def <= max {
			fmt.Fprintf(c.out, "%s [%d]：", prompt, def)
		} else {
			fmt.Fprintf(c.out, "%s：", prompt)
		}
		line, err := c.reader.ReadString('\n')
		if err != nil && len(line) == 0 {
			return 0, err
		}
		pasted := c.discardBufferedInput()
		value := strings.TrimSpace(line)
		if cancelable && (strings.EqualFold(value, "q") || (value == "0" && min > 0)) {
			return 0, errReturnToMenu
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
		if c.interactiveUI() {
			eraseChoiceRetry(c.out, false)
		}
		if pasted {
			fmt.Fprintln(c.out, "检测到粘贴了多行内容，已忽略多余输入。")
			invalidShown = true
			continue
		}
		if value == "" || invalidShown {
			continue
		}
		if cancelable {
			fmt.Fprintf(c.out, "无效选择，请输入 %d 到 %d 之间的数字，或输入 q/0 返回上级菜单。\n", min, max)
		} else {
			fmt.Fprintf(c.out, "无效选择，请输入 %d 到 %d 之间的数字。\n", min, max)
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

func (c *commandSet) printMenuError(err error) {
	fmt.Fprintf(c.errOut, "[错误] 操作失败：%v\n", err)
}

func (c *commandSet) askDefault(label, def string) string {
	value, _ := c.askDefaultInput(label, def, false)
	return value
}

func (c *commandSet) askDefaultCancelable(label, def string) (string, error) {
	return c.askDefaultInput(label, def, true)
}

func (c *commandSet) askDefaultInput(label, def string, cancelable bool) (string, error) {
	if def != "" {
		fmt.Fprintf(c.out, "%s [%s]: ", label, def)
	} else {
		fmt.Fprintf(c.out, "%s: ", label)
	}
	line, err := c.reader.ReadString('\n')
	c.discardBufferedInput()
	if err == nil || len(line) > 0 {
		v := strings.TrimSpace(line)
		if cancelable && strings.EqualFold(v, "q") {
			return "", errReturnToMenu
		}
		if v != "" {
			return v, nil
		}
	}
	return def, nil
}

func (c *commandSet) confirm(message string) (bool, error) {
	return c.confirmInput(message, false, false)
}

func (c *commandSet) confirmCancelable(message string) (bool, error) {
	return c.confirmInput(message, true, false)

}

func (c *commandSet) confirmCancelableDefaultYes(message string) (bool, error) {
	return c.confirmInput(message, true, true)
}

func (c *commandSet) printConfirmationPanel(title string, details []string, sections ...confirmationSection) {
	c.clearScreen()
	c.printPageHeader(title)
	for _, detail := range details {
		fmt.Fprintln(c.out, detail)
	}
	for _, section := range sections {
		if section.title == "" || len(section.items) == 0 {
			continue
		}
		fmt.Fprintf(c.out, "\n%s：\n", section.title)
		for _, item := range section.items {
			fmt.Fprintf(c.out, "  - %s\n", item)
		}
	}
	fmt.Fprintln(c.out, "----------------------------------------")
}

func (c *commandSet) confirmInput(message string, cancelable, defaultYes bool) (bool, error) {
	fmt.Fprintln(c.out, strings.TrimSpace(message))
	invalidShown := false
	for {
		if defaultYes {
			fmt.Fprint(c.out, "确认操作？[Y/1=确认，Q/0=返回，回车=确认]： ")
		} else {
			fmt.Fprint(c.out, "确认操作？[Y/1=确认，Q/0=返回]： ")
		}
		line, err := c.reader.ReadString('\n')
		if err != nil && len(line) == 0 {
			return false, err
		}
		pasted := c.discardBufferedInput()
		value := strings.TrimSpace(line)
		if strings.EqualFold(value, "q") || value == "0" {
			return false, errReturnToMenu
		}
		if defaultYes && value == "" {
			return true, nil
		}
		if strings.EqualFold(value, "yes") || strings.EqualFold(value, "y") || value == "1" {
			return true, nil
		}
		if c.interactiveUI() {
			eraseChoiceRetry(c.out, false)
		}
		if pasted {
			fmt.Fprintln(c.out, "检测到粘贴了多行内容，已忽略多余输入。")
			invalidShown = true
			continue
		}
		if value == "" || invalidShown {
			continue
		}
		fmt.Fprintln(c.out, "输入无效，请输入 yes/y 或 1 确认，或输入 q/0 返回当前菜单。")
		invalidShown = true
	}
}

func readerInteractive(r io.Reader) bool {
	f, ok := r.(*os.File)
	if !ok {
		return false
	}
	info, err := f.Stat()
	return err == nil && (info.Mode()&os.ModeCharDevice) != 0
}

func writerInteractive(w io.Writer) bool {
	return system.WriterInteractive(w)
}

func (c *commandSet) interactiveUI() bool {
	return !c.yes && os.Getenv("TERM") != "dumb" && readerInteractive(c.in) && writerInteractive(c.out)
}

func (c *commandSet) clearScreen() {
	if c.interactiveUI() {
		clearTerminal(c.out)
	}
}

func clearTerminal(w io.Writer) {
	// CSI 2 J clears the visible screen; CSI 3 J also clears saved scrollback.
	// Both are needed for terminal frontends that otherwise retain every menu
	// redraw and make one configuration appear several times.
	fmt.Fprint(w, "\033[H\033[2J\033[3J")
}

func (c *commandSet) pauseForMenu() {
	if !c.interactiveUI() {
		return
	}
	fmt.Fprint(c.out, "\n按 Enter 返回菜单……")
	_, _ = c.reader.ReadString('\n')
	c.discardBufferedInput()
}

func (c *commandSet) discardBufferedInput() bool {
	if c.reader == nil || !(c.interactiveUI() || c.discardBurst) {
		return false
	}
	discarded := false
	for n := c.reader.Buffered(); n > 0; n = c.reader.Buffered() {
		buf := make([]byte, n)
		if _, err := io.ReadFull(c.reader, buf); err != nil {
			return discarded
		}
		discarded = true
	}
	return discarded
}

func netJoinHostPort(host, port string) string {
	if strings.Contains(host, ":") && !strings.HasPrefix(host, "[") {
		return "[" + host + "]:" + port
	}
	return host + ":" + port
}
