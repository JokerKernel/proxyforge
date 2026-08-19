package app

import (
	"bytes"
	"testing"
)

func TestProgressIdentifiesProxyForgeAsSource(t *testing.T) {
	var output bytes.Buffer
	a := &App{Progress: &output}
	a.progressf("测试步骤 %d", 1)
	if got, want := output.String(), "[步骤] 测试步骤 1\n"; got != want {
		t.Fatalf("progress=%q, want %q", got, want)
	}
}
