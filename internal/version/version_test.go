package version

import (
	"fmt"
	"runtime"
	"testing"
)

func TestString(t *testing.T) {
	oldVersion, oldCommit, oldBuildDate := Version, Commit, BuildDate
	t.Cleanup(func() { Version, Commit, BuildDate = oldVersion, oldCommit, oldBuildDate })
	Version = "v1.2.3"
	Commit = "0123456789abcdef0123456789abcdef01234567"
	BuildDate = "2026-08-08T12:00:00Z"
	want := fmt.Sprintf(
		"v1.2.3\ncommit: 0123456789abcdef0123456789abcdef01234567\nbuild date: 2026-08-08T12:00:00Z\ngo: %s\nplatform: %s/%s",
		runtime.Version(), runtime.GOOS, runtime.GOARCH,
	)
	if got := String(); got != want {
		t.Fatalf("String() = %q, want %q", got, want)
	}
}
