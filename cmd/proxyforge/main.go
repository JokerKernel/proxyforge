package main

import (
	"fmt"
	"os"

	"proxyforge/internal/cli"
	"proxyforge/internal/system"
	"proxyforge/internal/version"
)

func main() {
	if cmd, err := cli.New(version.String()).ExecuteC(); err != nil {
		if cmd != nil && cmd.Name() == "update" {
			prefix := "[错误]"
			if system.TerminalColorEnabled(os.Stderr) {
				prefix = "\x1b[31m" + prefix + "\x1b[0m"
			}
			fmt.Fprintln(os.Stderr, prefix, err)
			os.Exit(1)
		}
		system.WriteDetail(system.NewTerminalColorWriter(os.Stderr), "操作失败", "%v", err)
		os.Exit(1)
	}
}
