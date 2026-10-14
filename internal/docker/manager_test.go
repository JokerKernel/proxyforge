package docker

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"go/parser"
	"go/token"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"proxyforge/internal/docker/model"
)

type simulatedDocker struct {
	t                 *testing.T
	manager           *Manager
	calls             [][]string
	container         *inspection
	images            map[string]Metadata
	selected          Metadata
	rejectValidation  bool
	failRestart       int
	foreignProject    bool
	unavailable       bool
	healthFailures    int
	failPull          bool
	latestVersion     string
	latestImageID     string
	prereleaseVersion string
	prereleaseImageID string
	versionOutput     string
}

func (f *simulatedDocker) RunStreaming(ctx context.Context, out, errOut io.Writer, name string, args ...string) error {
	b, err := f.Run(ctx, name, args...)
	if err == nil {
		_, err = out.Write(b)
	}
	return err
}
func (f *simulatedDocker) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	f.t.Helper()
	if name != "docker" {
		f.t.Fatalf("unexpected executable %s", name)
	}
	if len(args) < 3 || args[0] != "--host" || args[1] != endpoint {
		f.t.Fatalf("missing explicit local endpoint: %v", args)
	}
	a := append([]string(nil), args[2:]...)
	f.calls = append(f.calls, a)
	if f.unavailable {
		return nil, errors.New("daemon unavailable")
	}
	switch a[0] {
	case "info":
		return []byte(`{"OSType":"linux","Architecture":"` + runtime.GOARCH + `","SecurityOptions":["name=seccomp,profile=builtin"]}`), nil
	case "image":
		if a[1] == "pull" {
			if f.failPull {
				return nil, errors.New("pull failed")
			}
			tag := a[2]
			version := strings.TrimPrefix(tag, Repository+":")
			if version == "latest" {
				version = f.latestVersion
				if version == "" {
					version = "26.4.1"
				}
			}
			if version == "pre-release" {
				version = f.prereleaseVersion
				if version == "" {
					version = "26.10.10"
				}
			}
			char := "a"
			if version != "26.3.27" {
				char = "b"
			}
			id := "sha256:" + strings.Repeat(char, 64)
			if tag == Repository+":latest" && f.latestImageID != "" {
				id = f.latestImageID
			}
			if tag == Repository+":pre-release" {
				id = "sha256:" + strings.Repeat("d", 64)
				if f.prereleaseImageID != "" {
					id = f.prereleaseImageID
				}
			}
			md := Metadata{ImageID: id, ImageRef: Repository + "@" + id, RequestedVersion: version}
			f.selected = md
			f.images[id] = md
			f.images[md.ImageRef] = md
			f.images[tag] = md
			return []byte("pulled\n"), nil
		}
		if a[1] == "inspect" {
			md, ok := f.images[a[2]]
			if !ok {
				return nil, errors.New("image missing")
			}
			b, _ := json.Marshal([]map[string]any{{"Id": md.ImageID, "RepoDigests": []string{md.ImageRef}, "Architecture": runtime.GOARCH, "Os": "linux"}})
			return b, nil
		}
	case "container":
		switch a[1] {
		case "ls":
			joined := strings.Join(a, " ")
			if strings.Contains(joined, "label=com.docker.compose.project=") {
				if f.foreignProject {
					return []byte("unrelated\n"), nil
				}
				if f.container != nil {
					return []byte(Container + "\n"), nil
				}
				return nil, nil
			}
			if f.container != nil {
				return []byte(f.container.ID + "\n"), nil
			}
			return nil, nil
		case "inspect":
			if f.container != nil && (a[2] == Container || a[2] == f.container.ID) {
				b, _ := json.Marshal([]inspection{*f.container})
				return b, nil
			}
			return nil, errors.New("no such container")
		case "stop":
			if f.container != nil {
				f.container.State.Running = false
				f.container.State.Status = "exited"
			}
			return nil, nil
		case "rm":
			f.container = nil
			return nil, nil
		}
	case "run":
		image := ""
		at := -1
		for i, v := range a {
			if validImageID(v) {
				image = v
				at = i
				break
			}
		}
		if at < 0 {
			f.t.Fatalf("missing image ID: %v", a)
		}
		md := f.images[image]
		core := a[at+1:]
		switch core[0] {
		case "version":
			if f.versionOutput != "" {
				return []byte(f.versionOutput), nil
			}
			return []byte("Xray " + md.RequestedVersion + " (test build)\n"), nil
		case "x25519":
			key := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{1}, 32))
			return []byte("PrivateKey: " + key + "\nPassword: " + key + "\nHash32: ignored\n"), nil
		case "run":
			if f.rejectValidation {
				return nil, errors.New("invalid configuration")
			}
			return nil, nil
		}
	case "compose":
		at := 0
		for i, v := range a {
			if v == "--file" {
				at = i + 2
				break
			}
		}
		if at == 0 {
			if len(a) > 1 && a[1] == "version" {
				return []byte("2.30.0\n"), nil
			}
			f.t.Fatalf("unexpected compose args %v", a)
		}
		action := a[at]
		switch action {
		case "config":
			return nil, nil
		case "up":
			b, err := os.ReadFile(f.manager.paths.Compose())
			if err != nil {
				return nil, err
			}
			ref := ""
			for _, line := range strings.Split(string(b), "\n") {
				if strings.HasPrefix(strings.TrimSpace(line), "image:") {
					ref = strings.Trim(strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "image:")), `"`)
				}
			}
			md, ok := f.images[ref]
			if !ok {
				return nil, errors.New("unknown compose image")
			}
			f.newContainer(md.ImageID)
			f.container.State.Running = !contains(a, "--no-start")
			if f.container.State.Running {
				f.container.State.Status = "running"
			} else {
				f.container.State.Status = "created"
			}
			return nil, nil
		case "restart":
			if f.failRestart > 0 {
				f.failRestart--
				return nil, errors.New("restart failed")
			}
			if f.container == nil {
				return nil, errors.New("missing container")
			}
			f.container.State.Running = true
			f.container.State.Status = "running"
			return nil, nil
		case "stop":
			if f.container != nil {
				f.container.State.Running = false
				f.container.State.Status = "exited"
			}
			return nil, nil
		case "down":
			f.container = nil
			return nil, nil
		case "logs":
			return []byte("xray | example log\n"), nil
		}
	}
	f.t.Fatalf("unhandled Docker command: %v", a)
	return nil, nil
}
func contains(a []string, value string) bool {
	for _, v := range a {
		if v == value {
			return true
		}
	}
	return false
}
func (f *simulatedDocker) newContainer(image string) {
	in := &inspection{ID: strings.Repeat("c", 64), Image: image, Name: "/" + Container}
	in.Config.User = "0:0"
	in.Config.Cmd = []string{"run", "-config", "/usr/local/etc/xray/config.json"}
	in.Config.Entrypoint = []string{"/usr/local/bin/xray"}
	in.Config.Labels = map[string]string{"io.proxyforge.managed": "true", "io.proxyforge.instance": Instance, "io.proxyforge.core": "xray", "com.docker.compose.project": Project, "com.docker.compose.service": Service}
	in.HostConfig.NetworkMode = "host"
	in.HostConfig.ReadonlyRootfs = true
	for dest, source := range map[string]string{"/usr/local/etc/xray": filepath.Dir(f.manager.paths.Config()), "/etc/xray/tls": f.manager.paths.TLS()} {
		in.Mounts = append(in.Mounts, struct {
			Type        string
			Source      string
			Destination string
			RW          bool
		}{"bind", source, dest, false})
	}
	f.container = in
}
func testManager(t *testing.T) (*Manager, *simulatedDocker) {
	t.Helper()
	f := &simulatedDocker{t: t, images: map[string]Metadata{}}
	m := New(Options{Root: t.TempDir(), Runner: f, RootCheck: func() error { return nil }, LookPath: func(string) (string, error) { return "/test/docker", nil }, TargetCheck: func(context.Context, string, string, string) error { return nil }, PortFree: func(int) error { return nil }, HealthCheck: func(_ context.Context, _ []int, image string) error {
		if f.healthFailures > 0 {
			f.healthFailures--
			return errors.New("health failed")
		}
		if f.container == nil || !f.container.State.Running || f.container.Image != image {
			return errors.New("unhealthy simulated container")
		}
		return nil
	}})
	f.manager = m
	return m, f
}
func prepared(t *testing.T) (*Manager, *simulatedDocker) {
	t.Helper()
	m, f := testManager(t)
	if err := m.Prepare(context.Background(), "26.3.27"); err != nil {
		t.Fatal(err)
	}
	return m, f
}
func deployed(t *testing.T) (*Manager, *simulatedDocker) {
	t.Helper()
	m, f := prepared(t)
	if err := m.Generate(context.Background(), model.GenerateOptions{Server: "example.com", Port: 8443, SNI: "www.example.com", Target: "www.example.com:443", StandardConfig: true}, true); err != nil {
		t.Fatal(err)
	}
	return m, f
}
func readFile(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
func TestDependencyBoundary(t *testing.T) {
	err := filepath.WalkDir(".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") {
			return nil
		}
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
		if err != nil {
			return err
		}
		for _, imp := range file.Imports {
			value, err := strconv.Unquote(imp.Path.Value)
			if err != nil {
				return err
			}
			if strings.HasPrefix(value, "proxyforge/internal/") && !strings.HasPrefix(value, "proxyforge/internal/docker/") {
				return fmt.Errorf("Docker package %s imports native package %s", path, value)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
func TestPreparePinsImageWithoutStarting(t *testing.T) {
	m, f := prepared(t)
	if f.container != nil {
		t.Fatal("prepare started a business container")
	}
	md, err := m.metadata()
	if err != nil {
		t.Fatal(err)
	}
	b := readFile(t, m.paths.Compose())
	for _, part := range []string{md.ImageRef, "network_mode: host", "read_only: true", "NET_BIND_SERVICE", "json-file", "max-size: \"10m\""} {
		if !bytes.Contains(b, []byte(part)) {
			t.Fatalf("compose missing %q", part)
		}
	}
	if strings.Contains(string(b), "latest") {
		t.Fatal("unpinned image")
	}
	if _, err = os.Stat(m.paths.Journal()); !os.IsNotExist(err) {
		t.Fatal("prepare left an active transaction")
	}
}
func TestConfigChangesPreserveStopStateAndNativeFiles(t *testing.T) {
	m, f := deployed(t)
	native := m.paths.resolve("/usr/local/etc/xray/config.json")
	if err := os.MkdirAll(filepath.Dir(native), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(native, []byte("native-marker"), 0600); err != nil {
		t.Fatal(err)
	}
	before, err := m.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = m.Service(context.Background(), "stop"); err != nil {
		t.Fatal(err)
	}
	f.calls = nil
	if err = m.PatchSetting(context.Background(), "dns", "public-cloudflare"); err != nil {
		t.Fatal(err)
	}
	if f.container.State.Running {
		t.Fatal("config change started stopped instance")
	}
	for _, call := range f.calls {
		if contains(call, "restart") || contains(call, "up") {
			t.Fatalf("stopped config invoked service mutation: %v", call)
		}
	}
	after, err := m.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	if before.Node.UUID != after.Node.UUID || before.Node.PrivateKey != after.Node.PrivateKey {
		t.Fatal("settings changed credentials")
	}
	if string(readFile(t, native)) != "native-marker" {
		t.Fatal("native config was changed")
	}
}
func TestRestartFailureRestoresConfigStateAndRunningContainer(t *testing.T) {
	m, f := deployed(t)
	oldConfig := readFile(t, m.paths.Config())
	oldState := readFile(t, m.paths.State())
	f.failRestart = 1
	err := m.PatchSetting(context.Background(), "log", "debug")
	if err == nil || !strings.Contains(err.Error(), "已恢复") {
		t.Fatalf("expected recovered failure: %v", err)
	}
	if !bytes.Equal(oldConfig, readFile(t, m.paths.Config())) || !bytes.Equal(oldState, readFile(t, m.paths.State())) {
		t.Fatal("failed change did not restore exact files")
	}
	if f.container == nil || !f.container.State.Running {
		t.Fatal("old running state was lost")
	}
	if _, err = os.Stat(m.paths.Journal()); !os.IsNotExist(err) {
		t.Fatal("successful rollback left journal")
	}
}
func TestValidationFailsBeforeLiveMutation(t *testing.T) {
	m, f := deployed(t)
	before := readFile(t, m.paths.Config())
	f.rejectValidation = true
	f.calls = nil
	err := m.PatchSetting(context.Background(), "log", "debug")
	if err == nil {
		t.Fatal("invalid candidate accepted")
	}
	if !bytes.Equal(before, readFile(t, m.paths.Config())) {
		t.Fatal("invalid candidate overwrote live config")
	}
	for _, call := range f.calls {
		if contains(call, "restart") || contains(call, "stop") {
			t.Fatal("validation stopped a live service")
		}
	}
}
func TestUpgradePreservesCredentialsAndRollsBackImage(t *testing.T) {
	m, f := deployed(t)
	oldMD, err := m.metadata()
	if err != nil {
		t.Fatal(err)
	}
	oldState := readFile(t, m.paths.State())
	f.healthFailures = 1
	err = m.Upgrade(context.Background(), "26.4.1")
	if err == nil {
		t.Fatal("failed new image accepted")
	}
	md, err := m.metadata()
	if err != nil {
		t.Fatal(err)
	}
	if md.ImageID != oldMD.ImageID || f.container.Image != oldMD.ImageID || !f.container.State.Running {
		t.Fatal("upgrade failed to restore old image and runtime")
	}
	if !bytes.Equal(oldState, readFile(t, m.paths.State())) {
		t.Fatal("failed upgrade changed credentials")
	}
	if _, err = m.Service(context.Background(), "stop"); err != nil {
		t.Fatal(err)
	}
	if err = m.Upgrade(context.Background(), "26.4.1"); err != nil {
		t.Fatal(err)
	}
	if f.container.State.Running {
		t.Fatal("upgrade started a stopped instance")
	}
	s, err := m.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	var original State
	if err = json.Unmarshal(oldState, &original); err != nil {
		t.Fatal(err)
	}
	if s.Node.UUID != original.Node.UUID || s.Node.PrivateKey != original.Node.PrivateKey {
		t.Fatal("successful upgrade changed credentials")
	}
}
func TestCleanupRejectsContainersAndPreservesExternalFiles(t *testing.T) {
	m, f := deployed(t)
	if err := m.Cleanup(context.Background()); err == nil {
		t.Fatal("cleanup removed running container data")
	}
	f.container.Config.Labels["io.proxyforge.managed"] = "false"
	if err := m.Remove(context.Background()); err == nil {
		t.Fatal("foreign container removed")
	}
	f.container.Config.Labels["io.proxyforge.managed"] = "true"
	if err := m.Remove(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(m.paths.State()); err != nil {
		t.Fatal("remove erased state")
	}
	external := m.paths.resolve("/var/lib/proxyforge/state/xray.json")
	if err := os.MkdirAll(filepath.Dir(external), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(external, []byte("native-state"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := m.Cleanup(context.Background()); err != nil {
		t.Fatal(err)
	}
	if string(readFile(t, external)) != "native-state" {
		t.Fatal("cleanup erased external state")
	}
	for _, call := range f.calls {
		if contains(call, "prune") || contains(call, "--rmi") || contains(call, "--remove-orphans") {
			t.Fatalf("cleanup exceeded ownership: %v", call)
		}
	}
}
func TestRecoverInterruptedFileCommit(t *testing.T) {
	m, f := deployed(t)
	before := readFile(t, m.paths.Config())
	j, err := m.begin("config", f.container)
	if err != nil {
		t.Fatal(err)
	}
	if err = atomicWrite(m.paths.Config(), []byte(`{"broken":true}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err = m.phase(&j, "files-committed"); err != nil {
		t.Fatal(err)
	}
	if err = m.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, readFile(t, m.paths.Config())) || !f.container.State.Running {
		t.Fatal("interrupted operation not recovered")
	}
}
func TestTLSAndLandingPathsStayOwned(t *testing.T) {
	m, f := prepared(t)
	if err := m.Generate(context.Background(), model.GenerateOptions{Security: "tls", Server: "example.com", Port: 8443}, true); err != nil {
		t.Fatal(err)
	}
	s, err := m.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	if s.Node.TLSCertificate == nil || !strings.HasPrefix(s.Node.TLSCertificate.KeyFile, m.paths.TLS()) {
		t.Fatal("state did not retain host paths")
	}
	if !bytes.Contains(readFile(t, m.paths.Config()), []byte("/etc/xray/tls/")) {
		t.Fatal("container certificate path not rendered")
	}
	f.calls = nil
	peer, err := m.AddLanding(context.Background(), "relay-one", "tls", 8444)
	if err != nil {
		t.Fatal(err)
	}
	if peer.CertificateSHA256 == "" || peer.UUID == "" || peer.Port != 8444 {
		t.Fatal("invalid peer")
	}
	if _, err = m.Client(context.Background(), "clash", "relay-one"); err != nil {
		t.Fatal(err)
	}
	if err = m.ChangeLink(context.Background(), "landing", "relay-one", "disable"); err != nil {
		t.Fatal(err)
	}
	if _, err = m.Peer("relay-one"); err == nil {
		t.Fatal("disabled peer exported")
	}
}
func TestPolicyStoredEvenWhenInactive(t *testing.T) {
	m, _ := deployed(t)
	entry, err := model.ParseAccessEntry("example.com", false)
	if err != nil {
		t.Fatal(err)
	}
	if err = m.SetPolicy(context.Background(), model.AccessPolicy{Disabled: true, Whitelist: []model.AccessEntry{entry}}); err != nil {
		t.Fatal(err)
	}
	s, err := m.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Node.AccessPolicy.Whitelist) != 1 {
		t.Fatal("inactive policy was not saved")
	}
}
func TestSymlinksAndOversizedFilesRejected(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "real")
	if err := os.WriteFile(target, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if err := atomicWrite(link, []byte("replace"), 0600); err == nil {
		t.Fatal("symlink overwritten")
	}
	if string(readFile(t, target)) != "keep" {
		t.Fatal("symlink target changed")
	}
}
func TestInstanceLockHonorsCancellation(t *testing.T) {
	m, _ := testManager(t)
	started := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- m.withLock(context.Background(), func() error { close(started); <-release; return nil })
	}()
	<-started
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if err := m.withLock(ctx, func() error { return nil }); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("lock cancellation: %v", err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
