package app

import (
	"bytes"
	"testing"

	"proxyforge/internal/system"
)

func TestProgressShowsPurposeAndDetails(t *testing.T) {
	var output bytes.Buffer
	a := &App{Progress: &output}
	a.progressf("测试步骤", "%d", 1)
	if got, want := output.String(), system.DetailLine("测试步骤", "1"); got != want {
		t.Fatalf("progress=%q, want %q", got, want)
	}
}
