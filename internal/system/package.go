package system

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"proxyforge/internal/provider"
)

func RemovePackage(ctx context.Context, runner provider.Runner, layout Layout, packageName string, output io.Writer) error {
	family, err := packageFamily(layout)
	if err != nil {
		return err
	}
	var command string
	var args []string
	switch family {
	case "debian":
		command, args = "dpkg", []string{"--remove", packageName}
	case "rhel":
		command, args = "rpm", []string{"-e", packageName}
	default:
		return fmt.Errorf("不支持在 %s 系发行版卸载软件包", family)
	}
	var removeErr error
	if streaming, ok := runner.(provider.StreamingRunner); ok {
		if output == nil {
			output = io.Discard
		}
		prefixed := NewLinePrefixWriter(output, "[系统命令/输出] ")
		removeErr = streaming.RunStreaming(ctx, prefixed, prefixed, command, args...)
	} else {
		_, removeErr = runner.Run(ctx, command, args...)
	}
	if removeErr != nil {
		return fmt.Errorf("卸载软件包 %s: %w", packageName, removeErr)
	}
	return nil
}

func packageFamily(layout Layout) (string, error) {
	f, err := os.Open(layout.Resolve("/etc/os-release"))
	if err != nil {
		return "", fmt.Errorf("读取 os-release: %w", err)
	}
	defer f.Close()
	var identifiers []string
	s := bufio.NewScanner(f)
	for s.Scan() {
		parts := strings.SplitN(s.Text(), "=", 2)
		if len(parts) != 2 || (parts[0] != "ID" && parts[0] != "ID_LIKE") {
			continue
		}
		identifiers = append(identifiers, strings.Fields(strings.Trim(parts[1], `"`))...)
	}
	if err := s.Err(); err != nil {
		return "", fmt.Errorf("读取 os-release: %w", err)
	}
	for _, id := range identifiers {
		switch id {
		case "debian", "ubuntu":
			return "debian", nil
		case "rhel", "centos", "rocky", "almalinux", "fedora":
			return "rhel", nil
		}
	}
	return "", fmt.Errorf("无法确定受支持的软件包管理器")
}
