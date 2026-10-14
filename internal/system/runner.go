package system

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"os/exec"
	"strconv"
	"strings"
	"sync"

	"proxyforge/internal/provider"
)

type ExecRunner struct {
	Stdin io.Reader
}

type LoggingRunner struct {
	Runner provider.Runner
	Out    io.Writer
	mu     sync.Mutex
}

func (r *LoggingRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	logCommand := shouldLogCommand(name, args)
	if logCommand {
		r.log(name, args)
	}
	b, err := r.Runner.Run(ctx, name, args...)
	if logCommand {
		if err != nil && ctx.Err() != nil {
			r.logResult("已停止", name, args)
		} else if err != nil {
			r.logResult("执行失败", name, args)
		} else {
			r.logResult("执行成功", name, args)
		}
	}
	return b, err
}

func (r *LoggingRunner) RunStreaming(ctx context.Context, stdout, stderr io.Writer, name string, args ...string) error {
	r.log(name, args)
	var err error
	if streaming, ok := r.Runner.(provider.StreamingRunner); ok {
		err = streaming.RunStreaming(ctx, stdout, stderr, name, args...)
	} else {
		var b []byte
		b, err = r.Runner.Run(ctx, name, args...)
		if len(b) != 0 {
			_, _ = stdout.Write(b)
		}
	}
	if err != nil && ctx.Err() != nil {
		r.logResult("已停止", name, args)
	} else if err != nil {
		r.logResult("执行失败", name, args)
	} else {
		r.logResult("执行成功", name, args)
	}
	return err
}

func (r *LoggingRunner) log(name string, args []string) {
	if r.Out == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	WriteDetail(r.Out, commandPurpose(name, args), "%s", CommandLine(name, args...))
}

// CommandLine retains complete arguments while redacting proxy credentials.
func CommandLine(name string, args ...string) string {
	parts := []string{quoteCommandArg(name)}
	for _, arg := range redactCommandArgs(args) {
		parts = append(parts, quoteCommandArg(arg))
	}
	return strings.Join(parts, " ")
}

func commandPurpose(name string, args []string) string {
	if len(args) == 0 {
		return "执行命令"
	}
	switch name {
	case "systemctl":
		actions := map[string]string{
			"start": "启动服务", "stop": "停止服务", "restart": "重启服务",
			"enable": "启用开机自启", "disable": "禁用开机自启", "status": "查看服务状态",
			"daemon-reload": "刷新服务缓存", "show": "读取服务属性",
			"is-active": "检查运行状态", "is-enabled": "检查自启状态",
		}
		if label := actions[args[0]]; label != "" {
			return label
		}
	case "xray", "sing-box":
		switch args[0] {
		case "version":
			return "读取 " + name + " 版本"
		case "x25519":
			return "生成 REALITY 密钥"
		case "generate":
			if len(args) > 1 && args[1] == "reality-keypair" {
				return "生成 REALITY 密钥"
			}
		case "check":
			return "校验 " + name + " 配置"
		case "run":
			for _, arg := range args[1:] {
				if arg == "-test" {
					return "校验 " + name + " 配置"
				}
			}
			return "运行 " + name
		}
	case "journalctl":
		return "查看服务日志"
	case "bash":
		if args[0] == "-n" {
			return "校验脚本语法"
		}
		return "执行管理脚本"
	case "dpkg", "rpm":
		return "管理软件包"
	case "systemd-sysusers":
		return "配置系统用户"
	case "getent":
		return "查询系统账号"
	case "userdel", "groupdel":
		return "删除系统账号"
	case "runuser":
		return "以指定用户执行"
	}
	return "执行命令"
}

func redactCommandArgs(args []string) []string {
	redacted := append([]string(nil), args...)
	for index, arg := range redacted {
		if arg == "--proxy" {
			if index+1 < len(redacted) {
				redacted[index+1] = proxyAddressForLog(redacted[index+1])
			}
			continue
		}
		if strings.HasPrefix(arg, "--proxy=") {
			redacted[index] = "--proxy=" + proxyAddressForLog(strings.TrimPrefix(arg, "--proxy="))
		}
	}
	return redacted
}

func shouldLogCommand(name string, args []string) bool {
	if name != "systemctl" || len(args) == 0 {
		return true
	}
	switch args[0] {
	case "show", "is-active", "is-enabled":
		return false
	default:
		return true
	}
}

func proxyAddressForLog(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return "[REDACTED]"
	}
	return (&url.URL{Scheme: parsed.Scheme, Host: parsed.Host}).String()
}

func (r *LoggingRunner) logResult(result, name string, args []string) {
	if r.Out == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	WriteDetail(r.Out, "命令结果", "%s：%s", commandPurpose(name, args), result)
}

func quoteCommandArg(value string) string {
	if value != "" && !strings.ContainsAny(value, " \t\r\n\"'\\") {
		return value
	}
	return strconv.Quote(value)
}

func (r ExecRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdin = r.Stdin
	b, err := cmd.CombinedOutput()
	if err != nil {
		message := strings.TrimSpace(string(b))
		if message != "" {
			return b, fmt.Errorf("%s: %s: %w", name, message, err)
		}
		return b, fmt.Errorf("%s: %w", name, err)
	}
	return b, nil
}

func (r ExecRunner) RunStreaming(ctx context.Context, stdout, stderr io.Writer, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdin = r.Stdin
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	return nil
}
