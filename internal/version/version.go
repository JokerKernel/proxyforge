// Package version contains build metadata injected by the Go linker.
package version

import (
	"fmt"
	"runtime"
)

var (
	Version   = "dev"
	Commit    = "unknown"
	BuildDate = "unknown"
)

// String returns detailed, human-readable build information for CLI output.
func String() string {
	return fmt.Sprintf(
		"%s\ncommit: %s\nbuild date: %s\ngo: %s\nplatform: %s/%s",
		Version,
		Commit,
		BuildDate,
		runtime.Version(),
		runtime.GOOS,
		runtime.GOARCH,
	)
}
