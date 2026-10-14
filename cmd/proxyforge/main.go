package main

import (
	"os"

	"proxyforge/internal/cli"
	"proxyforge/internal/system"
	"proxyforge/internal/version"
)

func main() {
	if err := cli.New(version.String()).Execute(); err != nil {
		system.WriteDetail(system.NewTerminalColorWriter(os.Stderr), "操作失败", "%v", err)
		os.Exit(1)
	}
}
