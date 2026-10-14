package docker

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"
)

// Capture both the transcript and the screen remaining after terminal clears.
type dockerTerminalOutput struct {
	bytes.Buffer
	screen string
}

func (*dockerTerminalOutput) IsTerminal() bool { return true }
func (w *dockerTerminalOutput) Write(p []byte) (int, error) {
	w.screen += string(p)
	if at := strings.LastIndex(w.screen, "\033[H\033[2J\033[3J"); at >= 0 {
		w.screen = w.screen[at+len("\033[H\033[2J\033[3J"):]
	}
	return w.Buffer.Write(p)
}

// Deliver one answer at a time and capture what the user sees at each prompt.
type dockerTerminalInput struct {
	answers []string
	index   int
	output  *dockerTerminalOutput
	screens []string
}

func (*dockerTerminalInput) IsTerminal() bool { return true }
func (r *dockerTerminalInput) Read(p []byte) (int, error) {
	if r.index == len(r.answers) {
		return 0, io.EOF
	}
	r.screens = append(r.screens, r.output.screen)
	answer := r.answers[r.index]
	n := copy(p, answer)
	if n != len(answer) {
		panic("terminal test answer exceeds reader buffer")
	}
	r.index++
	return n, nil
}

func TestDockerMenusReplacePreviousScreen(t *testing.T) {
	t.Setenv("TERM", "xterm")
	m, _ := testManager(t)
	var out dockerTerminalOutput
	m.out, m.errOut = &out, &out
	in := &dockerTerminalInput{answers: []string{"2\n", "0\n", "4\n", "0\n", "0\n"}, output: &out}
	if err := m.Menu(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	want := []string{"╭─ 当前实例", "›  服务端配置", "╭─ 当前实例", "›  服务管理", "╭─ 当前实例"}
	if len(in.screens) != len(want) {
		t.Fatalf("unexpected prompts: %d", len(in.screens))
	}
	for i, screen := range in.screens {
		if !strings.Contains(screen, want[i]) {
			t.Fatalf("prompt %d missing current menu %q: %s", i, want[i], screen)
		}
		for _, other := range []string{"╭─ 当前实例", "›  服务端配置", "›  服务管理"} {
			if other != want[i] && strings.Contains(screen, other) {
				t.Fatalf("prompt %d retained previous menu %q: %s", i, other, screen)
			}
		}
	}
	if strings.Contains(out.String(), "按 Enter") {
		t.Fatal("submenu navigation should return directly without a pause")
	}
}

func TestDockerMenuResultsWaitBeforeRedraw(t *testing.T) {
	for _, showConfig := range []bool{false, true} {
		t.Run(map[bool]string{false: "error", true: "config"}[showConfig], func(t *testing.T) {
			t.Setenv("TERM", "xterm")
			m, _ := testManager(t)
			if showConfig {
				m, _ = deployed(t)
			}
			var out dockerTerminalOutput
			m.out, m.errOut = &out, &out
			in := &dockerTerminalInput{answers: []string{"2\n", "2\n", "\n", "0\n", "0\n"}, output: &out}
			if err := m.Menu(context.Background(), in); err != nil {
				t.Fatal(err)
			}
			if len(in.screens) != 5 {
				t.Fatalf("result pause consumed navigation incorrectly: %d prompts", len(in.screens))
			}
			result := in.screens[2]
			if !strings.Contains(result, "按 Enter 返回菜单") || strings.Contains(result, "生成/更新配置") {
				t.Fatalf("result was not shown on its own page: %s", result)
			}
			if showConfig && !strings.Contains(result, "REALITY 私钥") || !showConfig && !strings.Contains(result, "[错误] 操作失败：") {
				t.Fatalf("result disappeared before pause: %s", result)
			}
			if !strings.Contains(in.screens[3], "›  服务端配置") || strings.Contains(in.screens[3], "按 Enter") || strings.Contains(in.screens[3], "[错误] 操作失败：") {
				t.Fatalf("result remained after returning to menu: %s", in.screens[3])
			}
		})
	}
}

func TestDockerMenuPipedInputHasNoTerminalControls(t *testing.T) {
	t.Setenv("TERM", "xterm")
	m, _ := testManager(t)
	var out dockerTerminalOutput
	m.out, m.errOut = &out, &out
	if err := m.Menu(context.Background(), strings.NewReader("2\n2\n0\n0\n")); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "\033") || strings.Contains(out.String(), "按 Enter") {
		t.Fatal("piped input must not clear screens or consume pause answers")
	}
}

func TestDockerMenuDumbTerminalHasNoTerminalControls(t *testing.T) {
	t.Setenv("TERM", "dumb")
	m, _ := testManager(t)
	var out dockerTerminalOutput
	m.out, m.errOut = &out, &out
	in := &dockerTerminalInput{answers: []string{"2\n", "2\n", "0\n", "0\n"}, output: &out}
	if err := m.Menu(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "\033") || strings.Contains(out.String(), "按 Enter") {
		t.Fatal("dumb terminal must not receive interactive screen controls")
	}
}
