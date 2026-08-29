package cli

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

var configEditors = []string{"vim", "nano", "vi"}

func (c *commandSet) editServerConfig(ctx context.Context, core string) error {
	if c.runEditor == nil && !c.interactiveUI() {
		return fmt.Errorf("编辑配置需要交互式终端")
	}
	if c.app == nil {
		return fmt.Errorf("应用未初始化")
	}
	path, err := c.app.ServerConfigPath(core)
	if err != nil {
		return err
	}
	editor, err := c.findConfigEditor()
	if err != nil {
		return err
	}
	c.printPageHeader(core, "编辑服务端配置")
	fmt.Fprintf(c.out, "使用 %s 打开 %s\n", filepath.Base(editor), path)
	fmt.Fprintln(c.out, "警告：配置可能包含 UUID、REALITY 私钥等敏感信息。")
	for {
		if err := c.runConfigEditor(editor, path); err != nil {
			return fmt.Errorf("编辑器退出异常: %w", err)
		}
		fmt.Fprintln(c.out, "已关闭编辑器，正在校验配置……")
		if err := c.app.ValidateServerConfig(ctx, core); err != nil {
			fmt.Fprintf(c.out, "[错误] 配置校验失败：%v\n", err)
			fmt.Fprint(c.out, "\n按 Enter 重新打开配置文件……")
			if c.reader == nil {
				return fmt.Errorf("等待重新编辑配置: 输入读取器未初始化")
			}
			if _, readErr := c.reader.ReadString('\n'); readErr != nil {
				return fmt.Errorf("等待重新编辑配置: %w", readErr)
			}
			fmt.Fprintf(c.out, "重新使用 %s 打开 %s\n", filepath.Base(editor), path)
			continue
		}
		fmt.Fprintln(c.out, "配置校验通过，正在重启服务应用配置……")
		output, err := c.app.Service(ctx, core, "restart")
		if len(output) > 0 {
			fmt.Fprint(c.out, string(output))
			if output[len(output)-1] != '\n' {
				fmt.Fprintln(c.out)
			}
		}
		if err != nil {
			return fmt.Errorf("配置校验通过，但重启 %s 服务失败: %w", core, err)
		}
		fmt.Fprintf(c.out, "%s 配置已通过校验，服务已重启并应用新配置。\n", core)
		return nil
	}
}

func (c *commandSet) findConfigEditor() (string, error) {
	var last error
	for _, name := range configEditors {
		path, err := c.lookEditor(name)
		if err == nil && path != "" {
			return path, nil
		}
		last = err
	}
	if last == nil {
		last = exec.ErrNotFound
	}
	return "", fmt.Errorf("未找到可用编辑器（已尝试 vim、nano、vi）: %w", last)
}

func (c *commandSet) lookEditor(name string) (string, error) {
	if c.lookPath != nil {
		return c.lookPath(name)
	}
	if c.app != nil && c.app.LookPath != nil {
		return c.app.LookPath(name)
	}
	return exec.LookPath(name)
}

func (c *commandSet) runConfigEditor(editor, path string) error {
	if c.runEditor != nil {
		return c.runEditor(editor, path)
	}
	cmd := exec.Command(editor, path)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}
