// Package docker owns the complete Xray Docker feature. It deliberately has no
// dependencies on ProxyForge's native app, system, provider or domain packages.
package docker

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"proxyforge/internal/docker/xray"
)

const Instance = "xray-docker"
const Project = "proxyforge-xray"
const Service = "xray"
const Container = "proxyforge-xray"
const Repository = "ghcr.io/xtls/xray-core"
const endpoint = "unix:///var/run/docker.sock"

type Options struct {
	Root        string
	Runner      Runner
	Out, ErrOut io.Writer
	Now         func() time.Time
	// Injectable infrastructure for isolated tests. Production uses local checks.
	RootCheck   func() error
	LookPath    func(string) (string, error)
	TargetCheck func(context.Context, string, string, string) error
	HealthCheck func(context.Context, []int, string) error
	PortFree    func(int) error
	HTTPClient  *http.Client
}

type Manager struct {
	paths       Paths
	runner      Runner
	out, errOut io.Writer
	now         func() time.Time
	rootCheck   func() error
	lookPath    func(string) (string, error)
	targetCheck func(context.Context, string, string, string) error
	healthCheck func(context.Context, []int, string) error
	portFree    func(int) error
	http        *http.Client
	codec       *xray.Codec
	stateWrite  func(string, []byte, os.FileMode) error
	physicalIPs func() ([]publicAddress, error)
	externalIP  func(context.Context) (string, error)
	probeSNI    func(context.Context, []string, string, int) ([]sniCandidate, error)
}

func New(opts Options) *Manager {
	if opts.Out == nil {
		opts.Out = io.Discard
	}
	if opts.ErrOut == nil {
		opts.ErrOut = opts.Out
	}
	if opts.Runner == nil {
		opts.Runner = ExecRunner{}
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.RootCheck == nil {
		opts.RootCheck = requireRoot
	}
	if opts.LookPath == nil {
		opts.LookPath = exec.LookPath
	}
	if opts.TargetCheck == nil {
		opts.TargetCheck = validateTarget
	}
	if opts.HTTPClient == nil {
		opts.HTTPClient = &http.Client{Timeout: 30 * time.Second}
	}
	m := &Manager{paths: Paths{Root: opts.Root}, runner: opts.Runner, out: opts.Out, errOut: opts.ErrOut, now: opts.Now, rootCheck: opts.RootCheck, lookPath: opts.LookPath, targetCheck: opts.TargetCheck, healthCheck: opts.HealthCheck, portFree: opts.PortFree, http: opts.HTTPClient, codec: xray.New()}
	if m.portFree == nil {
		m.portFree = checkPortFree
	}
	m.stateWrite = atomicWrite
	if m.healthCheck == nil {
		m.healthCheck = m.waitHealthy
	}
	return m
}

func requireRoot() error {
	if os.Geteuid() != 0 {
		return fmt.Errorf("Docker 管理必须以 root 运行")
	}
	return nil
}
func (m *Manager) progress(format string, args ...any) {
	fmt.Fprintf(m.errOut, "[Docker] "+format+"\n", args...)
}
func (m *Manager) docker(ctx context.Context, args ...string) ([]byte, error) {
	full := append([]string{"--host", endpoint}, args...)
	m.noteDockerCall(full)
	return m.runner.Run(ctx, "docker", full...)
}
func (m *Manager) dockerStream(ctx context.Context, out, errOut io.Writer, args ...string) error {
	full := append([]string{"--host", endpoint}, args...)
	m.noteDockerCall(full)
	return m.runner.RunStreaming(ctx, out, errOut, "docker", full...)
}
func (m *Manager) noteDockerCall(args []string) {
	m.progress("调用：docker %s", formatCommandArgs(args))
}
func formatCommandArgs(args []string) string {
	quoted := make([]string, len(args))
	for i, arg := range args {
		quoted[i] = quoteCommandArg(arg)
	}
	return strings.Join(quoted, " ")
}
func quoteCommandArg(arg string) string {
	if arg == "" || strings.ContainsAny(arg, " \t\n\"'\\$`") {
		return strconv.Quote(arg)
	}
	return arg
}
func (m *Manager) compose(ctx context.Context, args ...string) ([]byte, error) {
	prefix := []string{"compose", "--project-name", Project, "--project-directory", m.paths.Deploy(), "--file", m.paths.Compose()}
	return m.docker(ctx, append(prefix, args...)...)
}

type Paths struct{ Root string }

func (p Paths) resolve(path string) string {
	if p.Root == "" || p.Root == "/" {
		return path
	}
	return filepath.Join(p.Root, path[1:])
}
func (p Paths) Deploy() string   { return p.resolve("/opt/proxyforge/xray-docker") }
func (p Paths) Data() string     { return p.resolve("/var/lib/proxyforge/docker/xray") }
func (p Paths) Compose() string  { return filepath.Join(p.Deploy(), "compose.yaml") }
func (p Paths) Config() string   { return filepath.Join(p.Deploy(), "config/config.json") }
func (p Paths) TLS() string      { return filepath.Join(p.Deploy(), "tls") }
func (p Paths) State() string    { return filepath.Join(p.Data(), "state.json") }
func (p Paths) Metadata() string { return filepath.Join(p.Data(), "deployment.json") }
func (p Paths) Owner() string    { return filepath.Join(p.Data(), "owner.json") }
func (p Paths) Journal() string  { return filepath.Join(p.Data(), "transaction.json") }
func (p Paths) Backups() string  { return filepath.Join(p.Data(), "backups") }
func (p Paths) Staging() string  { return filepath.Join(p.Data(), "staging") }
func (p Paths) Lock() string     { return p.resolve("/run/proxyforge/xray-docker.lock") }

func checkPortFree(port int) error {
	if port < 1 || port > 65535 {
		return fmt.Errorf("端口无效: %d", port)
	}
	var listeners []net.Listener
	defer func() {
		for _, ln := range listeners {
			_ = ln.Close()
		}
	}()
	for _, binding := range []struct{ network, address string }{{"tcp4", fmt.Sprintf("0.0.0.0:%d", port)}, {"tcp6", fmt.Sprintf("[::]:%d", port)}} {
		ln, err := net.Listen(binding.network, binding.address)
		if err != nil {
			return fmt.Errorf("端口 %d 已占用或不可用: %w", port, err)
		}
		listeners = append(listeners, ln)
	}
	return nil
}
