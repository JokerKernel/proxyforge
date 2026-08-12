package main

import (
	"fmt"
	"os"

	"proxyforge/internal/cli"
	"proxyforge/internal/system"
	"proxyforge/internal/version"
)

func main() {
	if err := cli.New(version.String()).Execute(); err != nil {
		fmt.Fprintln(system.NewTerminalColorWriter(os.Stderr), "[错误] 操作失败：", err)
		os.Exit(1)
	}
}
