package system

import (
	"context"
	"fmt"
	"io"
	"os/exec"
	"strings"
)

type ExecRunner struct{}

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
