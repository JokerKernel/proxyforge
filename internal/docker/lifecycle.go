package docker

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"proxyforge/internal/docker/model"
)

func (m *Manager) ensureDirs() error {
	if err := m.ensureOwner(); err != nil {
		return err
	}
	for _, p := range []string{m.paths.Deploy(), filepath.Dir(m.paths.Config()), m.paths.TLS(), m.paths.Data(), m.paths.Staging()} {
		if err := privateDir(p); err != nil {
			return err
		}
	}
	return nil
}

func (m *Manager) ensureOwner() error {
	if err := m.checkOwner(); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	for _, path := range []string{m.paths.Data(), m.paths.Deploy()} {
		entries, err := os.ReadDir(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return err
		}
		if len(entries) > 0 {
			return fmt.Errorf("目录含未受管文件，拒绝接管: %s", path)
		}
	}
	return saveJSON(m.paths.Owner(), ownership{SchemaVersion: 1, ManagedBy: "proxyforge", InstanceID: Instance})
}

type ownership struct {
	SchemaVersion int    `json:"schema_version"`
	ManagedBy     string `json:"managed_by"`
	InstanceID    string `json:"instance_id"`
}

func (m *Manager) checkOwner() error {
	var o ownership
	if err := loadJSON(m.paths.Owner(), &o); err != nil {
		return err
	}
	if o.SchemaVersion != 1 || o.ManagedBy != "proxyforge" || o.InstanceID != Instance {
		return fmt.Errorf("Docker 目录归属记录无效")
	}
	return nil
}
func (m *Manager) checkProject(ctx context.Context) error {
	b, err := m.docker(ctx, "container", "ls", "--all", "--filter", "label=com.docker.compose.project="+Project, "--format", "{{.Names}}")
	if err != nil {
		return err
	}
	for _, name := range stringsFields(b) {
		if name != Container {
			return fmt.Errorf("Compose 项目含额外容器 %s，拒绝操作", name)
		}
	}
	return nil
}
func stringsFields(b []byte) []string { return strings.Fields(string(b)) }
func (m *Manager) Prepare(ctx context.Context, version string) error {
	return m.withLock(ctx, func() error {
		if err := m.CheckReady(ctx); err != nil {
			return err
		}
		if err := m.recoverLocked(ctx); err != nil {
			return err
		}
		if err := m.checkProject(ctx); err != nil {
			return err
		}
		in, err := m.inspect(ctx)
		if err != nil {
			return err
		}
		if in != nil {
			return fmt.Errorf("实例已存在，请使用升级入口")
		}
		_, err = m.metadata()
		if err == nil {
			return fmt.Errorf("镜像已准备，请使用部署或升级入口")
		}
		if !os.IsNotExist(err) {
			return err
		}
		for _, p := range m.managedFiles() {
			if _, err = os.Lstat(p); err == nil {
				return fmt.Errorf("存在未受管文件 %s，拒绝覆盖", p)
			} else if !os.IsNotExist(err) {
				return err
			}
		}
		if err = m.ensureDirs(); err != nil {
			return err
		}
		md, err := m.prepareImage(ctx, version)
		if err != nil {
			return err
		}
		if err = m.probeCapabilities(ctx, md); err != nil {
			return err
		}
		compose, err := renderCompose(md.ImageRef)
		if err != nil {
			return err
		}
		md.ComposeSHA256 = digest(compose)
		j, err := m.begin("prepare", nil)
		if err != nil {
			return err
		}
		fail := func(e error) error { return m.rollback(j, e) }
		if err = atomicWrite(m.paths.Compose(), compose, 0600); err != nil {
			return fail(err)
		}
		if _, err = m.compose(ctx, "config", "--quiet"); err != nil {
			return fail(fmt.Errorf("Compose 定义校验失败: %w", err))
		}
		if err = saveJSON(m.paths.Metadata(), md); err != nil {
			return fail(err)
		}
		if err = m.savePreferences(md); err != nil {
			return fail(err)
		}
		if err = m.commit(j); err != nil {
			return fail(err)
		}
		m.progress("镜像 %s 已准备；生成节点配置后才能启动", md.RequestedVersion)
		return nil
	})
}
func (m *Manager) savePreferences(md Metadata) error {
	return saveJSON(filepath.Join(m.paths.Data(), "preferences.json"), struct {
		Channel string `json:"channel"`
		Version string `json:"version"`
	}{md.Channel, md.RequestedVersion})
}
func (m *Manager) probeCapabilities(ctx context.Context, md Metadata) error {
	keys, err := m.keyPair(ctx, md.ImageID)
	if err != nil {
		return err
	}
	uuid, err := newUUID()
	if err != nil {
		return err
	}
	short, err := randomHex(8)
	if err != nil {
		return err
	}
	n := model.Node{Core: "xray", ManagedBy: "proxyforge", Security: "reality", InboundTag: "docker-one", Server: "example.com", Port: 443, SNI: "example.com", Target: "example.com:443", UUID: uuid, UserName: "one", PrivateKey: keys.Private, PublicKey: keys.Public, ShortID: short, XrayFallbackGuard: true, XrayFallbackPort: 62000}
	server, err := m.codec.RenderServer(n)
	if err != nil {
		return err
	}
	if err = m.validateBytes(ctx, md.ImageID, server); err != nil {
		return fmt.Errorf("镜像不支持受管服务端配置: %w", err)
	}
	client, err := m.codec.RenderClient(n)
	if err != nil {
		return err
	}
	return m.validateBytes(ctx, md.ImageID, client)
}
func (m *Manager) Upgrade(ctx context.Context, version string) error {
	return m.withLock(ctx, func() error {
		if err := m.CheckReady(ctx); err != nil {
			return err
		}
		if err := m.recoverLocked(ctx); err != nil {
			return err
		}
		old, err := m.metadata()
		if err != nil {
			return err
		}
		if err = m.checkCompose(old); err != nil {
			return err
		}
		if err = m.checkProject(ctx); err != nil {
			return err
		}
		in, err := m.inspect(ctx)
		if err != nil {
			return err
		}
		if in != nil && in.Image != old.ImageID {
			return fmt.Errorf("实际容器镜像与记录不一致")
		}
		if _, err = m.docker(ctx, "image", "inspect", old.ImageID); err != nil {
			return fmt.Errorf("回滚镜像不在本机，拒绝升级: %w", err)
		}
		md, err := m.prepareImage(ctx, version)
		if err != nil {
			return err
		}
		if err = m.probeCapabilities(ctx, md); err != nil {
			return err
		}
		var state State
		hasNode := false
		if s, b, e := m.managedConfig(); e == nil {
			state = s
			hasNode = true
			if err = m.validateBytes(ctx, md.ImageID, b); err != nil {
				return err
			}
		} else if !os.IsNotExist(e) {
			return e
		}
		compose, err := renderCompose(md.ImageRef)
		if err != nil {
			return err
		}
		md.ComposeSHA256 = digest(compose)
		md.DesiredState = "stopped"
		if in != nil && in.State.Running {
			md.DesiredState = "running"
		}
		j, err := m.begin("upgrade", in)
		if err != nil {
			return err
		}
		fail := func(e error) error { return m.rollback(j, e) }
		if err = atomicWrite(m.paths.Compose(), compose, 0600); err != nil {
			return fail(err)
		}
		if _, err = m.compose(ctx, "config", "--quiet"); err != nil {
			return fail(err)
		}
		if err = m.phase(&j, "files-committed"); err != nil {
			return fail(err)
		}
		if in != nil {
			args := []string{"up", "--no-deps", "--pull", "never", "--force-recreate"}
			if in.State.Running {
				args = append(args, "-d")
			} else {
				args = append(args, "--no-start")
			}
			args = append(args, Service)
			if _, err = m.compose(ctx, args...); err != nil {
				return fail(err)
			}
			if in.State.Running {
				if !hasNode {
					return fail(fmt.Errorf("运行实例缺少节点状态"))
				}
				if err = m.healthCheck(ctx, nodePorts(state.Node), md.ImageID); err != nil {
					return fail(err)
				}
			} else {
				current, e := m.inspect(ctx)
				if e != nil || current == nil || current.State.Running || current.Image != md.ImageID {
					return fail(fmt.Errorf("停止实例升级后状态或镜像异常: %v", e))
				}
			}
		}
		if hasNode {
			state.Node.CoreVersion = md.Version
			state.Node.UpdatedAt = m.now().UTC()
			if err = m.saveNode(state.Node); err != nil {
				return fail(err)
			}
		}
		if err = saveJSON(m.paths.Metadata(), md); err != nil {
			return fail(err)
		}
		if err = m.savePreferences(md); err != nil {
			return fail(err)
		}
		if err = m.commit(j); err != nil {
			return fail(err)
		}
		m.progress("镜像升级完成：%s → %s", old.RequestedVersion, md.RequestedVersion)
		return nil
	})
}
func (m *Manager) Remove(ctx context.Context) error {
	return m.withLock(ctx, func() error {
		if err := m.CheckReady(ctx); err != nil {
			return err
		}
		if err := m.recoverLocked(ctx); err != nil {
			return err
		}
		if err := m.checkProject(ctx); err != nil {
			return err
		}
		in, err := m.inspect(ctx)
		if err != nil {
			return err
		}
		if in == nil {
			m.progress("当前没有容器，配置和数据仍保留")
			return nil
		}
		md, err := m.metadata()
		if err != nil {
			return err
		}
		if err = m.checkCompose(md); err != nil {
			return err
		}
		m.progress("正在停止并移除容器 %s", Container)
		if _, err = m.compose(ctx, "down"); err != nil {
			return err
		}
		in, err = m.inspect(ctx)
		if err != nil {
			return err
		}
		if in != nil {
			return fmt.Errorf("移除后仍存在容器，数据已保留")
		}
		m.progress("正在记录已移除状态，配置和数据保留")
		md.DesiredState = "stopped"
		md.UpdatedAt = m.now().UTC()
		if err = saveJSON(m.paths.Metadata(), md); err != nil {
			return err
		}
		m.progress("容器已移除，配置、凭据和备份已保留")
		return nil
	})
}
func (m *Manager) CheckCleanup(ctx context.Context) error {
	if err := m.rootCheck(); err != nil {
		return err
	}
	if _, err := m.lookPath("docker"); err != nil {
		if !m.hasData() {
			return nil
		}
		return fmt.Errorf("存在 Docker 数据但 Docker 不可用，无法确认容器已移除")
	}
	if err := m.CheckReady(ctx); err != nil {
		return err
	}
	if err := m.checkProject(ctx); err != nil {
		return err
	}
	in, err := m.inspect(ctx)
	if err != nil {
		return err
	}
	if in != nil {
		return fmt.Errorf("Docker Xray 容器仍存在，请先移除容器")
	}
	b, err := m.docker(ctx, "container", "ls", "--all", "--filter", "label=io.proxyforge.instance="+Instance, "--format", "{{.ID}}")
	if err != nil {
		return err
	}
	if len(stringsFields(b)) != 0 {
		return fmt.Errorf("仍存在 Docker Xray 临时/受管容器，请先恢复或移除")
	}
	if _, err = os.Stat(m.paths.Journal()); err == nil {
		return fmt.Errorf("存在未完成事务，请先恢复")
	} else if !os.IsNotExist(err) {
		return err
	}
	for _, path := range []string{m.paths.Deploy(), m.paths.Data()} {
		if err := safePath(path); err != nil {
			return err
		}
	}
	if m.hasData() {
		if err := m.checkOwner(); err != nil {
			return fmt.Errorf("缺少有效归属记录，拒绝清理: %w", err)
		}
		if md, err := m.metadata(); err == nil {
			if err = m.checkCompose(md); err != nil {
				return err
			}
		} else if !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}
func (m *Manager) Cleanup(ctx context.Context) error {
	lease, err := m.BeginCleanup(ctx)
	if err != nil {
		return err
	}
	defer lease.Close()
	return lease.Cleanup()
}

// CleanupLease holds the Docker lock across entry-layer coordination. The
// module never calls back into native business code.
type CleanupLease struct {
	manager *Manager
	ctx     context.Context
	release func()
}

func (m *Manager) BeginCleanup(ctx context.Context) (*CleanupLease, error) {
	release, err := m.acquireLock(ctx)
	if err != nil {
		return nil, err
	}
	if err = m.CheckCleanup(ctx); err != nil {
		release()
		return nil, err
	}
	return &CleanupLease{manager: m, ctx: ctx, release: release}, nil
}
func (l *CleanupLease) Close() {
	if l.release != nil {
		l.release()
		l.release = nil
	}
}
func (l *CleanupLease) Cleanup() error {
	if l.release == nil {
		return fmt.Errorf("清理锁已释放")
	}
	return l.manager.cleanupLocked(l.ctx)
}
func (m *Manager) cleanupLocked(ctx context.Context) error {
	m.progress("正在确认容器已移除，且没有未完成事务")
	if err := m.CheckCleanup(ctx); err != nil {
		return err
	}
	for _, path := range []string{m.paths.Deploy(), m.paths.Data()} {
		if err := safePath(path); err != nil {
			return err
		}
	}
	if m.hasData() {
		if err := m.checkOwner(); err != nil {
			return fmt.Errorf("缺少有效归属记录，拒绝清理: %w", err)
		}
		if md, err := m.metadata(); err == nil {
			if err = m.checkCompose(md); err != nil {
				return err
			}
		} else if !os.IsNotExist(err) {
			return err
		}
	}
	m.progress("正在永久删除配置、凭据、证书和备份")
	var errs []error
	for _, path := range []string{m.paths.Deploy(), m.paths.Data()} {
		m.progress("正在删除 %s", path)
		if err := os.RemoveAll(path); err != nil {
			errs = append(errs, err)
		}
	}
	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	for _, path := range []string{filepath.Dir(m.paths.Data()), m.paths.resolve("/var/lib/proxyforge")} {
		err := os.Remove(path)
		if err != nil && !os.IsNotExist(err) && !errors.Is(err, syscall.ENOTEMPTY) {
			return err
		}
	}
	m.progress("Docker Xray 残留已清理，其他容器和镜像未改动")
	return nil
}

// Bounded operations deliberately preserve user stop state.
func (m *Manager) Service(ctx context.Context, action string) ([]byte, error) {
	if action == "logs" {
		if err := m.CheckReady(ctx); err != nil {
			return nil, err
		}
		md, err := m.metadata()
		if err != nil {
			return nil, err
		}
		if err = m.checkCompose(md); err != nil {
			return nil, err
		}
		if _, err = m.inspect(ctx); err != nil {
			return nil, err
		}
		return m.compose(ctx, "logs", "--no-color", "--tail", "100", Service)
	}
	var output []byte
	err := m.withLock(ctx, func() error {
		if err := m.CheckReady(ctx); err != nil {
			return err
		}
		if err := m.recoverLocked(ctx); err != nil {
			return err
		}
		md, err := m.metadata()
		if err != nil {
			return err
		}
		if err = m.checkCompose(md); err != nil {
			return err
		}
		if err = m.checkProject(ctx); err != nil {
			return err
		}
		in, err := m.inspect(ctx)
		if err != nil {
			return err
		}
		if action != "start" && action != "stop" && action != "restart" {
			return fmt.Errorf("服务操作无效")
		}
		state, b, err := m.managedConfig()
		if err != nil {
			return err
		}
		if action != "stop" {
			if err = m.validateBytes(ctx, md.ImageID, b); err != nil {
				return err
			}
		}
		j, err := m.begin("service", in)
		if err != nil {
			return err
		}
		fail := func(e error) error { return m.rollback(j, e) }
		switch action {
		case "start":
			output, err = m.compose(ctx, "up", "-d", "--no-deps", "--pull", "never", Service)
		case "stop":
			if in != nil {
				output, err = m.compose(ctx, "stop", Service)
			}
		case "restart":
			if in == nil {
				return fail(fmt.Errorf("容器已移除，请先启动"))
			}
			output, err = m.compose(ctx, "restart", Service)
		}
		if err != nil {
			return fail(err)
		}
		if action != "stop" {
			if err = m.healthCheck(ctx, nodePorts(state.Node), md.ImageID); err != nil {
				return fail(err)
			}
			md.DesiredState = "running"
		} else {
			current, e := m.inspect(ctx)
			if e != nil || current != nil && current.State.Running {
				return fail(fmt.Errorf("停止容器后状态异常: %v", e))
			}
			md.DesiredState = "stopped"
		}
		md.UpdatedAt = m.now().UTC()
		if err = saveJSON(m.paths.Metadata(), md); err != nil {
			return fail(err)
		}
		if err = m.commit(j); err != nil {
			return fail(err)
		}
		return nil
	})
	return output, err
}
func (m *Manager) FollowLogs(ctx context.Context) error {
	if err := m.CheckReady(ctx); err != nil {
		return err
	}
	md, err := m.metadata()
	if err != nil {
		return err
	}
	if err = m.checkCompose(md); err != nil {
		return err
	}
	if _, err = m.inspect(ctx); err != nil {
		return err
	}
	return m.dockerStream(ctx, m.out, m.errOut, "compose", "--project-name", Project, "--project-directory", m.paths.Deploy(), "--file", m.paths.Compose(), "logs", "--no-color", "--tail", "100", "--follow", Service)
}
