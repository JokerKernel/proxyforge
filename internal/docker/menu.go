package docker

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
)

var errCancelled = errors.New("用户取消")
var errBack = errors.New("返回菜单")

type ui struct {
	reader      *bufio.Reader
	out         io.Writer
	interactive bool
}

func newUI(input io.Reader, out io.Writer) *ui {
	r, ok := input.(*bufio.Reader)
	if !ok {
		r = bufio.NewReader(input)
	}
	interactiveInput := terminalInput(input)
	if reporter, ok := input.(interface{ IsTerminal() bool }); ok {
		interactiveInput = reporter.IsTerminal()
	} else if _, buffered := input.(*bufio.Reader); buffered {
		// The main menu passes its shared stdin reader so buffered input stays
		// with the same owner when entering and leaving the Docker menu.
		interactiveInput = terminalInput(os.Stdin)
	}
	interactiveOutput := false
	if reporter, ok := out.(interface{ IsTerminal() bool }); ok {
		interactiveOutput = reporter.IsTerminal()
	} else if f, ok := out.(*os.File); ok {
		info, err := f.Stat()
		interactiveOutput = err == nil && info.Mode()&os.ModeCharDevice != 0
	}
	return &ui{reader: r, out: out, interactive: interactiveInput && interactiveOutput && os.Getenv("TERM") != "dumb"}
}
func (u *ui) clearScreen() {
	if u.interactive {
		fmt.Fprint(u.out, "\033[H\033[2J\033[3J")
	}
}
func (u *ui) pause() error {
	if !u.interactive {
		return nil
	}
	fmt.Fprint(u.out, "\n按 Enter 返回菜单……")
	_, err := u.reader.ReadString('\n')
	return err
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
