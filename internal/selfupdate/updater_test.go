package selfupdate

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
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
	err     error
}

func (r *runnerStub) Run(context.Context, string, ...string) ([]byte, error) {
	return nil, errors.New("buffered execution should not be used")
}

func (r *runnerStub) RunStreaming(_ context.Context, _, _ io.Writer, command string, args ...string) error {
	r.called = true
	r.command = command
	r.args = append([]string(nil), args...)
	if len(args) > 0 {
		path := args[len(args)-1]
		body, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		r.script = string(body)
	}
	return r.err
}

func TestAlreadyLatestSkipsScript(t *testing.T) {
	requests := 0
	client := testClient(t, func(req *http.Request) string {
		requests++
		if req.URL.Path != "/version" {
			t.Fatalf("unexpected request %s", req.URL)
		}
		return "v1.2.3\n"
	})
	var out strings.Builder
	u := Updater{
		Client: client, Output: &out,
		VersionURL:   "https://official.example/version",
		ScriptURL:    "https://official.example/install.sh",
		AllowedHosts: []string{"official.example"},
	}
	if err := u.Run(context.Background(), Options{CurrentVersion: "v1.2.3"}); err != nil {
		t.Fatal(err)
	}
	if requests != 1 || !strings.Contains(out.String(), "已是最新正式版本") {
		t.Fatalf("requests=%d output=%q", requests, out.String())
	}
}

func TestConfirmedUpdateExecutesPreparedInstaller(t *testing.T) {
	scriptBody := "#!/usr/bin/env bash\nprintf 'updated\\n'\n"
	client := testClient(t, func(req *http.Request) string {
		switch req.URL.Path {
		case "/version":
			return "v1.2.4\n"
		case "/install.sh":
			return scriptBody
		default:
			t.Fatalf("unexpected request %s", req.URL)
			return ""
		}
	})
	runner := &runnerStub{}
	confirmed := false
	var out strings.Builder
	u := Updater{
		Client: client, Output: &out,
		Installer:    install.Installer{Client: client, Runner: runner, Output: io.Discard},
		VersionURL:   "https://official.example/version",
		ScriptURL:    "https://official.example/install.sh",
		AllowedHosts: []string{"official.example"},
	}
	err := u.Run(context.Background(), Options{
		CurrentVersion: "v1.2.3",
		Confirm: func(message string) (bool, error) {
			confirmed = strings.Contains(message, "v1.2.3") && strings.Contains(message, "v1.2.4")
			return true, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !confirmed || !runner.called {
		t.Fatalf("confirmed=%v runner.called=%v", confirmed, runner.called)
	}
	if runner.command != "env" || len(runner.args) < 3 || runner.args[0] != "PROXYFORGE_VERSION=v1.2.4" || runner.args[1] != "bash" {
		t.Fatalf("command=%q args=%v", runner.command, runner.args)
	}
	if runner.script != scriptBody {
		t.Fatalf("executed script=%q", runner.script)
	}
	for _, want := range []string{"当前版本：v1.2.3", "目标版本：v1.2.4", "脚本 SHA-256：", "已升级到 v1.2.4"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("output missing %q: %q", want, out.String())
		}
	}
}

func TestAssumeYesSkipsConfirmation(t *testing.T) {
	client := updateClient(t)
	runner := &runnerStub{}
	u := testUpdater(client, runner)
	if err := u.Run(context.Background(), Options{CurrentVersion: "v1.2.3", AssumeYes: true}); err != nil {
		t.Fatal(err)
	}
	if !runner.called {
		t.Fatal("installer was not executed")
	}
}

func TestUpdateRequiresConfirmation(t *testing.T) {
	client := updateClient(t)
	runner := &runnerStub{}
	u := testUpdater(client, runner)
	err := u.Run(context.Background(), Options{CurrentVersion: "v1.2.3"})
	if err == nil || !strings.Contains(err.Error(), "--yes") || runner.called {
		t.Fatalf("error=%v runner.called=%v", err, runner.called)
	}
}

func TestDeclinedUpdateDoesNotExecute(t *testing.T) {
	client := updateClient(t)
	runner := &runnerStub{}
	u := testUpdater(client, runner)
	err := u.Run(context.Background(), Options{
		CurrentVersion: "v1.2.3",
		Confirm:        func(string) (bool, error) { return false, nil },
	})
	if err == nil || !strings.Contains(err.Error(), "取消") || runner.called {
		t.Fatalf("error=%v runner.called=%v", err, runner.called)
	}
}

func TestRejectsInvalidVersionAndScript(t *testing.T) {
	t.Run("version", func(t *testing.T) {
		client := testClient(t, func(*http.Request) string { return "latest\n" })
		u := Updater{Client: client, VersionURL: "https://official.example/version", AllowedHosts: []string{"official.example"}}
		if err := u.Run(context.Background(), Options{}); err == nil || !strings.Contains(err.Error(), "内容无效") {
			t.Fatalf("error=%v", err)
		}
	})
	t.Run("script", func(t *testing.T) {
		client := testClient(t, func(req *http.Request) string {
			if req.URL.Path == "/version" {
				return "v1.2.4\n"
			}
			return "not a shell script\n"
		})
		u := Updater{
			Client: client, Installer: install.Installer{Client: client, Runner: &runnerStub{}, Output: io.Discard},
			VersionURL: "https://official.example/version", ScriptURL: "https://official.example/install.sh",
			AllowedHosts: []string{"official.example"},
		}
		if err := u.Run(context.Background(), Options{CurrentVersion: "v1.2.3", AssumeYes: true}); err == nil || !strings.Contains(err.Error(), "shebang") {
			t.Fatalf("error=%v", err)
		}
	})
}

func TestRejectsInsecureOrUnapprovedVersionSource(t *testing.T) {
	for _, rawURL := range []string{
		"http://official.example/version",
		"https://evil.example/version",
	} {
		u := Updater{VersionURL: rawURL, AllowedHosts: []string{"official.example"}}
		if err := u.Run(context.Background(), Options{}); err == nil {
			t.Fatalf("source %q was accepted", rawURL)
		}
	}
}

func TestExecutionFailureIsReportedAndTemporaryScriptRemoved(t *testing.T) {
	client := updateClient(t)
	runner := &runnerStub{err: errors.New("execution failed")}
	u := testUpdater(client, runner)
	err := u.Run(context.Background(), Options{CurrentVersion: "v1.2.3", AssumeYes: true})
	if err == nil || !strings.Contains(err.Error(), "执行自升级脚本") {
		t.Fatalf("error=%v", err)
	}
	if len(runner.args) < 3 {
		t.Fatalf("args=%v", runner.args)
	}
	if _, statErr := os.Stat(runner.args[len(runner.args)-1]); !os.IsNotExist(statErr) {
		t.Fatalf("temporary script still exists: %v", statErr)
	}
}

func updateClient(t *testing.T) *http.Client {
	t.Helper()
	return testClient(t, func(req *http.Request) string {
		if req.URL.Path == "/version" {
			return "v1.2.4\n"
		}
		return "#!/usr/bin/env bash\nprintf 'updated\\n'\n"
	})
}

func testUpdater(client *http.Client, runner *runnerStub) Updater {
	return Updater{
		Client: client, Output: io.Discard,
		Installer:  install.Installer{Client: client, Runner: runner, Output: io.Discard},
		VersionURL: "https://official.example/version", ScriptURL: "https://official.example/install.sh",
		AllowedHosts: []string{"official.example"},
	}
}

func testClient(t *testing.T, body func(*http.Request) string) *http.Client {
	t.Helper()
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
