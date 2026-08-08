package version

import "testing"

func TestString(t *testing.T) {
	oldVersion, oldCommit, oldBuildDate := Version, Commit, BuildDate
	t.Cleanup(func() { Version, Commit, BuildDate = oldVersion, oldCommit, oldBuildDate })
	Version, Commit, BuildDate = "v1.2.3", "abc1234", "2026-08-08T12:00:00Z"
	want := "v1.2.3 (commit abc1234, built 2026-08-08T12:00:00Z)"
	if got := String(); got != want {
		t.Fatalf("String() = %q, want %q", got, want)
	}
}
