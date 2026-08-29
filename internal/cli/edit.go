package cli

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

var configEditors = []string{"vim", "nano", "vi"}

func (c *commandSet) editServerConfig(core string) error {
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
	if err := c.runConfigEditor(editor, path); err != nil {
		return fmt.Errorf("编辑器退出异常: %w", err)
	}
	fmt.Fprintln(c.out, "已关闭编辑器。如已修改配置，请到“服务端配置 → 服务管理”中重启服务使配置生效。")
	return nil
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
