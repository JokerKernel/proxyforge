package docker

import (
	"context"
	"strings"
	"testing"
)

type composeVersionRunner struct {
	*simulatedDocker
	version string
}

func (r *composeVersionRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	if len(args) == 5 && args[0] == "--host" && args[1] == endpoint && args[2] == "compose" && args[3] == "version" && args[4] == "--short" {
		return []byte(r.version), nil
	}
	return r.simulatedDocker.Run(ctx, name, args...)
}

func TestCheckReadyComposeVersions(t *testing.T) {
	for _, tc := range []struct {
		version string
		wantErr string
	}{
		{version: "2.30.0\n"},
		{version: "v2.30.0\n"},
		{version: "5.6.0\n"},
		{version: " v5.6.0\r\n"},
		{version: "v5.6.0-desktop.1"},
		{version: "5.6.0+build.1"},
		{version: "1.29.2", wantErr: "需要 Docker Compose v2 或更高版本"},
		{version: "v1.29.2", wantErr: "需要 Docker Compose v2 或更高版本"},
		{version: "", wantErr: "无法识别 Docker Compose 版本"},
		{version: "garbage", wantErr: "无法识别 Docker Compose 版本"},
		{version: "2.garbage", wantErr: "无法识别 Docker Compose 版本"},
	} {
		t.Run(tc.version, func(t *testing.T) {
			m, fake := testManager(t)
			m.runner = &composeVersionRunner{simulatedDocker: fake, version: tc.version}
			err := m.CheckReady(context.Background())
			if tc.wantErr == "" {
				if err != nil {
					t.Fatal(err)
				}
				if len(fake.calls) != 1 || fake.calls[0][0] != "info" {
					t.Fatalf("expected daemon check, got %v", fake.calls)
				}
			} else {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("got %v, want %s", err, tc.wantErr)
				}
				if len(fake.calls) != 0 {
					t.Fatalf("unsupported Compose must stop before daemon check: %v", fake.calls)
				}
			}
		})
	}
}
