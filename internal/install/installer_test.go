package install

import (
	"context"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"

	"proxyforge/internal/system"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

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
