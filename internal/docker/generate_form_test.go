package docker

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"proxyforge/internal/docker/model"
)

func TestAskGenerateRealityFlowMatchesMainline(t *testing.T) {
	m, _ := testManager(t)
	m.physicalIPs = func() ([]publicAddress, error) {
		return []publicAddress{{Interface: "eth0", Address: "203.0.113.10", Private: false}}, nil
	}
	m.probeSNI = func(_ context.Context, _ []string, _ string, _ int) ([]sniCandidate, error) {
		return []sniCandidate{{Domain: "www.bing.com", Latency: 5 * time.Millisecond, TLSVersion: "1.3", CertificateSANs: []string{"www.bing.com"}}}, nil
	}
	var out bytes.Buffer
	opts, err := m.askGenerateUI(context.Background(), newUI(strings.NewReader(strings.Repeat("\n", 11)+"1\n\n"), &out), model.GenerateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if opts.Server != "203.0.113.10" || opts.Port != 443 || opts.SNI != "www.bing.com" || opts.Target != "www.bing.com:443" {
		t.Fatalf("address options=%#v", opts)
	}
	if !opts.XrayFallbackGuard || opts.StandardConfig || !opts.ApplyFallbackPolicy || opts.XrayFallbackHTTPDomain || opts.XrayFallbackExactDomain {
		t.Fatalf("mode options=%#v", opts)
	}
	if opts.XrayFallbackPort < 1024 || opts.XrayFallbackPort > 65535 || opts.XrayFallbackPort == opts.Port || opts.UserName != "one" || opts.InboundTag != "docker-one" {
		t.Fatalf("identity options=%#v", opts)
	}
	for _, want := range []string{
		"HTTP 回落域名限制", "不限制 Host       -- 默认", "严格匹配回落域名", "允许子域名        -- 默认",
		"公网地址获取方式", "物理网卡", "api.ipify.org", "监听端口 [443]", "本机 tunnel 回落端口", "用户名称", "入站标签",
		"REALITY SNI（输入域名；直接回车自动测速候选）", "REALITY SNI 候选检测",
		"配置确认：REALITY 回落目标", "Target：www.bing.com:443", "确认 SNI 和 REALITY target？",
	} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("output missing %q:\n%s", want, out.String())
		}
	}
	if strings.Contains(out.String(), "REALITY target:") || strings.Contains(out.String(), "REALITY target 必须为 host:port") {
		t.Fatalf("target was asked as a blank field:\n%s", out.String())
	}
}

func TestAskGenerateUsesHighPortWhen443IsBusy(t *testing.T) {
	m, _ := testManager(t)
	m.portFree = func(port int) error {
		if port == 443 {
			return fmt.Errorf("端口 %d 已占用或不可用", port)
		}
		return nil
	}
	m.physicalIPs = func() ([]publicAddress, error) {
		return []publicAddress{{Interface: "eth0", Address: "203.0.113.10", Private: false}}, nil
	}
	m.probeSNI = func(_ context.Context, _ []string, _ string, _ int) ([]sniCandidate, error) {
		return []sniCandidate{{Domain: "www.bing.com", Latency: 5 * time.Millisecond, TLSVersion: "1.3", CertificateSANs: []string{"www.bing.com"}}}, nil
	}
	var out bytes.Buffer
	opts, err := m.askGenerateUI(context.Background(), newUI(strings.NewReader(strings.Repeat("\n", 11)+"1\n\n"), &out), model.GenerateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if opts.Port < model.FallbackPortMin || opts.Port > model.FallbackPortMax || opts.Port == 443 {
		t.Fatalf("expected a random high port, got %d", opts.Port)
	}
	if !strings.Contains(out.String(), fmt.Sprintf("443 已被占用，默认使用随机高位端口 %d。", opts.Port)) || !strings.Contains(out.String(), fmt.Sprintf("监听端口 [%d]", opts.Port)) {
		t.Fatalf("high-port default was not shown:\n%s", out.String())
	}
}

func TestPreferredListenPortKeepsExistingHighPort(t *testing.T) {
	m, _ := testManager(t)
	m.portFree = func(port int) error {
		if port == 443 {
			return fmt.Errorf("busy")
		}
		return nil
	}
	port, err := m.preferredListenPort(34567)
	if err != nil || port != 34567 {
		t.Fatalf("existing high port = %d, %v", port, err)
	}
	port, err = m.preferredListenPort(443)
	if err != nil || port != 443 {
		t.Fatalf("owned 443 = %d, %v", port, err)
	}
}

func TestAskGenerateCanRestrictFallbackDomains(t *testing.T) {
	m, _ := testManager(t)
	m.physicalIPs = func() ([]publicAddress, error) {
		return []publicAddress{{Interface: "eth0", Address: "203.0.113.10"}}, nil
	}
	m.probeSNI = func(_ context.Context, candidates []string, _ string, _ int) ([]sniCandidate, error) {
		return []sniCandidate{{Domain: candidates[0], Latency: time.Millisecond, TLSVersion: "1.3"}}, nil
	}
	var out bytes.Buffer
	// protocol, mode, HTTP restrict, exact match, address method, server, port, fallback, user, tag, empty SNI, candidate, confirm
	input := "\n\n2\n2\n" + strings.Repeat("\n", 7) + "1\n\n"
	opts, err := m.askGenerateUI(context.Background(), newUI(strings.NewReader(input), &out), model.GenerateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !opts.XrayFallbackGuard || !opts.XrayFallbackHTTPDomain || !opts.XrayFallbackExactDomain || opts.SNI != "www.bing.com" {
		t.Fatalf("options=%#v", opts)
	}
}

func TestGenerateApplyFallbackPolicyCanClearDomainFlags(t *testing.T) {
	m, _ := prepared(t)
	base := model.GenerateOptions{
		Server: "example.com", Port: 8443, SNI: "www.example.com", Target: "www.example.com:443",
		XrayFallbackGuard: true, XrayFallbackPort: 62000,
		XrayFallbackHTTPDomain: true, XrayFallbackExactDomain: true,
	}
	if err := m.Generate(context.Background(), base, true); err != nil {
		t.Fatal(err)
	}
	kept := base
	kept.XrayFallbackHTTPDomain = false
	kept.XrayFallbackExactDomain = false
	if err := m.Generate(context.Background(), kept, true); err != nil {
		t.Fatal(err)
	}
	state, err := m.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	if !state.Node.XrayFallbackHTTPDomain || !state.Node.XrayFallbackExactDomain {
		t.Fatalf("cli omission cleared flags: %+v", state.Node)
	}
	cleared := kept
	cleared.ApplyFallbackPolicy = true
	cleared.XrayFallbackGuard = true
	if err := m.Generate(context.Background(), cleared, true); err != nil {
		t.Fatal(err)
	}
	state, err = m.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	if !state.Node.XrayFallbackGuard || state.Node.XrayFallbackHTTPDomain || state.Node.XrayFallbackExactDomain {
		t.Fatalf("menu choice did not replace flags: %+v", state.Node)
	}
}
