package docker

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"
)

type inspection struct {
	ID           string
	Image        string
	Name         string
	RestartCount int
	State        struct {
		Status     string
		Running    bool
		Paused     bool
		Restarting bool
		ExitCode   int
		Error      string
		StartedAt  string
	}
	Config struct {
		Labels     map[string]string
		Cmd        []string
		Entrypoint []string
		User       string
	}
	HostConfig struct {
		NetworkMode    string
		ReadonlyRootfs bool
	}
	Mounts []struct {
		Type        string
		Source      string
		Destination string
		RW          bool
	}
}
type Status struct {
	Label        string `json:"label"`
	Detail       string `json:"detail,omitempty"`
	Exists       bool   `json:"exists"`
	Running      bool   `json:"running"`
	ContainerID  string `json:"container_id,omitempty"`
	ImageID      string `json:"image_id,omitempty"`
	Version      string `json:"version,omitempty"`
	ExitCode     int    `json:"exit_code"`
	RestartCount int    `json:"restart_count"`
}

func (m *Manager) inspect(ctx context.Context) (*inspection, error) {
	b, err := m.docker(ctx, "container", "ls", "--all", "--no-trunc", "--filter", "name=^/"+Container+"$", "--format", "{{.ID}}")
	if err != nil {
		return nil, fmt.Errorf("检查 Docker 容器失败: %w", err)
	}
	if strings.TrimSpace(string(b)) == "" {
		return nil, nil
	}
	if len(strings.Fields(string(b))) != 1 {
		return nil, fmt.Errorf("容器名称对应多个实例")
	}
	b, err = m.docker(ctx, "container", "inspect", Container)
	if err != nil {
		return nil, fmt.Errorf("读取容器详情失败: %w", err)
	}
	var values []inspection
	if err = json.Unmarshal(b, &values); err != nil || len(values) != 1 {
		return nil, fmt.Errorf("容器详情格式无效")
	}
	in := values[0]
	if err = m.checkOwned(&in); err != nil {
		return nil, err
	}
	return &in, nil
}
func (m *Manager) checkOwned(in *inspection) error {
	labels := in.Config.Labels
	if labels["io.proxyforge.managed"] != "true" || labels["io.proxyforge.instance"] != Instance || labels["io.proxyforge.core"] != "xray" || labels["com.docker.compose.project"] != Project || labels["com.docker.compose.service"] != Service || labels["com.docker.compose.oneoff"] == "True" {
		return fmt.Errorf("同名容器不属于 ProxyForge Docker Xray，拒绝操作")
	}
	if in.HostConfig.NetworkMode != "host" || !in.HostConfig.ReadonlyRootfs || in.Config.User != "0:0" || !reflect.DeepEqual(in.Config.Cmd, []string{"run", "-config", "/usr/local/etc/xray/config.json"}) || !reflect.DeepEqual(in.Config.Entrypoint, []string{"/usr/local/bin/xray"}) {
		return fmt.Errorf("受管容器运行定义已改变，拒绝操作")
	}
	want := map[string]string{"/usr/local/etc/xray": filepath.Join(m.paths.Deploy(), "config"), "/etc/xray/tls": m.paths.TLS()}
	found := 0
	for _, mount := range in.Mounts {
		if src, ok := want[mount.Destination]; ok {
			if mount.Type != "bind" || mount.RW || filepath.Clean(mount.Source) != src {
				return fmt.Errorf("容器挂载与受管目录不一致")
			}
			found++
		} else if mount.Type == "bind" || mount.Type == "volume" {
			return fmt.Errorf("容器存在额外持久挂载，拒绝操作")
		}
	}
	if found != len(want) {
		return fmt.Errorf("容器缺少受管配置/证书挂载")
	}
	return nil
}
func (m *Manager) Status(ctx context.Context) (Status, error) {
	previous := m.quietDockerNotes
	m.quietDockerNotes = true
	defer func() { m.quietDockerNotes = previous }()
	return m.collectStatus(ctx)
}
func (m *Manager) collectStatus(ctx context.Context) (Status, error) {
	s := Status{Label: "未部署"}
	if _, err := m.lookPath("docker"); err != nil {
		s.Label = "Docker 未安装"
		s.Detail = "请预先安装 Docker 和 Compose 插件"
		return s, err
	}
	if err := m.CheckReady(ctx); err != nil {
		s.Label = "Docker 不可用"
		s.Detail = err.Error()
		return s, err
	}
	in, err := m.inspect(ctx)
	if err != nil {
		s.Label = "异常"
		s.Detail = err.Error()
		return s, err
	}
	if _, err = os.Lstat(m.paths.Journal()); err == nil {
		if err = safePath(m.paths.Journal()); err != nil {
			s.Label = "异常"
			s.Detail = err.Error()
			return s, err
		}
		s.Label = "需恢复"
		return s, nil
	} else if !os.IsNotExist(err) {
		return s, err
	}
	md, metaErr := m.metadata()
	if metaErr != nil && !os.IsNotExist(metaErr) {
		s.Label = "异常"
		s.Detail = metaErr.Error()
		return s, metaErr
	}
	if in != nil && os.IsNotExist(metaErr) {
		err = fmt.Errorf("存在受管容器但缺少部署记录")
		s.Label = "异常"
		s.Detail = err.Error()
		return s, err
	}
	if metaErr == nil {
		if err = m.checkOwner(); err != nil {
			s.Label = "异常"
			s.Detail = err.Error()
			return s, err
		}
		s.Version = md.Version
		if err = m.checkCompose(md); err != nil {
			s.Label = "异常"
			s.Detail = err.Error()
			return s, err
		}
	}
	if metaErr == nil {
		if _, _, stateErr := m.managedConfig(); stateErr != nil && !os.IsNotExist(stateErr) {
			s.Label = "异常"
			s.Detail = stateErr.Error()
			return s, stateErr
		}
		if err = m.checkProject(ctx); err != nil {
			s.Label = "异常"
			s.Detail = err.Error()
			return s, err
		}
	}
	if in == nil {
		if metaErr == nil {
			s.Label = "待配置"
			if _, err = os.Stat(m.paths.Config()); err == nil {
				s.Label = "已移除·数据保留"
			}
		}
		return s, nil
	}
	s.Exists = true
	s.ContainerID = in.ID
	s.ImageID = in.Image
	s.ExitCode = in.State.ExitCode
	s.RestartCount = in.RestartCount
	if in.Image != md.ImageID {
		s.Label = "异常"
		s.Detail = "容器镜像与部署记录不一致"
		return s, fmt.Errorf("%s", s.Detail)
	}
	if in.State.Paused || in.State.Restarting || in.State.Error != "" {
		s.Label = "异常"
		s.Detail = in.State.Status
		return s, nil
	}
	if in.State.Running {
		s.Label = "运行中"
		s.Running = true
		return s, nil
	}
	if md.DesiredState == "running" {
		s.Label = "异常"
		s.Detail = fmt.Sprintf("预期运行，当前 %s，退出码 %d", in.State.Status, in.State.ExitCode)
	} else {
		s.Label = "已停止"
	}
	return s, nil
}
func (m *Manager) waitHealthy(ctx context.Context, ports []int, imageID string) error {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	var id, started string
	var restarts int
	var stableSince time.Time
	lastWait := time.Now()
	readyAnnounced := false
	for {
		in, err := m.inspect(ctx)
		if err != nil {
			return err
		}
		if in == nil {
			return fmt.Errorf("启动后未找到容器")
		}
		if in.Image != imageID || !in.State.Running || in.State.Paused || in.State.Restarting {
			return fmt.Errorf("容器未稳定运行: %s（退出码 %d）", in.State.Status, in.State.ExitCode)
		}
		if id == "" {
			id = in.ID
			started = in.State.StartedAt
			restarts = in.RestartCount
			stableSince = time.Now()
		} else if id != in.ID || started != in.State.StartedAt || restarts != in.RestartCount {
			return fmt.Errorf("容器启动后发生重建或自动重启")
		}
		ready := true
		for _, port := range ports {
			conn, err := (&netDialer).DialContext(ctx, "tcp", fmt.Sprintf("127.0.0.1:%d", port))
			if err != nil {
				ready = false
				break
			}
			_ = conn.Close()
		}
		if ready && !readyAnnounced {
			remaining := 5*time.Second - time.Since(stableSince)
			if remaining > 0 {
				m.progress("端口已可连接，继续观察 %d 秒，确认容器没有重启", int(remaining.Round(time.Second)/time.Second))
			}
			readyAnnounced = true
		}
		if !ready && time.Since(lastWait) >= 2*time.Second {
			m.progress("仍在等待容器监听端口")
			lastWait = time.Now()
		}
		if ready && time.Since(stableSince) >= 5*time.Second {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("容器监听/稳定运行检查超时: %w", ctx.Err())
		case <-time.After(200 * time.Millisecond):
		}
	}
}
