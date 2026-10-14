package docker

import (
	"bytes"
	"context"
	"io"
	"os"
	"os/exec"
	"strings"
)

type Runner interface {
	Run(context.Context, string, ...string) ([]byte, error)
	RunStreaming(context.Context, io.Writer, io.Writer, string, ...string) error
}
type ExecRunner struct{}

func command(ctx context.Context, name string, args []string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, name, args...)
	// Only explicitly selected Compose files and the local socket are used.
	for _, env := range os.Environ() {
		key, _, _ := strings.Cut(env, "=")
		if strings.HasPrefix(key, "DOCKER_") || strings.HasPrefix(key, "COMPOSE_") {
			continue
		}
		cmd.Env = append(cmd.Env, env)
	}
	return cmd
}
func (ExecRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	var b bytes.Buffer
	cmd := command(ctx, name, args)
	cmd.Stdout = &b
	cmd.Stderr = &b
	err := cmd.Run()
	return b.Bytes(), err
}
func (ExecRunner) RunStreaming(ctx context.Context, out, errOut io.Writer, name string, args ...string) error {
	cmd := command(ctx, name, args)
	cmd.Stdout = out
	cmd.Stderr = errOut
	return cmd.Run()
}
