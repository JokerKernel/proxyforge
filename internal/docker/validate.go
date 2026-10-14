package docker

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"
)

func (m *Manager) coreRun(ctx context.Context, imageID string, mounts []string, command ...string) ([]byte, error) {
	if !validImageID(imageID) {
		return nil, fmt.Errorf("无效的本机镜像 ID")
	}
	suffix, err := randomHex(12)
	if err != nil {
		return nil, err
	}
	name := Container + "-check-" + suffix
	args := []string{"run", "--rm", "--pull", "never", "--name", name, "--label", "io.proxyforge.managed=true", "--label", "io.proxyforge.instance=" + Instance, "--label", "io.proxyforge.role=validator", "--network", "host", "--user", "0:0", "--read-only", "--cap-drop", "ALL", "--cap-add", "NET_BIND_SERVICE", "--security-opt", "no-new-privileges:true", "--tmpfs", "/usr/local/etc/xray:rw,noexec,nosuid,nodev,size=1m,mode=0700", "--tmpfs", "/tmp:rw,noexec,nosuid,nodev,size=16m,mode=1777", "--tmpfs", "/var/log/xray:rw,noexec,nosuid,nodev,size=16m,mode=0700"}
	args = append(args, mounts...)
	args = append(args, imageID)
	args = append(args, command...)
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		b, err := m.docker(cleanupCtx, "container", "inspect", name)
		if err != nil {
			return
		}
		var ins []inspection
		if json.Unmarshal(b, &ins) == nil && len(ins) == 1 && ins[0].Config.Labels["io.proxyforge.instance"] == Instance && ins[0].Config.Labels["io.proxyforge.role"] == "validator" {
			_, _ = m.docker(cleanupCtx, "container", "rm", "--force", "--volumes", ins[0].ID)
		}
	}()
	return m.docker(ctx, args...)
}
func (m *Manager) validateBytes(ctx context.Context, imageID string, data []byte) error {
	if len(data) == 0 || len(data) > maxConfigSize || !json.Valid(data) {
		return fmt.Errorf("配置必须是大小合理的有效 JSON")
	}
	if err := privateDir(m.paths.Staging()); err != nil {
		return err
	}
	if err := privateDir(m.paths.TLS()); err != nil {
		return err
	}
	f, err := os.CreateTemp(m.paths.Staging(), "candidate-*.json")
	if err != nil {
		return err
	}
	path := f.Name()
	defer os.Remove(path)
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(data)
	}
	if e := f.Close(); err == nil {
		err = e
	}
	if err != nil {
		return err
	}
	for _, path := range []string{path, m.paths.TLS()} {
		if strings.ContainsAny(path, ",\n\r") {
			return fmt.Errorf("挂载路径含不支持的字符")
		}
		if err = safePath(path); err != nil {
			return err
		}
	}
	m.progress("正在用本机镜像启动临时容器，执行 xray run -test 校验配置")
	_, err = m.coreRun(ctx, imageID, []string{"--mount", "type=bind,src=" + path + ",dst=/run/proxyforge/config.json,readonly", "--mount", "type=bind,src=" + m.paths.TLS() + ",dst=/etc/xray/tls,readonly"}, "run", "-test", "-config", "/run/proxyforge/config.json")
	if err != nil {
		return fmt.Errorf("Docker Xray 配置校验失败: %w", err)
	}
	m.progress("配置校验通过")
	return nil
}
