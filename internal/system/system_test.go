package system

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"proxyforge/internal/domain"
)

type recordingRunner struct {
	name string
	args []string
}

func (r *recordingRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	r.name = name
	r.args = append([]string(nil), args...)
	return nil, nil
}

type outputRunner struct{ output []byte }

func (r outputRunner) Run(context.Context, string, ...string) ([]byte, error) {
	return r.output, nil
}

type streamingRecordingRunner struct {
	name string
	args []string
}

func (r *streamingRecordingRunner) Run(context.Context, string, ...string) ([]byte, error) {
	return nil, nil
}

func (r *streamingRecordingRunner) RunStreaming(_ context.Context, stdout, _ io.Writer, name string, args ...string) error {
	r.name = name
	r.args = append([]string(nil), args...)
	_, _ = io.WriteString(stdout, "live log line\n")
	return nil
}

type canceledStreamingRunner struct{}

func (canceledStreamingRunner) Run(context.Context, string, ...string) ([]byte, error) {
	return nil, context.Canceled
}

func (canceledStreamingRunner) RunStreaming(context.Context, io.Writer, io.Writer, string, ...string) error {
	return context.Canceled
}

func TestServiceManagerEnable(t *testing.T) {
	runner := &recordingRunner{}
	if err := (ServiceManager{Runner: runner}).Enable(context.Background(), "sing-box.service"); err != nil {
		t.Fatal(err)
	}
	if runner.name != "systemctl" || !reflect.DeepEqual(runner.args, []string{"enable", "sing-box.service"}) {
		t.Fatalf("command=%s args=%v", runner.name, runner.args)
	}
}

func TestServiceManagerUninstallActions(t *testing.T) {
	runner := &recordingRunner{}
	manager := ServiceManager{Runner: runner}
	if err := manager.DisableNow(context.Background(), "sing-box.service"); err != nil {
		t.Fatal(err)
	}
	if runner.name != "systemctl" || !reflect.DeepEqual(runner.args, []string{"disable", "--now", "sing-box.service"}) {
		t.Fatalf("disable command=%s args=%v", runner.name, runner.args)
	}
	if err := manager.DaemonReload(context.Background()); err != nil {
		t.Fatal(err)
	}
	if runner.name != "systemctl" || !reflect.DeepEqual(runner.args, []string{"daemon-reload"}) {
		t.Fatalf("reload command=%s args=%v", runner.name, runner.args)
	}
}

func TestServiceManagerFollowsJournalLogs(t *testing.T) {
	runner := &streamingRecordingRunner{}
	var output bytes.Buffer
	err := (ServiceManager{Runner: runner}).FollowLogs(context.Background(), "xray.service", &output)
	if err != nil {
		t.Fatal(err)
	}
	wantArgs := []string{"-u", "xray.service", "-n", "100", "-f", "--no-pager"}
	if runner.name != "journalctl" || !reflect.DeepEqual(runner.args, wantArgs) {
		t.Fatalf("command=%s args=%v, want journalctl %v", runner.name, runner.args, wantArgs)
	}
	if output.String() != "[服务日志/xray] live log line\n" {
		t.Fatalf("output=%q", output.String())
	}
}

func TestServiceManagerLabelsBufferedOutput(t *testing.T) {
	runner := outputRunner{output: []byte("line one\nline two\n")}
	manager := ServiceManager{Runner: runner}
	logs, err := manager.Action(context.Background(), "sing-box.service", "logs")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(logs), "[服务日志/sing-box] line one\n[服务日志/sing-box] line two\n"; got != want {
		t.Fatalf("logs=%q, want %q", got, want)
	}
	status, err := manager.Action(context.Background(), "sing-box.service", "status")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(status), "[系统命令/输出] line one\n[系统命令/输出] line two\n"; got != want {
		t.Fatalf("status=%q, want %q", got, want)
	}
}

func TestLinePrefixWriterHandlesFragmentedLines(t *testing.T) {
	var output bytes.Buffer
	w := NewLinePrefixWriter(&output, "[来源] ")
	if _, err := io.WriteString(w, "first\npart"); err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(w, "ial\nlast"); err != nil {
		t.Fatal(err)
	}
	if got, want := output.String(), "[来源] first\n[来源] partial\n[来源] last"; got != want {
		t.Fatalf("output=%q, want %q", got, want)
	}
}

func TestColorWriterUsesSemanticTerminalColors(t *testing.T) {
	var output bytes.Buffer
	w := NewColorWriter(&output, true)
	for _, line := range []string{
		"========================================\n",
		"1) 安装/升级内核\n",
		"[ProxyForge/步骤] 检查环境\n",
		"[ProxyForge/警告] 谨慎操作\n",
		"[ProxyForge/结果] 操作成功\n",
		"[系统命令/输出] system output\n",
		"[官方脚本/风险] 将以 root 执行\n",
		"[服务日志/xray] accepted connection\n",
		"请输入 yes/y 确认；其他输入取消： ",
	} {
		if _, err := io.WriteString(w, line); err != nil {
			t.Fatal(err)
		}
	}

	got := output.String()
	for _, want := range []string{
		"\x1b[1;36m========================================\x1b[0m",
		"\x1b[1;32m1)\x1b[0m 安装/升级内核",
		"\x1b[1;36m[ProxyForge/步骤]\x1b[0m",
		"\x1b[1;33m[ProxyForge/警告]\x1b[0m",
		"\x1b[1;32m[ProxyForge/结果]\x1b[0m",
		"\x1b[90m[系统命令/输出]\x1b[0m",
		"\x1b[1;33m[官方脚本/风险]\x1b[0m",
		"\x1b[35m[服务日志/xray]\x1b[0m",
		"\x1b[1;33m请输入 yes/y 确认；其他输入取消： \x1b[0m",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("colored output missing %q: %q", want, got)
		}
	}
}

func TestColorWriterDisabledKeepsPlainOutput(t *testing.T) {
	var output bytes.Buffer
	w := NewColorWriter(&output, false)
	want := "[ProxyForge/错误] 操作失败\n1) 返回\n"
	if _, err := io.WriteString(w, want); err != nil {
		t.Fatal(err)
	}
	if got := output.String(); got != want {
		t.Fatalf("plain output=%q, want %q", got, want)
	}
	if strings.Contains(output.String(), "\x1b[") {
		t.Fatalf("disabled output contains ANSI controls: %q", output.String())
	}
}

func TestTerminalColorWriterHonorsColorModeOverride(t *testing.T) {
	t.Run("always", func(t *testing.T) {
		t.Setenv("PROXYFORGE_COLOR", "always")
		var output bytes.Buffer
		if _, err := io.WriteString(NewTerminalColorWriter(&output), "[ProxyForge/结果] 完成\n"); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(output.String(), "\x1b[1;32m[ProxyForge/结果]\x1b[0m") {
			t.Fatalf("forced color output=%q", output.String())
		}
	})

	t.Run("never", func(t *testing.T) {
		t.Setenv("PROXYFORGE_COLOR", "never")
		var output bytes.Buffer
		want := "[ProxyForge/结果] 完成\n"
		if _, err := io.WriteString(NewTerminalColorWriter(&output), want); err != nil {
			t.Fatal(err)
		}
		if got := output.String(); got != want {
			t.Fatalf("disabled terminal color output=%q, want %q", got, want)
		}
	})
}

func TestExecRunnerStreamsStdoutAndStderr(t *testing.T) {
	var stdout, stderr bytes.Buffer
	err := (ExecRunner{}).RunStreaming(
		context.Background(),
		&stdout,
		&stderr,
		"bash",
		"-c",
		"printf stdout-data; printf stderr-data >&2",
	)
	if err != nil {
		t.Fatal(err)
	}
	if stdout.String() != "stdout-data" {
		t.Fatalf("stdout=%q", stdout.String())
	}
	if stderr.String() != "stderr-data" {
		t.Fatalf("stderr=%q", stderr.String())
	}
}

func TestLoggingRunnerReportsCommandWithoutLeakingBufferedOutput(t *testing.T) {
	var log bytes.Buffer
	runner := &LoggingRunner{Runner: outputRunner{output: []byte("private-key-output")}, Out: &log}
	b, err := runner.Run(context.Background(), "sing-box", "generate", "reality-keypair")
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "private-key-output" {
		t.Fatalf("output=%q", b)
	}
	for _, want := range []string{"[ProxyForge/命令] 执行命令：sing-box generate reality-keypair", "[ProxyForge/命令] 命令完成：sing-box"} {
		if !strings.Contains(log.String(), want) {
			t.Fatalf("log missing %q: %s", want, log.String())
		}
	}
	if strings.Contains(log.String(), "private-key-output") {
		t.Fatalf("buffered command output leaked into log: %s", log.String())
	}
}

func TestLoggingRunnerReportsCanceledStreamingCommandAsStopped(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var log bytes.Buffer
	runner := &LoggingRunner{Runner: canceledStreamingRunner{}, Out: &log}
	if err := runner.RunStreaming(ctx, io.Discard, io.Discard, "journalctl", "-f"); !errors.Is(err, context.Canceled) {
		t.Fatalf("error=%v, want context cancellation", err)
	}
	if !strings.Contains(log.String(), "[ProxyForge/命令] 命令已停止：journalctl") || strings.Contains(log.String(), "命令失败") {
		t.Fatalf("unexpected cancellation log: %s", log.String())
	}
}

func TestLoggingRunnerRedactsProxyArgument(t *testing.T) {
	var log bytes.Buffer
	runner := &LoggingRunner{Runner: outputRunner{}, Out: &log}
	proxyURL := "http://user:secret@127.0.0.1:7890"
	if _, err := runner.Run(context.Background(), "bash", "/tmp/install.sh", "install", "--proxy", proxyURL); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(log.String(), proxyURL) || strings.Contains(log.String(), "secret") {
		t.Fatalf("proxy URL leaked into log: %s", log.String())
	}
	if !strings.Contains(log.String(), "--proxy [REDACTED]") {
		t.Fatalf("redacted proxy argument missing: %s", log.String())
	}
}

func TestRandomCredentialFormats(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 64; i++ {
		u, err := UUID()
		if err != nil {
			t.Fatal(err)
		}
		if !regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`).MatchString(u) {
			t.Fatalf("bad UUID %q", u)
		}
		s, err := ShortID()
		if err != nil {
			t.Fatal(err)
		}
		if !regexp.MustCompile(`^[0-9a-f]{16}$`).MatchString(s) {
			t.Fatalf("bad short ID %q", s)
		}
		if seen[s] {
			t.Fatalf("duplicate short ID %q", s)
		}
		seen[s] = true
	}
}

func TestValidation(t *testing.T) {
	for _, tt := range []struct {
		name string
		err  error
		want bool
	}{
		{"port zero", ValidatePort(0), true}, {"port max", ValidatePort(65535), false},
		{"sni ip", ValidateSNI("1.1.1.1"), true}, {"sni domain", ValidateSNI("www.example.com"), false},
		{"target missing port", ValidateTarget("example.com"), true}, {"target good", ValidateTarget("example.com:443"), false},
		{"user name", ValidateUserName("phone"), false}, {"empty user name", ValidateUserName(""), true},
		{"user name control", ValidateUserName("phone\nadmin"), true},
		{"tag", ValidateTag("home-in"), false}, {"empty tag", ValidateTag(""), true},
		{"tag control", ValidateTag("home\nin"), true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if (tt.err != nil) != tt.want {
				t.Fatalf("error=%v wantErr=%v", tt.err, tt.want)
			}
		})
	}
}

func TestStatePermissionsAndIsolation(t *testing.T) {
	root := t.TempDir()
	store := StateStore{Layout: Layout{Root: root}}
	n := domain.NodeSpec{ManagedBy: "proxyforge", Core: "sing-box", Server: "example.com", UpdatedAt: time.Now()}
	if err := store.Save(n); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(store.Layout.StatePath("sing-box"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("state mode=%o", info.Mode().Perm())
	}
	if _, err := store.Load("xray"); err != ErrNoState {
		t.Fatalf("xray state leaked: %v", err)
	}
	got, err := store.Load("sing-box")
	if err != nil {
		t.Fatal(err)
	}
	if got.Server != n.Server {
		t.Fatalf("got %#v", got)
	}
}

func TestStateRejectsUnsafePermissions(t *testing.T) {
	root := t.TempDir()
	store := StateStore{Layout: Layout{Root: root}}
	n := domain.NodeSpec{ManagedBy: "proxyforge", Core: "sing-box"}
	if err := store.Save(n); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(store.Layout.StatePath("sing-box"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load("sing-box"); err == nil {
		t.Fatal("expected unsafe permission error")
	}
}

func TestAtomicWriteAndBackupPermissions(t *testing.T) {
	root := t.TempDir()
	path := root + "/config/config.json"
	if err := AtomicWrite(path, []byte("old"), 0640); err != nil {
		t.Fatal(err)
	}
	backup, err := BackupFile(path, root+"/backups", time.Unix(1, 0))
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(backup)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "old" {
		t.Fatalf("backup=%q", b)
	}
	info, _ := os.Stat(backup)
	if info.Mode().Perm() != 0600 {
		t.Fatalf("backup mode=%o", info.Mode().Perm())
	}
	if err := AtomicWrite(path, []byte("new"), 0600); err != nil {
		t.Fatal(err)
	}
	b, _ = os.ReadFile(path)
	if string(b) != "new" {
		t.Fatalf("atomic result=%q", b)
	}
}

func TestBackupFileKeepsNewestThreeBackups(t *testing.T) {
	root := t.TempDir()
	path := root + "/config/config.json"
	backupRoot := root + "/backups"
	if err := os.MkdirAll(backupRoot+"/manual-notes", 0700); err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 5; i++ {
		content := []byte(fmt.Sprintf("version-%d", i))
		if err := AtomicWrite(path, content, 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := BackupFile(path, backupRoot, time.Unix(int64(i), 0)); err != nil {
			t.Fatal(err)
		}
	}

	entries, err := os.ReadDir(backupRoot)
	if err != nil {
		t.Fatal(err)
	}
	var backups []string
	for _, entry := range entries {
		if _, err := time.Parse(backupTimestampLayout, entry.Name()); err == nil {
			backups = append(backups, entry.Name())
		}
	}
	if len(backups) != 3 {
		t.Fatalf("backup directories=%v, want newest three", backups)
	}
	for i, name := range backups {
		b, err := os.ReadFile(filepath.Join(backupRoot, name, "config.json"))
		if err != nil {
			t.Fatal(err)
		}
		want := fmt.Sprintf("version-%d", i+3)
		if string(b) != want {
			t.Fatalf("backup %s=%q, want %q", name, b, want)
		}
	}
	if _, err := os.Stat(backupRoot + "/manual-notes"); err != nil {
		t.Fatalf("unrecognized directory was removed: %v", err)
	}
}

func TestRemovePackageUsesDistributionNativeDatabase(t *testing.T) {
	for _, tt := range []struct {
		name, osRelease, command string
		args                     []string
	}{
		{name: "debian", osRelease: "ID=ubuntu\nID_LIKE=debian\n", command: "dpkg", args: []string{"--remove", "sing-box"}},
		{name: "rhel", osRelease: "ID=rocky\nID_LIKE=\"rhel centos fedora\"\n", command: "rpm", args: []string{"-e", "sing-box"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			if err := os.MkdirAll(root+"/etc", 0755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(root+"/etc/os-release", []byte(tt.osRelease), 0644); err != nil {
				t.Fatal(err)
			}
			runner := &recordingRunner{}
			if err := RemovePackage(context.Background(), runner, Layout{Root: root}, "sing-box", io.Discard); err != nil {
				t.Fatal(err)
			}
			if runner.name != tt.command || strings.Join(runner.args, " ") != strings.Join(tt.args, " ") {
				t.Fatalf("command=%q args=%v, want %q %v", runner.name, runner.args, tt.command, tt.args)
			}
		})
	}
}

func TestRemovePackageLabelsStreamingCommandOutput(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(root+"/etc", 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(root+"/etc/os-release", []byte("ID=debian\n"), 0644); err != nil {
		t.Fatal(err)
	}
	runner := &streamingRecordingRunner{}
	var output bytes.Buffer
	if err := RemovePackage(context.Background(), runner, Layout{Root: root}, "sing-box", &output); err != nil {
		t.Fatal(err)
	}
	if got, want := output.String(), "[系统命令/输出] live log line\n"; got != want {
		t.Fatalf("output=%q, want %q", got, want)
	}
}
