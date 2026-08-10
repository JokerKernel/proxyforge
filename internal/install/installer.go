package install

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"proxyforge/internal/provider"
	"proxyforge/internal/system"
)

const MaxScriptSize = 2 << 20
const maxCapturedScriptOutput = 64 << 10

type ConfirmFunc func(summary string) (bool, error)

type Options struct {
	URL               string
	Version           string
	NonInteractive    bool
	TrustScriptSHA256 string
	Confirm           ConfirmFunc
}

type Installer struct {
	Client          *http.Client
	Runner          provider.Runner
	Layout          system.Layout
	Output          io.Writer
	ProxyForRequest func(*http.Request) (*url.URL, error)
}

// DownloadedScript is a management script that has passed source, size,
// format, and bash syntax validation.
type DownloadedScript struct {
	Content   []byte
	SourceURL string
	FinalURL  string
	SHA256    string
}

func (i Installer) Run(ctx context.Context, p provider.CoreProvider, opts Options) (string, error) {
	args := p.InstallArgs(opts.Version)
	if proxyProvider, ok := p.(provider.ScriptProxyProvider); ok {
		proxyURL, err := i.runtimeProxy(p.OfficialScriptURL())
		if err != nil {
			return "", fmt.Errorf("检查运行时代理: %w", err)
		}
		if proxyURL != "" && i.Output != nil {
			fmt.Fprintln(i.Output, "[ProxyForge/信息] 检测到运行时代理，将传递给官方管理脚本。")
		}
		args = append(args, proxyProvider.ScriptProxyArgs(proxyURL)...)
	}
	return i.runScript(ctx, p, opts, args, "安装/升级")
}

func (i Installer) Uninstall(ctx context.Context, p provider.CoreProvider, opts Options) error {
	if packageName := p.PackageName(); packageName != "" {
		return system.RemovePackage(ctx, i.Runner, i.Layout, packageName, i.Output)
	}
	args := p.UninstallArgs()
	if len(args) == 0 {
		return fmt.Errorf("%s 未定义卸载方式", p.Name())
	}
	_, err := i.runScript(ctx, p, opts, args, "卸载")
	return err
}

func (i Installer) runScript(ctx context.Context, p provider.CoreProvider, opts Options, scriptArgs []string, operation string) (string, error) {
	scriptURL := opts.URL
	if scriptURL == "" {
		scriptURL = p.OfficialScriptURL()
	}
	script, err := i.PrepareScript(ctx, scriptURL, p.ScriptHosts())
	if err != nil {
		return "", err
	}
	if i.Output != nil {
		fmt.Fprintf(i.Output, "[官方脚本/信息] 来源：%s\n", scriptURL)
		fmt.Fprintf(i.Output, "[官方脚本/信息] 最终地址：%s\n", script.FinalURL)
		fmt.Fprintf(i.Output, "[官方脚本/信息] 大小：%d bytes\n", len(script.Content))
		fmt.Fprintf(i.Output, "[官方脚本/信息] SHA-256：%s\n", script.SHA256)
		fmt.Fprintf(i.Output, "[官方脚本/风险] 将以 root 执行%s操作，可能修改二进制、systemd unit 和软件包文件。\n", operation)
	}
	if err := i.trust(p.Name(), script.SHA256, opts); err != nil {
		return script.SHA256, err
	}
	if err := i.ExecutePreparedScript(ctx, script, nil, scriptArgs...); err != nil {
		return script.SHA256, err
	}
	return script.SHA256, nil
}

// PrepareScript securely downloads and validates a shell script without
// executing it.
func (i Installer) PrepareScript(ctx context.Context, scriptURL string, hosts []string) (DownloadedScript, error) {
	client := i.secureClient(hosts)
	body, finalURL, err := download(ctx, client, scriptURL, hosts)
	if err != nil {
		return DownloadedScript{}, err
	}
	if err := validateScript(body); err != nil {
		return DownloadedScript{}, err
	}
	if err := bashSyntaxLogged(ctx, body, i.Output); err != nil {
		return DownloadedScript{}, err
	}
	return DownloadedScript{
		Content: body, SourceURL: scriptURL, FinalURL: finalURL, SHA256: system.SHA256(body),
	}, nil
}

// ExecutePreparedScript writes a validated script to a private temporary file
// and executes it. Environment entries are applied only to the child process.
func (i Installer) ExecutePreparedScript(ctx context.Context, script DownloadedScript, environment []string, scriptArgs ...string) error {
	// 官方内核安装脚本使用清晰的 xray 前缀，便于用户从执行日志识别脚本用途。
	f, err := os.CreateTemp("", "xray-*.sh")
	if err != nil {
		return err
	}
	path := f.Name()
	defer os.Remove(path)
	if err := f.Chmod(0700); err == nil {
		_, err = f.Write(script.Content)
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	args := append([]string{path}, scriptArgs...)
	if len(environment) == 0 {
		return i.executeScript(ctx, args)
	}
	envArgs := append(append([]string(nil), environment...), "bash")
	envArgs = append(envArgs, args...)
	return i.executeScriptCommand(ctx, "env", envArgs)
}

func (i Installer) executeScript(ctx context.Context, args []string) error {
	return i.executeScriptCommand(ctx, "bash", args)
}

func (i Installer) executeScriptCommand(ctx context.Context, command string, args []string) error {
	output := i.Output
	if output == nil {
		output = io.Discard
	}
	fmt.Fprintln(output, "[官方脚本/状态] 开始执行，以下为实时输出：")
	capture := &scriptOutputCapture{
		output: system.NewLinePrefixWriter(output, "[官方脚本/输出] "),
		limit:  maxCapturedScriptOutput,
	}
	if streaming, ok := i.Runner.(provider.StreamingRunner); ok {
		runErr := streaming.RunStreaming(ctx, capture, capture, command, args...)
		capture.FinishLine()
		if runErr != nil {
			if tail := strings.TrimSpace(capture.String()); tail != "" {
				prefixedTail := strings.TrimSpace(string(system.PrefixLines([]byte(tail), "[官方脚本/输出] ")))
				return fmt.Errorf("官方管理脚本执行失败（末尾输出如下）:\n%s\n%w", prefixedTail, runErr)
			}
			return fmt.Errorf("官方管理脚本执行失败: %w", runErr)
		}
		fmt.Fprintln(output, "[官方脚本/状态] 执行完成。")
		return nil
	}

	b, err := i.Runner.Run(ctx, command, args...)
	if len(b) > 0 {
		_, _ = capture.Write(b)
		if b[len(b)-1] != '\n' {
			_, _ = capture.Write([]byte("\n"))
		}
	}
	if err != nil {
		return fmt.Errorf("官方管理脚本执行失败: %w", err)
	}
	fmt.Fprintln(output, "[官方脚本/状态] 执行完成。")
	return nil
}

type scriptOutputCapture struct {
	mu      sync.Mutex
	output  io.Writer
	tail    []byte
	limit   int
	wrote   bool
	newline bool
}

func (w *scriptOutputCapture) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(p) > 0 {
		w.wrote = true
		w.newline = p[len(p)-1] == '\n'
	}
	if len(p) >= w.limit {
		w.tail = append(w.tail[:0], p[len(p)-w.limit:]...)
	} else {
		overflow := len(w.tail) + len(p) - w.limit
		if overflow > 0 {
			copy(w.tail, w.tail[overflow:])
			w.tail = w.tail[:len(w.tail)-overflow]
		}
		w.tail = append(w.tail, p...)
	}
	return w.output.Write(p)
}

func (w *scriptOutputCapture) FinishLine() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.wrote && !w.newline {
		_, _ = w.output.Write([]byte("\n"))
		w.newline = true
	}
}

func (w *scriptOutputCapture) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return string(w.tail)
}

func (i Installer) secureClient(hosts []string) *http.Client {
	base := i.Client
	if base == nil {
		base = &http.Client{Timeout: 45 * time.Second}
	}
	c := *base
	c.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= 5 {
			return fmt.Errorf("重定向次数过多")
		}
		return checkURL(req.URL, hosts)
	}
	return &c
}

func (i Installer) runtimeProxy(rawURL string) (string, error) {
	req, err := http.NewRequest(http.MethodGet, rawURL, nil)
	if err != nil {
		return "", err
	}
	proxyForRequest := i.ProxyForRequest
	if proxyForRequest == nil {
		proxyForRequest = http.ProxyFromEnvironment
	}
	proxyURL, err := proxyForRequest(req)
	if err != nil {
		return "", err
	}
	if proxyURL == nil {
		return "", nil
	}
	return proxyURL.String(), nil
}

func (i Installer) trust(core, hash string, opts Options) error {
	path := i.Layout.TrustPath(core)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	if err := os.Chmod(filepath.Dir(path), 0700); err != nil {
		return err
	}
	oldBytes, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	old := strings.TrimSpace(string(oldBytes))
	expected := strings.ToLower(strings.TrimSpace(opts.TrustScriptSHA256))
	if expected != "" && (len(expected) != 64 || expected != hash) {
		return fmt.Errorf("--trust-script-sha256 与下载内容不匹配；实际为 %s", hash)
	}
	if opts.NonInteractive {
		if expected == "" {
			return fmt.Errorf("非交互模式必须提供 --trust-script-sha256=%s；--yes 不能绕过脚本信任", hash)
		}
		if old != "" && old != hash {
			return fmt.Errorf("脚本哈希已变化（固定值 %s，当前 %s）；请先交互式重新确认", old, hash)
		}
		return system.AtomicWrite(path, []byte(hash+"\n"), 0600)
	}
	if old == hash {
		return nil
	}
	if opts.Confirm == nil {
		return fmt.Errorf("首次或变更后的脚本必须交互确认 SHA-256 %s", hash)
	}
	message := "首次信任此脚本"
	if old != "" {
		message = fmt.Sprintf("脚本哈希已由 %s 变为 %s，重新信任", old, hash)
	}
	ok, err := opts.Confirm(message + "？")
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("用户拒绝信任安装脚本")
	}
	return system.AtomicWrite(path, []byte(hash+"\n"), 0600)
}

func download(ctx context.Context, client *http.Client, rawURL string, hosts []string) ([]byte, string, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, "", err
	}
	if err := checkURL(u, hosts); err != nil {
		return nil, "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("Accept", "text/x-shellscript,text/plain;q=0.9,*/*;q=0.1")
	resp, err := client.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("下载安装脚本: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("安装脚本 HTTP 状态 %s", resp.Status)
	}
	r := io.LimitReader(resp.Body, MaxScriptSize+1)
	b, err := io.ReadAll(r)
	if err != nil {
		return nil, "", err
	}
	if len(b) > MaxScriptSize {
		return nil, "", fmt.Errorf("安装脚本超过 %d bytes", MaxScriptSize)
	}
	return b, resp.Request.URL.String(), nil
}

func checkURL(u *url.URL, hosts []string) error {
	if u.Scheme != "https" {
		return fmt.Errorf("安装脚本只允许 HTTPS")
	}
	host := strings.ToLower(u.Hostname())
	for _, allowed := range hosts {
		if host == strings.ToLower(allowed) {
			return nil
		}
	}
	return fmt.Errorf("安装脚本地址主机 %q 不在白名单", host)
}

func validateScript(b []byte) error {
	if len(b) == 0 {
		return fmt.Errorf("安装脚本为空")
	}
	if bytes.IndexByte(b, 0) >= 0 {
		return fmt.Errorf("安装脚本包含 NUL 字节")
	}
	first := strings.SplitN(string(b), "\n", 2)[0]
	if !validShellShebang(first) {
		return fmt.Errorf("安装脚本缺少受支持的 sh/bash shebang")
	}
	if !bytes.Contains(b, []byte("\n")) {
		return fmt.Errorf("安装脚本不是有效文本脚本")
	}
	if !utf8.Valid(b) {
		return fmt.Errorf("安装脚本不是有效 UTF-8 文本")
	}
	lower := bytes.ToLower(bytes.TrimSpace(b))
	if bytes.HasPrefix(lower, []byte("<html")) || bytes.HasPrefix(lower, []byte("<!doctype")) {
		return fmt.Errorf("安装脚本响应是 HTML，不是 shell 文本")
	}
	var controls int
	for _, value := range b {
		if value < 0x09 || (value > 0x0d && value < 0x20) {
			controls++
		}
	}
	if controls > len(b)/100 {
		return fmt.Errorf("安装脚本包含过多非文本控制字符")
	}
	return nil
}

func validShellShebang(line string) bool {
	if !strings.HasPrefix(line, "#!") {
		return false
	}
	fields := strings.Fields(strings.TrimSpace(strings.TrimPrefix(line, "#!")))
	if len(fields) == 0 {
		return false
	}
	interpreter := filepath.Base(fields[0])
	if interpreter == "sh" || interpreter == "bash" {
		return true
	}
	return interpreter == "env" && len(fields) >= 2 && (fields[1] == "sh" || fields[1] == "bash")
}

func bashSyntax(ctx context.Context, b []byte) error {
	return bashSyntaxLogged(ctx, b, nil)
}

func bashSyntaxLogged(ctx context.Context, b []byte, output io.Writer) error {
	dir, err := os.MkdirTemp("", "proxyforge-check-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "install.sh")
	if err := os.WriteFile(path, b, 0600); err != nil {
		return err
	}
	if output != nil {
		fmt.Fprintf(output, "[ProxyForge/命令] 执行命令：bash -n %s\n", path)
	}
	cmd := exec.CommandContext(ctx, "bash", "-n", path)
	if out, err := cmd.CombinedOutput(); err != nil {
		if output != nil {
			fmt.Fprintln(output, "[ProxyForge/命令] 命令失败：bash")
		}
		return fmt.Errorf("安装脚本 bash -n 校验失败: %s: %w", strings.TrimSpace(string(out)), err)
	}
	if output != nil {
		fmt.Fprintln(output, "[ProxyForge/命令] 命令完成：bash")
	}
	return nil
}
