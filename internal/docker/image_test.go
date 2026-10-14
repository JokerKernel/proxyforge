package docker

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"
	"testing"

	"proxyforge/internal/docker/model"
)

func TestLatestPreparePinsDigestAndRecordsActualVersion(t *testing.T) {
	m, fake := testManager(t)
	if err := m.Prepare(context.Background(), " latest "); err != nil {
		t.Fatal(err)
	}
	md, err := m.metadata()
	if err != nil {
		t.Fatal(err)
	}
	if md.Channel != "latest" || md.RequestedVersion != "26.4.1" || !strings.HasPrefix(md.Version, "Xray 26.4.1 ") {
		t.Fatalf("latest lost its channel or actual version: %+v", md)
	}
	compose := readFile(t, m.paths.Compose())
	if !bytes.Contains(compose, []byte(md.ImageRef)) || bytes.Contains(compose, []byte(Repository+":latest")) {
		t.Fatalf("latest deployment must pin digest: %s", compose)
	}
	if fake.container != nil {
		t.Fatal("prepare started a business container")
	}
	pulled := false
	for _, call := range fake.calls {
		if len(call) == 3 && call[0] == "image" && call[1] == "pull" {
			pulled = true
			if call[2] != Repository+":latest" {
				t.Fatalf("wrong pull: %v", call)
			}
		}
	}
	if !pulled {
		t.Fatal("latest tag was not pulled")
	}
}

func TestLatestDeploymentEntrypoints(t *testing.T) {
	for _, entry := range []string{"manager", "command"} {
		for _, alreadyPrepared := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/prepared=%t", entry, alreadyPrepared), func(t *testing.T) {
				m, fake := testManager(t)
				if alreadyPrepared {
					if err := m.Prepare(context.Background(), "latest"); err != nil {
						t.Fatal(err)
					}
				}
				fake.calls = nil
				var err error
				if entry == "manager" {
					err = m.Deploy(context.Background(), "latest", model.GenerateOptions{Server: "example.com", Port: 8443, SNI: "www.example.com", Target: "www.example.com:443", StandardConfig: true})
				} else {
					cmd := m.Command()
					cmd.SetOut(io.Discard)
					cmd.SetErr(io.Discard)
					cmd.SetIn(strings.NewReader(""))
					cmd.SetArgs([]string{"deploy", "--version", "latest", "--server", "example.com", "--port", "8443", "--sni", "www.example.com", "--target", "www.example.com:443", "--standard-config"})
					err = cmd.ExecuteContext(context.Background())
				}
				if err != nil {
					t.Fatal(err)
				}
				if fake.container == nil || !fake.container.State.Running {
					t.Fatal("latest deployment did not start")
				}
				if alreadyPrepared {
					for _, call := range fake.calls {
						if contains(call, "pull") {
							t.Fatalf("deploy must reuse prepared digest: %v", call)
						}
					}
				}
			})
		}
	}
}

func TestLatestUpgradeRefreshesDigestAndRollsBack(t *testing.T) {
	m, fake := deployed(t)
	ctx := context.Background()
	if err := m.Upgrade(ctx, "latest"); err != nil {
		t.Fatal(err)
	}
	oldMD, err := m.metadata()
	if err != nil {
		t.Fatal(err)
	}
	oldState := readFile(t, m.paths.State())
	oldCompose := readFile(t, m.paths.Compose())
	fake.latestVersion = "26.4.2"
	fake.latestImageID = "sha256:" + strings.Repeat("c", 64)
	fake.healthFailures = 1
	if err := m.Upgrade(ctx, "latest"); err == nil {
		t.Fatal("unhealthy latest image accepted")
	}
	md, err := m.metadata()
	if err != nil {
		t.Fatal(err)
	}
	if md.ImageID != oldMD.ImageID || md.Channel != "latest" || fake.container.Image != oldMD.ImageID || !fake.container.State.Running || !bytes.Equal(oldState, readFile(t, m.paths.State())) || !bytes.Equal(oldCompose, readFile(t, m.paths.Compose())) {
		t.Fatal("failed latest upgrade did not restore deployment")
	}
	if _, err := m.Service(ctx, "stop"); err != nil {
		t.Fatal(err)
	}
	if err := m.Upgrade(ctx, "latest"); err != nil {
		t.Fatal(err)
	}
	md, err = m.metadata()
	if err != nil {
		t.Fatal(err)
	}
	if md.ImageID != fake.latestImageID || md.RequestedVersion != "26.4.2" || fake.container.Image != md.ImageID || fake.container.State.Running {
		t.Fatal("latest upgrade did not refresh digest while preserving stopped state")
	}
	fake.calls = nil
	if _, err := m.Service(ctx, "start"); err != nil {
		t.Fatal(err)
	}
	for _, call := range fake.calls {
		if contains(call, "pull") {
			t.Fatalf("start must reuse latest digest: %v", call)
		}
	}
}

func TestImageVersionValidation(t *testing.T) {
	for _, tc := range []struct{ selection, output string }{
		{"latest", "Xray unknown (build)\n"},
		{"latest", "invalid output\n"},
		{"26.3.27", "Xray 26.4.1 (build)\n"},
	} {
		t.Run(tc.selection+"/"+tc.output, func(t *testing.T) {
			m, fake := testManager(t)
			fake.versionOutput = tc.output
			if err := m.Prepare(context.Background(), tc.selection); err == nil {
				t.Fatal("invalid/mismatched version accepted")
			}
			if _, err := m.metadata(); err == nil {
				t.Fatal("failed image detection saved deployment metadata")
			}
		})
	}
}

func TestLatestDeployRequiresLatestPreparedChannel(t *testing.T) {
	for _, selection := range []string{"latest", "stable", "pre-release"} {
		t.Run(selection, func(t *testing.T) {
			m, fake := prepared(t)
			fake.calls = nil
			if err := m.Deploy(context.Background(), selection, model.GenerateOptions{}); err == nil || !strings.Contains(err.Error(), "升级入口") {
				t.Fatalf("%s deployment reused a pinned version: %v", selection, err)
			}
			if len(fake.calls) != 0 {
				t.Fatal("version selection mismatch mutated deployment")
			}
		})
	}
}

func TestStableAndPrereleaseUseOfficialTags(t *testing.T) {
	for _, tc := range []struct {
		selection, tag, channel, version string
	}{
		{"stable", Repository + ":latest", "latest", "26.4.1"},
		{"", Repository + ":latest", "latest", "26.4.1"},
		{"pre-release", Repository + ":pre-release", "prerelease", "26.10.10"},
		{"prerelease", Repository + ":pre-release", "prerelease", "26.10.10"},
	} {
		t.Run(tc.selection+"/"+tc.tag, func(t *testing.T) {
			m, fake := testManager(t)
			if err := m.Prepare(context.Background(), tc.selection); err != nil {
				t.Fatal(err)
			}
			md, err := m.metadata()
			if err != nil {
				t.Fatal(err)
			}
			if md.Channel != tc.channel || md.RequestedVersion != tc.version {
				t.Fatalf("channel or version: %+v", md)
			}
			compose := readFile(t, m.paths.Compose())
			if bytes.Contains(compose, []byte(tc.tag)) || !bytes.Contains(compose, []byte(md.ImageRef)) {
				t.Fatalf("compose did not pin digest: %s", compose)
			}
			pulled := false
			for _, call := range fake.calls {
				if len(call) == 3 && call[0] == "image" && call[1] == "pull" {
					pulled = true
					if call[2] != tc.tag {
						t.Fatalf("wrong pull: %v", call)
					}
				}
			}
			if !pulled {
				t.Fatal("official tag was not pulled")
			}
		})
	}
}

func TestPrepareWaitsForEnterAfterPull(t *testing.T) {
	t.Setenv("TERM", "xterm")
	m, _ := testManager(t)
	var out dockerTerminalOutput
	m.out, m.errOut = &out, &out
	in := &dockerTerminalInput{answers: []string{"\n"}, output: &out}
	m.review = newUI(in, &out)
	if err := m.Prepare(context.Background(), "latest"); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	pull := strings.Index(text, "image pull ")
	pause := strings.Index(text, "按 Enter 继续")
	inspect := strings.Index(text, "image inspect ")
	if pull < 0 || pause < 0 || inspect < 0 || pull > pause || pause > inspect {
		t.Fatalf("pause must sit between the pull output and the next docker call:\n%s", text)
	}
	if len(in.screens) != 1 || strings.Contains(in.screens[0], "image inspect ") {
		t.Fatalf("next step ran before Enter: %q", in.screens)
	}
	if in.index != 1 {
		t.Fatalf("continue input was not consumed: %d", in.index)
	}
}

func TestPrepareSkipsPullPauseOutsideMenu(t *testing.T) {
	m, _ := testManager(t)
	var plain bytes.Buffer
	m.out, m.errOut = &plain, &plain
	if err := m.Prepare(context.Background(), "latest"); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(plain.String(), "按 Enter") {
		t.Fatalf("command path waited for Enter:\n%s", plain.String())
	}

	t.Setenv("TERM", "dumb")
	m, _ = testManager(t)
	var out dockerTerminalOutput
	m.out, m.errOut = &out, &out
	in := &dockerTerminalInput{output: &out}
	m.review = newUI(in, &out)
	if err := m.Prepare(context.Background(), "latest"); err != nil {
		t.Fatal(err)
	}
	if in.index != 0 || strings.Contains(out.String(), "按 Enter") {
		t.Fatalf("dumb terminal waited for Enter: index=%d\n%s", in.index, out.String())
	}
}

func TestPrepareContinuesWhenPullPauseHitsEOF(t *testing.T) {
	t.Setenv("TERM", "xterm")
	m, _ := testManager(t)
	var out dockerTerminalOutput
	m.out, m.errOut = &out, &out
	in := &dockerTerminalInput{output: &out}
	m.review = newUI(in, &out)
	if err := m.Prepare(context.Background(), "latest"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "按 Enter 继续") {
		t.Fatalf("missing continue prompt:\n%s", out.String())
	}
	if _, err := m.metadata(); err != nil {
		t.Fatal(err)
	}
}

func TestPrepareDoesNotPauseWhenPullFails(t *testing.T) {
	t.Setenv("TERM", "xterm")
	m, fake := testManager(t)
	fake.failPull = true
	var out dockerTerminalOutput
	m.out, m.errOut = &out, &out
	in := &dockerTerminalInput{answers: []string{"\n"}, output: &out}
	m.review = newUI(in, &out)
	if err := m.Prepare(context.Background(), "latest"); err == nil {
		t.Fatal("failed pull was accepted")
	}
	if in.index != 0 || strings.Contains(out.String(), "按 Enter") {
		t.Fatalf("failed pull waited for Enter: index=%d\n%s", in.index, out.String())
	}
}
