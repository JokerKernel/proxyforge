package cli

import (
	"bufio"
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestStableCommandTree(t *testing.T) {
	root := New("test")
	for _, args := range [][]string{{"install"}, {"upgrade"}, {"config", "generate"}, {"config", "client"}, {"config", "reset"}, {"service"}} {
		cmd, remaining, err := root.Find(args)
		if err != nil {
			t.Fatalf("find %v: %v", args, err)
		}
		if len(remaining) != 0 {
			t.Fatalf("find %v left %v", args, remaining)
		}
		if cmd.Name() != args[len(args)-1] {
			t.Fatalf("find %v got %s", args, cmd.Name())
		}
	}
}

func TestResetCommandRequiresYesWhenNonInteractive(t *testing.T) {
	var out bytes.Buffer
	c := &commandSet{in: strings.NewReader(""), reader: bufio.NewReader(strings.NewReader("")), out: &out}
	cmd := c.resetCommand()
	cmd.SetArgs([]string{"sing-box"})
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "--yes") {
		t.Fatalf("error = %v, want --yes requirement", err)
	}
}

func TestCredentialResetConfirmationWarnsAboutOldClients(t *testing.T) {
	var out bytes.Buffer
	c := &commandSet{reader: bufio.NewReader(strings.NewReader("yes\n")), out: &out}
	confirmed, err := c.confirmCredentialReset("xray")
	if err != nil {
		t.Fatal(err)
	}
	if !confirmed || !strings.Contains(out.String(), "所有旧客户端配置会立即失效") {
		t.Fatalf("confirmed=%v output=%q", confirmed, out.String())
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
	if count := strings.Count(out.String(), "无效选择"); count != 2 {
		t.Fatalf("invalid message count = %d, output=%q", count, out.String())
	}
}

func TestSelectCoreUsesNumericChoiceAndDefault(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{name: "default sing-box", input: "\n", want: "sing-box"},
		{name: "xray", input: "2\n", want: "xray"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var out bytes.Buffer
			c := &commandSet{reader: bufio.NewReader(strings.NewReader(tt.input)), out: &out}
			got, selected, err := c.selectCore()
			if err != nil {
				t.Fatal(err)
			}
			if !selected || got != tt.want {
				t.Fatalf("core=%q selected=%v, want %q", got, selected, tt.want)
			}
		})
	}
}

func TestMenuCanReturnFromCoreSelection(t *testing.T) {
	var out, errOut bytes.Buffer
	c := &commandSet{
		reader: bufio.NewReader(strings.NewReader("4\n0\n0\n")),
		out:    &out,
		errOut: &errOut,
	}
	if err := c.menu(context.Background()); err != nil {
		t.Fatal(err)
	}
	if strings.Count(out.String(), "ProxyForge 双内核代理管理器") != 2 {
		t.Fatalf("main menu was not shown twice: %q", out.String())
	}
	if !strings.Contains(out.String(), "已退出 ProxyForge") {
		t.Fatalf("missing exit message: %q", out.String())
	}
}
