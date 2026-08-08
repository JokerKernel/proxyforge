package main

import (
	"fmt"
	"os"

	"proxyforge/internal/cli"
	"proxyforge/internal/version"
)

func main() {
	if err := cli.New(version.String()).Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "错误:", err)
		os.Exit(1)
	}
}
