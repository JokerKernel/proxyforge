package cli

import (
	"bufio"
	"bytes"
	"context"
	"reflect"
	"strings"
	"testing"

	"proxyforge/internal/app"
	"proxyforge/internal/domain"
	"proxyforge/internal/provider"
	"proxyforge/internal/provider/singbox"
	"proxyforge/internal/provider/xray"
	"proxyforge/internal/system"
)

func TestServiceMenuLiveLogsReturnsAfterInterrupt(t *testing.T) {
	runner := &liveLogRunner{}
	a := &app.App{
		Registry:  provider.NewRegistry(singbox.New(), xray.New()),
		Services:  system.ServiceManager{Runner: runner},
		RootCheck: func() error { return nil },
	}
	var out, errOut bytes.Buffer
	c := &commandSet{
		app: a, reader: bufio.NewReader(strings.NewReader("6\n0\n")), out: &out, errOut: &errOut,
		interruptContext: func(parent context.Context) (context.Context, context.CancelFunc) {
			child, cancel := context.WithCancel(parent)
			cancel()
			return child, func() {}
		},
	}
	if err := c.serviceMenu(context.Background(), domain.CoreXray); err != nil {
		t.Fatal(err)
	}
	wantArgs := []string{"-u", "xray.service", "-n", "100", "-f", "--no-pager"}
	if runner.name != "journalctl" || !reflect.DeepEqual(runner.args, wantArgs) {
		t.Fatalf("command=%s args=%v, want journalctl %v", runner.name, runner.args, wantArgs)
	}
	for _, want := range []string{"实时日志", "live entry", "已停止实时日志", "ProxyForge  ›  xray  ›  服务管理"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("output missing %q: %q", want, out.String())
		}
	}
	if strings.Contains(errOut.String(), "操作失败") {
		t.Fatalf("interrupt was reported as an error: %q", errOut.String())
	}
}

func TestServiceMenuOffersLogLevelSettings(t *testing.T) {
	var out bytes.Buffer
	c := &commandSet{reader: bufio.NewReader(strings.NewReader("0\n")), out: &out}
	if err := c.serviceMenu(context.Background(), domain.CoreSingBox); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "7   日志级别") {
		t.Fatalf("service menu output=%q", out.String())
	}
	if got := logLevelDisplay(domain.CoreSingBox, "info"); !strings.Contains(got, "ProxyForge 默认") {
		t.Fatalf("sing-box info display=%q", got)
	}
	if got := logLevelDisplay(domain.CoreXray, "warning"); !strings.Contains(got, "ProxyForge 默认") {
		t.Fatalf("xray warning display=%q", got)
	}
}
