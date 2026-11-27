package system

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"
)

func TestDetailRowsAlignMixedWidthLabelsAndPreserveDetails(t *testing.T) {
	for _, label := range []string{"备份位置", "校验 xray 配置", "脚本 SHA-256", "SNI 校验", "e\u0301"} {
		line := DetailLine(label, "/path with spaces/config.json\n完整错误内容")
		rows := strings.Split(strings.TrimSuffix(line, "\n"), "\n")
		if len(rows) != 2 {
			t.Fatalf("unexpected row count: %q", line)
		}
		for _, row := range rows {
			left, _, found := strings.Cut(row, " │ ")
			if !found || DisplayWidth(left) != 22 {
				t.Fatalf("label %q did not align: %q", label, row)
			}
		}
		if !strings.HasSuffix(rows[0], " │ /path with spaces/config.json") || !strings.HasSuffix(rows[1], " │ 完整错误内容") {
			t.Fatalf("details changed: %q", line)
		}
	}
	longLabel := strings.Repeat("长", 20)
	if !strings.HasPrefix(DetailLine(longLabel, "详情"), longLabel+" │ ") {
		t.Fatal("long operation label was truncated")
	}
}

func TestDetailStreamingKeepsPartialLinesAndOutput(t *testing.T) {
	var output bytes.Buffer
	w := NewLinePrefixWriter(&output, DetailPrefix("服务日志 xray"))
	for _, chunk := range []string{"first\npart", "ial\nlast"} {
		if n, err := io.WriteString(w, chunk); err != nil || n != len(chunk) {
			t.Fatalf("stream write = %d, error = %v", n, err)
		}
	}
	want := "服务日志 xray          │ first\n服务日志 xray          │ partial\n服务日志 xray          │ last"
	if output.String() != want {
		t.Fatalf("stream = %q; want %q", output.String(), want)
	}
}

func TestLoggingRunnerDescribesCommandsAndKeepsFullArguments(t *testing.T) {
	for _, tt := range []struct {
		name    string
		args    []string
		purpose string
	}{
		{"xray", []string{"run", "-test", "-config", "/tmp/path with spaces/config.json"}, "校验 xray 配置"},
		{"systemctl", []string{"restart", "xray.service"}, "重启服务"},
		{"systemctl", []string{"enable", "xray.service"}, "启用开机自启"},
		{"custom-core", []string{"--custom", "自由参数"}, "执行命令"},
	} {
		t.Run(tt.name+"/"+tt.args[0], func(t *testing.T) {
			var output bytes.Buffer
			runner := &LoggingRunner{Runner: outputRunner{}, Out: &output}
			if _, err := runner.Run(context.Background(), tt.name, tt.args...); err != nil {
				t.Fatal(err)
			}
			lines := strings.Split(strings.TrimSuffix(output.String(), "\n"), "\n")
			left, detail, found := strings.Cut(lines[0], " │ ")
			if !found || strings.TrimSpace(left) != tt.purpose || !strings.Contains(detail, tt.args[len(tt.args)-1]) {
				t.Fatalf("command lost purpose or arguments: %s", output.String())
			}
			if len(lines) != 2 || !strings.Contains(lines[1], tt.purpose+"：执行成功") {
				t.Fatalf("command result lost context: %s", output.String())
			}
		})
	}
}

func TestDetailColoringPreservesRawJSON(t *testing.T) {
	var output bytes.Buffer
	w := NewColorWriter(&output, true)
	config := "{\n  \"manual\": \"example │ unchanged\",\n  \"padding\": \"               │ unchanged\"\n}\n"
	if _, err := io.WriteString(w, config); err != nil {
		t.Fatal(err)
	}
	if output.String() != config {
		t.Fatalf("client JSON was altered: %q", output.String())
	}
}
