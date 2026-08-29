package cli

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
)

func TestConfirmAcceptsGlobalAffirmativeForms(t *testing.T) {
	for _, input := range []string{"yes\n", "y\n", "Y\n", "YES\n"} {
		t.Run(strings.TrimSpace(input), func(t *testing.T) {
			var out bytes.Buffer
			c := &commandSet{reader: bufio.NewReader(strings.NewReader(input)), out: &out}
			ok, err := c.confirm("confirm")
			if err != nil || !ok {
				t.Fatalf("input=%q confirmed=%v error=%v", input, ok, err)
			}
		})
	}
	t.Run("pasted lines are discarded", func(t *testing.T) {
		var out bytes.Buffer
		c := &commandSet{
			reader:       bufio.NewReader(strings.NewReader("nope\nyes\n1\n")),
			out:          &out,
			discardBurst: true,
		}
		_, err := c.confirm("confirm")
		if !errors.Is(err, io.EOF) {
			t.Fatalf("error=%v, want EOF after discarding the rest of the paste", err)
		}
		if !strings.Contains(out.String(), "检测到粘贴了多行内容，已忽略多余输入。") {
			t.Fatalf("paste hint missing: %q", out.String())
		}
	})
	t.Run("invalid input retries", func(t *testing.T) {
		var out bytes.Buffer
		c := &commandSet{reader: bufio.NewReader(strings.NewReader("n\n\nx\ny\n")), out: &out}
		ok, err := c.confirm("confirm")
		if err != nil || !ok || !strings.Contains(out.String(), "输入无效，请输入 yes/y") {
			t.Fatalf("confirmed=%v error=%v output=%q", ok, err, out.String())
		}
		if count := strings.Count(out.String(), "输入无效"); count != 1 {
			t.Fatalf("invalid message count=%d output=%q", count, out.String())
		}
	})
	t.Run("q returns menu", func(t *testing.T) {
		c := &commandSet{reader: bufio.NewReader(strings.NewReader("q\n")), out: io.Discard}
		ok, err := c.confirm("confirm")
		if !errors.Is(err, errReturnToMenu) || ok {
			t.Fatalf("confirmed=%v error=%v", ok, err)
		}
	})
}

func TestCancelableGenerateInputsRecognizeQ(t *testing.T) {
	t.Run("text", func(t *testing.T) {
		c := &commandSet{reader: bufio.NewReader(strings.NewReader("Q\n")), out: io.Discard}
		if _, err := c.askDefaultCancelable("value", "default"); !errors.Is(err, errReturnToMenu) {
			t.Fatalf("error=%v", err)
		}
	})
	t.Run("number", func(t *testing.T) {
		c := &commandSet{reader: bufio.NewReader(strings.NewReader("q\n")), out: io.Discard}
		if _, err := c.chooseNumberCancelable("choice", 1, 3, 1); !errors.Is(err, errReturnToMenu) {
			t.Fatalf("error=%v", err)
		}
	})
	t.Run("confirmation", func(t *testing.T) {
		c := &commandSet{reader: bufio.NewReader(strings.NewReader("q\n")), out: io.Discard}
		if _, err := c.confirmCancelable("confirm"); !errors.Is(err, errReturnToMenu) {
			t.Fatalf("error=%v", err)
		}
	})
}

func TestSNIConfirmationDefaultsToYes(t *testing.T) {
	var out bytes.Buffer
	c := &commandSet{reader: bufio.NewReader(strings.NewReader("\n")), out: &out}
	ok, err := c.confirmCancelableDefaultYes("确认 SNI 和 REALITY target？")
	if err != nil || !ok {
		t.Fatalf("confirmed=%v error=%v", ok, err)
	}
	if !strings.Contains(out.String(), "Y/1=确认") || !strings.Contains(out.String(), "Q/0=返回") || !strings.Contains(out.String(), "回车=确认") {
		t.Fatalf("default confirmation prompt=%q", out.String())
	}
}

func TestChooseNumberDiscardsPastedLines(t *testing.T) {
	var out bytes.Buffer
	c := &commandSet{
		reader:       bufio.NewReader(strings.NewReader("copied text\n2\n3\n4\n")),
		out:          &out,
		discardBurst: true,
	}
	_, err := c.chooseNumber("请选择", 0, 5, 0)
	if !errors.Is(err, io.EOF) {
		t.Fatalf("error=%v, want EOF after discarding the rest of the paste", err)
	}
	if count := strings.Count(out.String(), "❯ 请选择"); count != 2 {
		t.Fatalf("prompt count=%d, want 2 (original + one retry): %q", count, out.String())
	}
	if !strings.Contains(out.String(), "检测到粘贴了多行内容，已忽略多余输入。") {
		t.Fatalf("paste hint missing: %q", out.String())
	}
}

func TestChooseNumberKeepsFirstValidPastedChoice(t *testing.T) {
	var out bytes.Buffer
	c := &commandSet{
		reader:       bufio.NewReader(strings.NewReader("2\nleftover\n3\n")),
		out:          &out,
		discardBurst: true,
	}
	choice, err := c.chooseNumber("请选择", 0, 5, 0)
	if err != nil {
		t.Fatal(err)
	}
	if choice != 2 {
		t.Fatalf("choice=%d, want first pasted line", choice)
	}
	if strings.Contains(out.String(), "检测到粘贴") {
		t.Fatalf("valid first line should not warn about paste: %q", out.String())
	}
}

func TestChooseNumberRetriesInvalidInput(t *testing.T) {
	var out bytes.Buffer
	c := &commandSet{reader: bufio.NewReader(strings.NewReader("abc\n9\n2\n")), out: &out}
	choice, err := c.chooseNumber("请选择", 0, 5, 0)
	if err != nil {
		t.Fatal(err)
	}
	if choice != 2 {
		t.Fatalf("choice = %d, want 2", choice)
	}
	if count := strings.Count(out.String(), "无效选择"); count != 1 {
		t.Fatalf("invalid message count = %d, output=%q", count, out.String())
	}
	if strings.Contains(out.String(), "\x1b[") {
		t.Fatalf("redirected choice output contains ANSI controls: %q", out.String())
	}
}

func TestEraseChoiceRetryReplacesPromptAndPreviousError(t *testing.T) {
	const eraseLine = "\x1b[1A\r\x1b[2K"
	var first, retry bytes.Buffer
	eraseChoiceRetry(&first, false)
	eraseChoiceRetry(&retry, true)
	if first.String() != eraseLine {
		t.Fatalf("first invalid clear=%q", first.String())
	}
	if retry.String() != eraseLine+eraseLine {
		t.Fatalf("repeated invalid clear=%q", retry.String())
	}
}

func TestScreenControlsAreDisabledForRedirectedIO(t *testing.T) {
	input := strings.NewReader("kept\n")
	var out bytes.Buffer
	c := &commandSet{in: input, reader: bufio.NewReader(input), out: &out}
	c.clearScreen()
	c.pauseForMenu()
	if out.Len() != 0 {
		t.Fatalf("redirected output=%q", out.String())
	}
	line, err := c.reader.ReadString('\n')
	if err != nil || line != "kept\n" {
		t.Fatalf("redirected input was consumed: line=%q err=%v", line, err)
	}
}

func TestClearTerminalClearsVisibleScreenAndScrollback(t *testing.T) {
	var out bytes.Buffer
	clearTerminal(&out)
	if out.String() != "\x1b[H\x1b[2J\x1b[3J" {
		t.Fatalf("clear sequence=%q", out.String())
	}
}
