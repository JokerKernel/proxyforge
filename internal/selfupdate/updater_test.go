package selfupdate

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"reflect"
	"strings"
	"testing"

	"proxyforge/internal/install"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

type runnerStub struct {
	called  bool
	command string
	args    []string
	script  string
	path    string
	err     error
	output  string
}

func (r *runnerStub) Run(context.Context, string, ...string) ([]byte, error) {
	return nil, errors.New("buffered execution should not be used")
}

func (r *runnerStub) RunStreaming(_ context.Context, stdout, _ io.Writer, command string, args ...string) error {
	r.called = true
	r.command = command
	r.args = append([]string(nil), args...)
	if len(args) > 0 {
		r.path = args[0]
		body, readErr := os.ReadFile(r.path)
		if readErr != nil {
			return readErr
		}
		r.script = string(body)
	}
	_, _ = io.WriteString(stdout, r.output)
	return r.err
}

func TestRunOnlyStartsPreparedScript(t *testing.T) {
	t.Setenv("PROXYFORGE_COLOR", "never")
	scriptBody := "#!/usr/bin/env bash\nprintf 'updated\\n'\n"
	requests := 0
	client := testClient(func(req *http.Request) string {
		requests++
		if req.URL.Path != "/install.sh" {
			t.Fatalf("unexpected request %s", req.URL)
		}
		return scriptBody
	})
	runner := &runnerStub{output: "install-ui\n"}
	u := testUpdater(client, runner)
	var output strings.Builder
	u.Installer.Output = &output
	var stderr strings.Builder
	u.Stderr = &stderr

	if err := u.Run(context.Background(), Options{}); err != nil {
		t.Fatal(err)
	}
	if requests != 1 || !runner.called {
		t.Fatalf("requests=%d runner.called=%v", requests, runner.called)
	}
	if runner.command != "bash" || len(runner.args) != 1 {
		t.Fatalf("command=%q args=%v", runner.command, runner.args)
	}
	if runner.script != scriptBody {
		t.Fatalf("executed script=%q", runner.script)
	}
	want := fmt.Sprintf("下载 ProxyForge 更新脚本\n✓ 下载完成 · ProxyForge 更新脚本 · %d 字节\n✓ 运行脚本\n\n", len(scriptBody))
	if stderr.String() != want {
		t.Fatalf("stderr=%q, want %q", stderr.String(), want)
	}
	if output.String() != "install-ui\n" {
		t.Fatalf("stdout=%q", output.String())
	}
}

type terminalOutput struct{ strings.Builder }

func (*terminalOutput) IsTerminal() bool { return true }

func TestUpdateProgressInTerminal(t *testing.T) {
	t.Setenv("TERM", "xterm")
	t.Setenv("PROXYFORGE_COLOR", "always")
	for _, tc := range []struct {
		name  string
		total int64
		start string
	}{
		{"known length", 39, "\r\x1b[38;5;208m\x1b[0m\x1b[2m" + strings.Repeat("･", 50) + "\x1b[0m   0%  0 B / 39 B\x1b[K"},
		{"unknown length", -1, "\r已下载 0 B\x1b[K"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var stderr terminalOutput
			client := updateClient()
			transport := client.Transport
			client.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
				response, err := transport.RoundTrip(req)
				response.ContentLength = tc.total
				return response, err
			})
			u := testUpdater(client, &runnerStub{})
			u.Stderr = &stderr
			if err := u.Run(context.Background(), Options{}); err != nil {
				t.Fatal(err)
			}
			wantEnd := "\r\x1b[38;5;208m" + strings.Repeat("■", 50) + "\x1b[0m\x1b[2m\x1b[0m 100%  39 B / 39 B\x1b[K\n\x1b[32m✓\x1b[0m 运行脚本\n\n"
			if !strings.Contains(stderr.String(), tc.start) || !strings.HasSuffix(stderr.String(), wantEnd) {
				t.Fatalf("unexpected progress output %q", stderr.String())
			}
		})
	}
}

func TestAssumeYesIsPassedToScript(t *testing.T) {
	client := updateClient()
	runner := &runnerStub{}
	u := testUpdater(client, runner)
	if err := u.Run(context.Background(), Options{AssumeYes: true}); err != nil {
		t.Fatal(err)
	}
	if got, want := runner.args[1:], []string{"--yes"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("script args=%v, want %v", got, want)
	}
}

func TestUninstallIsPassedToScript(t *testing.T) {
	client := updateClient()
	runner := &runnerStub{}
	u := testUpdater(client, runner)
	if err := u.Run(context.Background(), Options{Uninstall: true}); err != nil {
		t.Fatal(err)
	}
	if got, want := runner.args[1:], []string{"uninstall"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("script args=%v, want %v", got, want)
	}
}

func TestRejectsInvalidOrUnapprovedScript(t *testing.T) {
	t.Run("invalid", func(t *testing.T) {
		client := testClient(func(*http.Request) string { return "not a shell script\n" })
		u := testUpdater(client, &runnerStub{})
		if err := u.Run(context.Background(), Options{}); err == nil || !strings.Contains(err.Error(), "shebang") {
			t.Fatalf("error=%v", err)
		}
	})
	t.Run("unapproved host", func(t *testing.T) {
		u := Updater{ScriptURL: "https://evil.example/install.sh", AllowedHosts: []string{"official.example"}}
		if err := u.Run(context.Background(), Options{}); err == nil || !strings.Contains(err.Error(), "白名单") {
			t.Fatalf("error=%v", err)
		}
	})
}

func TestExecutionFailureIsReportedAndTemporaryScriptRemoved(t *testing.T) {
	client := updateClient()
	runner := &runnerStub{err: errors.New("execution failed")}
	u := testUpdater(client, runner)
	err := u.Run(context.Background(), Options{AssumeYes: true})
	if err == nil || !strings.Contains(err.Error(), "执行 ProxyForge 安装/更新脚本") {
		t.Fatalf("error=%v", err)
	}
	if _, statErr := os.Stat(runner.path); !os.IsNotExist(statErr) {
		t.Fatalf("temporary script still exists: %v", statErr)
	}
}

func updateClient() *http.Client {
	return testClient(func(*http.Request) string {
		return "#!/usr/bin/env bash\nprintf 'updated\\n'\n"
	})
}

func testUpdater(client *http.Client, runner *runnerStub) Updater {
	return Updater{
		Client: client,
		Installer: install.Installer{
			Client: client, Runner: runner, Output: io.Discard,
		},
		ScriptURL: "https://official.example/install.sh", AllowedHosts: []string{"official.example"},
	}
}

func testClient(body func(*http.Request) string) *http.Client {
	return &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Status:     "200 OK",
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(body(req))),
			Request:    req,
		}, nil
	})}
}
