package install

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"

	"proxyforge/internal/provider/xray"
	"proxyforge/internal/system"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type streamingRunnerStub struct {
	streamErr       error
	bufferedCalled  bool
	streamingCalled bool
	command         string
	args            []string
}

func (r *streamingRunnerStub) Run(context.Context, string, ...string) ([]byte, error) {
	r.bufferedCalled = true
	return nil, errors.New("不应调用缓冲执行")
}

func (r *streamingRunnerStub) RunStreaming(_ context.Context, stdout, stderr io.Writer, command string, args ...string) error {
	r.streamingCalled = true
	r.command = command
	r.args = append([]string(nil), args...)
	_, _ = io.WriteString(stdout, "stdout step\n")
	_, _ = io.WriteString(stderr, "stderr step\n")
	return r.streamErr
}

func TestValidateScript(t *testing.T) {
	ctx := context.Background()
	good := []byte("#!/usr/bin/env bash\necho ok\n")
	if err := validateScript(good); err != nil {
		t.Fatal(err)
	}
	if err := bashSyntax(ctx, good); err != nil {
		t.Fatal(err)
	}
	if err := validateScript([]byte("#!/bin/sh\necho ok\n")); err != nil {
		t.Fatalf("POSIX shell script rejected: %v", err)
	}
	for _, tt := range []struct {
		name string
		body []byte
	}{
		{"empty", nil}, {"nul", []byte("#!/bin/bash\nprintf '\x00'\n")}, {"no shebang", []byte("echo hi\n")}, {"wrong interpreter", []byte("#!/usr/bin/python\nprint('x')\n")}, {"html", []byte("<html>\n")},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if err := validateScript(tt.body); err == nil {
				t.Fatal("expected error")
			}
		})
	}
	if err := bashSyntax(ctx, []byte("#!/bin/bash\nif then\n")); err == nil {
		t.Fatal("expected syntax error")
	}
}

func TestDownloadRejectsRedirectOutsideWhitelist(t *testing.T) {
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusFound, Status: "302 Found", Header: http.Header{"Location": []string{"https://evil.invalid/install.sh"}}, Body: io.NopCloser(strings.NewReader("")), Request: r}, nil
	})
	i := Installer{Client: &http.Client{Transport: transport}}
	client := i.secureClient([]string{"official.example"})
	if _, _, err := download(context.Background(), client, "https://official.example/install.sh", []string{"official.example"}); err == nil || !strings.Contains(err.Error(), "白名单") {
		t.Fatalf("error=%v", err)
	}
}

func TestDownloadEmptyAndOversize(t *testing.T) {
	for _, tt := range []struct {
		name string
		body []byte
		want string
	}{{"empty", nil, ""}, {"oversize", make([]byte, MaxScriptSize+1), "超过"}} {
		t.Run(tt.name, func(t *testing.T) {
			transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: http.StatusOK, Status: "200 OK", Header: make(http.Header), Body: io.NopCloser(strings.NewReader(string(tt.body))), Request: r}, nil
			})
			i := Installer{Client: &http.Client{Transport: transport}}
			client := i.secureClient([]string{"official.example"})
			b, _, err := download(context.Background(), client, "https://official.example/install.sh", []string{"official.example"})
			if tt.name == "empty" {
				if err != nil || len(b) != 0 {
					t.Fatalf("body=%d error=%v", len(b), err)
				}
			} else if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error=%v", err)
			}
		})
	}
}

func TestTrustPinning(t *testing.T) {
	i := Installer{Layout: system.Layout{Root: t.TempDir()}, Output: io.Discard}
	h1 := strings.Repeat("a", 64)
	h2 := strings.Repeat("b", 64)
	if err := i.trust("sing-box", h1, Options{NonInteractive: true}); err == nil || !strings.Contains(err.Error(), "必须提供") {
		t.Fatalf("error=%v", err)
	}
	if err := i.trust("sing-box", h1, Options{NonInteractive: true, TrustScriptSHA256: h1}); err != nil {
		t.Fatal(err)
	}
	if err := i.trust("sing-box", h2, Options{NonInteractive: true, TrustScriptSHA256: h2}); err == nil || !strings.Contains(err.Error(), "已变化") {
		t.Fatalf("error=%v", err)
	}
	confirmed := false
	err := i.trust("sing-box", h2, Options{Confirm: func(string) (bool, error) { confirmed = true; return true, nil }})
	if err != nil {
		t.Fatal(err)
	}
	if !confirmed {
		t.Fatal("changed script was not confirmed")
	}
	b, err := os.ReadFile(i.Layout.TrustPath("sing-box"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(b)) != h2 {
		t.Fatalf("pin=%q", b)
	}
	if err := i.trust("xray", h1, Options{NonInteractive: true, TrustScriptSHA256: h2}); err == nil || !strings.Contains(err.Error(), "不匹配") {
		t.Fatalf("error=%v", err)
	}
}

func TestExecuteScriptStreamsOutput(t *testing.T) {
	runner := &streamingRunnerStub{}
	var output strings.Builder
	i := Installer{Runner: runner, Output: &output}

	if err := i.executeScript(context.Background(), []string{"/tmp/install.sh"}); err != nil {
		t.Fatal(err)
	}
	if !runner.streamingCalled || runner.bufferedCalled {
		t.Fatalf("streaming=%v buffered=%v", runner.streamingCalled, runner.bufferedCalled)
	}
	for _, want := range []string{
		"[状态] 开始执行，以下为实时输出",
		"[Bash] stdout step",
		"[Bash] stderr step",
		"[状态] 执行完成",
	} {
		if !strings.Contains(output.String(), want) {
			t.Fatalf("output missing %q: %s", want, output.String())
		}
	}
}

func TestExecutePreparedScriptAttachedForwardsNativeStreams(t *testing.T) {
	runner := &streamingRunnerStub{}
	var stdout, stderr strings.Builder
	i := Installer{Runner: runner, Output: io.Discard}
	script := DownloadedScript{Content: []byte("#!/usr/bin/env bash\nexit 0\n")}

	if err := i.ExecutePreparedScriptAttached(context.Background(), script, &stdout, &stderr, "uninstall"); err != nil {
		t.Fatal(err)
	}
	if !runner.streamingCalled || runner.bufferedCalled {
		t.Fatalf("streaming=%v buffered=%v", runner.streamingCalled, runner.bufferedCalled)
	}
	if runner.command != "bash" || len(runner.args) != 2 || runner.args[1] != "uninstall" {
		t.Fatalf("command=%q args=%v", runner.command, runner.args)
	}
	if got := stdout.String(); got != "stdout step\n" {
		t.Fatalf("stdout=%q", got)
	}
	if got := stderr.String(); got != "stderr step\n" {
		t.Fatalf("stderr=%q", got)
	}
	if strings.Contains(stdout.String()+stderr.String(), "[Bash]") {
		t.Fatalf("attached output was decorated: stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
	if _, err := os.Stat(runner.args[0]); !os.IsNotExist(err) {
		t.Fatalf("temporary script still exists: %v", err)
	}
}

func TestRunPassesRuntimeProxyToXrayScript(t *testing.T) {
	script := []byte("#!/usr/bin/env bash\nexit 0\n")
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Status: "200 OK", Header: make(http.Header), Body: io.NopCloser(strings.NewReader(string(script))), Request: r}, nil
	})
	runner := &streamingRunnerStub{}
	proxyURL, err := url.Parse("http://user:secret@127.0.0.1:7890")
	if err != nil {
		t.Fatal(err)
	}
	var output strings.Builder
	i := Installer{
		Client: &http.Client{Transport: transport}, Runner: runner,
		Layout: system.Layout{Root: t.TempDir()}, Output: &output,
		ProxyForRequest: func(*http.Request) (*url.URL, error) { return proxyURL, nil },
	}
	if _, err := i.Run(context.Background(), xray.New(), Options{
		NonInteractive: true, TrustScriptSHA256: system.SHA256(script),
	}); err != nil {
		t.Fatal(err)
	}
	if len(runner.args) != 4 || runner.args[1] != "install" || runner.args[2] != "--proxy" || runner.args[3] != proxyURL.String() {
		t.Fatalf("args=%v, want <script> install --proxy <runtime proxy>", runner.args)
	}
	if !strings.Contains(output.String(), "检测到运行时代理") || strings.Contains(output.String(), proxyURL.String()) {
		t.Fatalf("proxy notice missing or proxy URL leaked: %s", output.String())
	}
	for _, want := range []string{
		"[信息] 检测到运行时代理",
		"[命令] 执行命令：bash -n",
		"[官方脚本/信息] 来源：",
		"[官方脚本/信息] SHA-256：",
		"[官方脚本/风险]",
		"[Bash] stdout step",
		"[状态] 执行完成",
	} {
		if !strings.Contains(output.String(), want) {
			t.Fatalf("labeled output missing %q: %s", want, output.String())
		}
	}
}

func TestUninstallRunsXrayOfficialRemoveAction(t *testing.T) {
	script := []byte("#!/usr/bin/env bash\nexit 0\n")
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Status: "200 OK", Header: make(http.Header), Body: io.NopCloser(strings.NewReader(string(script))), Request: r}, nil
	})
	runner := &streamingRunnerStub{}
	i := Installer{
		Client: &http.Client{Transport: transport}, Runner: runner,
		Layout: system.Layout{Root: t.TempDir()}, Output: io.Discard,
	}
	if err := i.Uninstall(context.Background(), xray.New(), Options{
		NonInteractive: true, TrustScriptSHA256: system.SHA256(script),
	}); err != nil {
		t.Fatal(err)
	}
	if runner.command != "bash" || len(runner.args) != 2 || runner.args[1] != "remove" {
		t.Fatalf("command=%q args=%v, want bash <script> remove", runner.command, runner.args)
	}
}

func TestExecuteScriptFailureIncludesRecentOutput(t *testing.T) {
	runner := &streamingRunnerStub{streamErr: errors.New("exit status 1")}
	var output strings.Builder
	i := Installer{Runner: runner, Output: &output}

	err := i.executeScript(context.Background(), []string{"/tmp/install.sh"})
	if err == nil {
		t.Fatal("expected script failure")
	}
	for _, want := range []string{"stdout step", "stderr step", "exit status 1"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error missing %q: %v", want, err)
		}
	}
	if !strings.Contains(output.String(), "stdout step") || !strings.Contains(output.String(), "stderr step") {
		t.Fatalf("script output was not forwarded: %s", output.String())
	}
}

func TestScriptOutputCaptureKeepsBoundedTail(t *testing.T) {
	capture := &scriptOutputCapture{output: io.Discard, limit: 5}
	_, _ = capture.Write([]byte("123"))
	_, _ = capture.Write([]byte("4567"))
	if got := capture.String(); got != "34567" {
		t.Fatalf("tail=%q", got)
	}
}
