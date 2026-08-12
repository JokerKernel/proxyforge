package selfupdate

import (
	"context"
	"errors"
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
	for _, expected := range []string{
		"[步骤] 下载并验证 ProxyForge 安装/更新脚本",
		"[信息] 安装脚本地址：https://official.example/install.sh",
		"[信息] 重定向后地址：https://official.example/install.sh",
		"[信息] 脚本 SHA-256：", "[步骤] 启动 ProxyForge 安装/更新流程", "install-ui",
	} {
		if !strings.Contains(output.String(), expected) {
			t.Fatalf("output missing %q:\n%s", expected, output.String())
		}
	}
	if strings.Contains(output.String(), "[Bash]") {
		t.Fatalf("self-update UI should not be prefixed as an official script:\n%s", output.String())
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
