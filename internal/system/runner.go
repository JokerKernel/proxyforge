package system

import (
	"context"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"strings"
	"sync"

	"proxyforge/internal/provider"
)

type ExecRunner struct{}

type LoggingRunner struct {
	Runner provider.Runner
	Out    io.Writer
	mu     sync.Mutex
}

func (r *LoggingRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	r.log("执行命令", name, args)
	b, err := r.Runner.Run(ctx, name, args...)
	if err != nil && ctx.Err() != nil {
		r.logResult("命令已停止", name)
	} else if err != nil {
		r.logResult("命令失败", name)
	} else {
		r.logResult("命令完成", name)
	}
	return b, err
}

func (r *LoggingRunner) RunStreaming(ctx context.Context, stdout, stderr io.Writer, name string, args ...string) error {
	r.log("执行命令", name, args)
	var err error
	if streaming, ok := r.Runner.(provider.StreamingRunner); ok {
		err = streaming.RunStreaming(ctx, stdout, stderr, name, args...)
	} else {
		var b []byte
		b, err = r.Runner.Run(ctx, name, args...)
		if len(b) != 0 {
			_, _ = stdout.Write(b)
		}
	}
	if err != nil && ctx.Err() != nil {
		r.logResult("命令已停止", name)
	} else if err != nil {
		r.logResult("命令失败", name)
	} else {
		r.logResult("命令完成", name)
	}
	return err
}

func (r *LoggingRunner) log(label, name string, args []string) {
	if r.Out == nil {
		return
	}
	parts := make([]string, 0, len(args)+1)
	parts = append(parts, quoteCommandArg(name))
	for _, arg := range redactCommandArgs(args) {
		parts = append(parts, quoteCommandArg(arg))
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	fmt.Fprintf(r.Out, "[命令] %s：%s\n", label, strings.Join(parts, " "))
}

func redactCommandArgs(args []string) []string {
	redacted := append([]string(nil), args...)
	for index, arg := range redacted {
		if arg == "--proxy" || arg == "-p" {
			if index+1 < len(redacted) {
				redacted[index+1] = "[REDACTED]"
			}
			continue
		}
		if strings.HasPrefix(arg, "--proxy=") {
			redacted[index] = "--proxy=[REDACTED]"
		}
	}
	return redacted
}

func (r *LoggingRunner) logResult(label, name string) {
	if r.Out == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	fmt.Fprintf(r.Out, "[命令] %s：%s\n", label, name)
}

func quoteCommandArg(value string) string {
	if value != "" && !strings.ContainsAny(value, " \t\r\n\"'\\") {
		return value
	}
	return strconv.Quote(value)
}

func (ExecRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	b, err := cmd.CombinedOutput()
	if err != nil {
		message := strings.TrimSpace(string(b))
		if message != "" {
			return b, fmt.Errorf("%s: %s: %w", name, message, err)
		}
		return b, fmt.Errorf("%s: %w", name, err)
	}
	return b, nil
}

func (ExecRunner) RunStreaming(ctx context.Context, stdout, stderr io.Writer, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	return nil
}
