package docker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"proxyforge/internal/docker/model"
)

func configRoot(t *testing.T, b []byte) map[string]any {
	t.Helper()
	var r map[string]any
	if err := json.Unmarshal(b, &r); err != nil {
		t.Fatal(err)
	}
	return r
}
func rootBytes(t *testing.T, r map[string]any) []byte {
	t.Helper()
	b, err := jsonBytes(r)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
func TestRemoveCleanupAndRecoverPrintEachStep(t *testing.T) {
	m, f := deployed(t)
	var errOut bytes.Buffer
	m.errOut = &errOut
	if err := m.Remove(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"正在停止并移除容器 " + Container,
		"正在记录已移除状态，配置和数据保留",
		"容器已移除，配置、凭据和备份已保留",
	} {
		if !strings.Contains(errOut.String(), want) {
			t.Fatalf("remove missing %q:\n%s", want, errOut.String())
		}
	}
	errOut.Reset()
	if err := m.Cleanup(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"正在确认容器已移除，且没有未完成事务",
		"正在永久删除配置、凭据、证书和备份",
		"正在删除 " + m.paths.Deploy(),
		"正在删除 " + m.paths.Data(),
		"Docker Xray 残留已清理，其他容器和镜像未改动",
	} {
		if !strings.Contains(errOut.String(), want) {
			t.Fatalf("cleanup missing %q:\n%s", want, errOut.String())
		}
	}

	m, f = deployed(t)
	errOut.Reset()
	m.errOut = &errOut
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
	if !bytes.Equal(before, readFile(t, m.paths.Config())) {
		t.Fatal("recovery lost old config")
	}
	for _, want := range []string{
		"发现未完成的配置事务，正在继续恢复",
		"正在读取事务快照",
		"正在停止当前容器",
		"正在恢复配置、状态和部署记录",
		"正在按中断前的运行状态启动容器",
		"正在检查恢复后的容器是否稳定监听",
		"容器已稳定监听",
		"正在清除恢复记录",
		"事务恢复完成",
	} {
		if !strings.Contains(errOut.String(), want) {
			t.Fatalf("recover missing %q:\n%s", want, errOut.String())
		}
	}

	errOut.Reset()
	if err = m.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(errOut.String(), "没有未完成事务") {
		t.Fatalf("idle recover missing notice:\n%s", errOut.String())
	}
}

func TestApplyConfigPrintsEachStep(t *testing.T) {
	m, _ := deployed(t)
	var errOut bytes.Buffer
	m.errOut = &errOut
	s, err := m.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	entry, err := model.ParseAccessEntry("123.123.123.123", false)
	if err != nil {
		t.Fatal(err)
	}
	policy := s.Node.AccessPolicy
	policy.Blacklist = append(policy.Blacklist, entry)
	if err = m.SetPolicy(context.Background(), policy); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"正在用本机镜像启动临时容器，执行 xray run -test 校验配置",
		"配置校验通过",
		"正在写入服务端配置",
		"正在重启容器，使新配置生效",
		"正在检查容器是否稳定监听端口",
		"容器已稳定监听",
		"正在保存节点状态",
		"配置已生效",
	} {
		if !strings.Contains(errOut.String(), want) {
			t.Fatalf("missing %q:\n%s", want, errOut.String())
		}
	}
}

func TestIndependentPolicyKeepsRelayAndFallbackPriority(t *testing.T) {
	m, _ := prepared(t)
	ctx := context.Background()
	opts := model.GenerateOptions{Server: "example.com", Port: 8443, SNI: "www.example.com", Target: "www.example.com:443", XrayFallbackPort: 62000}
	if err := m.Generate(ctx, opts, true); err != nil {
		t.Fatal(err)
	}
	s, err := m.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	peer := model.LandingPeer{Name: "upstream", Server: "upstream.example.com", Port: 443, SNI: "www.example.com", Security: "reality", UUID: s.Node.UUID, PublicKey: s.Node.PublicKey, ShortID: s.Node.ShortID}
	if err = m.AddRelay(ctx, "relay-one", peer); err != nil {
		t.Fatal(err)
	}
	white, err := model.ParseAccessEntry("10.0.0.0/8", false)
	if err != nil {
		t.Fatal(err)
	}
	black, err := model.ParseAccessEntry("blocked.example.com", false)
	if err != nil {
		t.Fatal(err)
	}
	if err = m.SetPolicy(ctx, model.AccessPolicy{Mode: "whitelist", Whitelist: []model.AccessEntry{white}, Blacklist: []model.AccessEntry{black}}); err != nil {
		t.Fatal(err)
	}
	r := configRoot(t, readFile(t, m.paths.Config()))
	route := r["routing"].(map[string]any)
	rules := route["rules"].([]any)
	whiteAt, relayAt, denyAt := -1, -1, -1
	for i, v := range rules {
		rule := v.(map[string]any)
		tag, _ := rule["ruleTag"].(string)
		switch tag {
		case "white-ip":
			whiteAt = i
		case "access-white-relay-relay-one-ip":
			relayAt = i
		case "access-final":
			denyAt = i
		}
		if tag == "black-domain" {
			t.Fatal("blacklist active in whitelist-only mode")
		}
	}
	if relayAt < 0 || whiteAt < 0 || denyAt < whiteAt || relayAt > whiteAt {
		t.Fatalf("wrong whitelist/relay/final priority: %+v", rules)
	}
	client, err := m.Client(ctx, "native", "")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(client, []byte("10.0.0.0/8")) {
		t.Fatal("client did not preserve explicit private destination exception")
	}
	if err = m.Generate(ctx, model.GenerateOptions{}, false); err != nil {
		t.Fatal(err)
	}
	policy := model.AccessPolicy{Disabled: true, Whitelist: []model.AccessEntry{white}}
	if err = m.SetPolicy(ctx, policy); err != nil {
		t.Fatal(err)
	}
	r = configRoot(t, readFile(t, m.paths.Config()))
	for _, v := range r["inbounds"].([]any) {
		in := v.(map[string]any)
		if in["tag"] == "docker-one" {
			sniff, ok := in["sniffing"].(map[string]any)
			if !ok || sniff["enabled"] != true {
				t.Fatal("regeneration lost original sniffing settings")
			}
		}
	}
	for _, v := range r["routing"].(map[string]any)["rules"].([]any) {
		tag, _ := v.(map[string]any)["ruleTag"].(string)
		if strings.HasPrefix(tag, "access-") || tag == "white-ip" {
			t.Fatal("disabled policy left generated routing")
		}
	}
}
func TestEditedConfigRetainsUnknownFieldsAndRejectsCredentialChanges(t *testing.T) {
	m, _ := deployed(t)
	root := configRoot(t, readFile(t, m.paths.Config()))
	root["custom"] = map[string]any{"preserve": "value"}
	if err := m.ApplyEdited(context.Background(), rootBytes(t, root)); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(readFile(t, m.paths.Config()), []byte("preserve")) {
		t.Fatal("unknown field dropped")
	}
	before := readFile(t, m.paths.Config())
	root = configRoot(t, before)
	in := root["inbounds"].([]any)[0].(map[string]any)
	in["streamSettings"].(map[string]any)["realitySettings"].(map[string]any)["privateKey"] = "changed"
	if err := m.ApplyEdited(context.Background(), rootBytes(t, root)); err == nil {
		t.Fatal("credential drift was silently committed")
	}
	if !bytes.Equal(before, readFile(t, m.paths.Config())) {
		t.Fatal("bad edit changed live file")
	}
}
func TestStatusDistinguishesStoppedDriftAndRecovery(t *testing.T) {
	m, f := deployed(t)
	s, err := m.Status(context.Background())
	if err != nil || s.Label != "运行中" {
		t.Fatalf("running status: %+v %v", s, err)
	}
	if _, err = m.Service(context.Background(), "stop"); err != nil {
		t.Fatal(err)
	}
	s, err = m.Status(context.Background())
	if err != nil || s.Label != "已停止" {
		t.Fatalf("stopped status: %+v %v", s, err)
	}
	before := readFile(t, m.paths.Config())
	if err = atomicWrite(m.paths.Config(), []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	s, err = m.Status(context.Background())
	if err == nil || s.Label != "异常" {
		t.Fatalf("drift status: %+v %v", s, err)
	}
	if err = atomicWrite(m.paths.Config(), before, 0600); err != nil {
		t.Fatal(err)
	}
	j, err := m.begin("upgrade", f.container)
	if err != nil {
		t.Fatal(err)
	}
	if err = atomicWrite(m.paths.Compose(), []byte("changed"), 0600); err != nil {
		t.Fatal(err)
	}
	s, err = m.Status(context.Background())
	if err != nil || s.Label != "需恢复" {
		t.Fatalf("pending transaction status: %+v %v", s, err)
	}
	if err = m.restoreTransaction(context.Background(), j); err != nil {
		t.Fatal(err)
	}
	f.unavailable = true
	s, err = m.Status(context.Background())
	if err == nil || s.Label != "Docker 不可用" {
		t.Fatalf("daemon failure status: %+v %v", s, err)
	}
}
func TestRollbackFailureRetainsJournalAndCanRecover(t *testing.T) {
	m, f := deployed(t)
	before := readFile(t, m.paths.Config())
	f.failRestart = 1
	m.healthCheck = func(context.Context, []int, string) error { return errors.New("persistent health failure") }
	err := m.PatchSetting(context.Background(), "log", "debug")
	if err == nil || !strings.Contains(err.Error(), "回滚未完成") {
		t.Fatalf("failure concealed: %v", err)
	}
	if _, err = os.Stat(m.paths.Journal()); err != nil {
		t.Fatal("failed rollback erased recovery record")
	}
	m.healthCheck = func(context.Context, []int, string) error { return nil }
	if err = m.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, readFile(t, m.paths.Config())) {
		t.Fatal("later recovery lost old config")
	}
}
func TestCleanupLeasePreventsWritesDuringCoordination(t *testing.T) {
	m, _ := prepared(t)
	lease, err := m.BeginCleanup(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err = m.withLock(ctx, func() error { return nil }); err == nil {
		t.Fatal("cleanup lease failed to exclude writes")
	}
	if err = lease.Cleanup(); err != nil {
		t.Fatal(err)
	}
	lease.Close()
	if err = lease.Cleanup(); err == nil {
		t.Fatal("released cleanup lease accepted")
	}
}
func TestIncompletePrepareCanBeCleanedByOwnershipMarker(t *testing.T) {
	m, f := testManager(t)
	f.rejectValidation = true
	if err := m.Prepare(context.Background(), "26.3.27"); err == nil {
		t.Fatal("prepare should fail capability check")
	}
	if err := m.Cleanup(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(m.paths.Data()); !os.IsNotExist(err) {
		t.Fatal("failed prepare data retained")
	}
}
func TestMissingDockerNeverRunsAnInstaller(t *testing.T) {
	m, f := testManager(t)
	m.lookPath = func(string) (string, error) { return "", exec.ErrNotFound }
	if err := m.Prepare(context.Background(), "26.3.27"); err == nil {
		t.Fatal("missing Docker accepted")
	}
	if len(f.calls) != 0 {
		t.Fatalf("missing dependency executed commands: %v", f.calls)
	}
	s, _ := m.Status(context.Background())
	if s.Label != "Docker 未安装" {
		t.Fatalf("missing Docker status: %+v", s)
	}
}
func TestCLICommandsAreIndependentAndRequireConfirmation(t *testing.T) {
	m, f := testManager(t)
	var out bytes.Buffer
	cmd := m.Command()
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetIn(strings.NewReader(""))
	cmd.SetArgs([]string{"--help"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if len(f.calls) != 0 {
		t.Fatal("help invoked Docker")
	}
	for _, name := range []string{"prepare", "deploy", "upgrade", "config", "service", "recover"} {
		if !strings.Contains(out.String(), name) {
			t.Fatalf("command help missing %s", name)
		}
	}
	cmd = m.Command()
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetIn(strings.NewReader(""))
	cmd.SetArgs([]string{"remove"})
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "--yes") {
		t.Fatalf("noninteractive removal not protected: %v", err)
	}
	if len(f.calls) != 0 {
		t.Fatal("unconfirmed removal invoked Docker")
	}
	cmd = m.Command()
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"prepare", "--version", "26.3.27"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	cmd = m.Command()
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"config", "generate", "--server", "example.com", "--sni", "www.example.com", "--port", "8443", "--standard-config"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if f.container == nil || !f.container.State.Running {
		t.Fatal("first config generation did not start instance")
	}
}
func TestExportDoesNotChangeDirectoryPermissions(t *testing.T) {
	m, _ := testManager(t)
	dir := t.TempDir()
	if err := os.Chmod(dir, 0755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "client.json")
	if err := m.WriteExport(path, []byte("first"), false); err != nil {
		t.Fatal(err)
	}
	if err := m.WriteExport(path, []byte("second"), false); err == nil {
		t.Fatal("export overwrote without force")
	}
	if err := m.WriteExport(path, []byte("second"), true); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0755 {
		t.Fatal("export chmodded user directory")
	}
}

func TestStateWriteFailureRestoresAllSnapshots(t *testing.T) {
	m, f := deployed(t)
	before := map[string][]byte{}
	for _, path := range m.managedFiles() {
		before[path] = readFile(t, path)
	}
	writes := 0
	m.stateWrite = func(path string, data []byte, mode os.FileMode) error {
		writes++
		if writes == 3 {
			return errors.New("simulated state disk failure")
		}
		return atomicWrite(path, data, mode)
	}
	err := m.PatchSetting(context.Background(), "log", "debug")
	if err == nil || !strings.Contains(err.Error(), "已恢复") {
		t.Fatalf("state failure not recovered: %v", err)
	}
	for path, data := range before {
		if !bytes.Equal(data, readFile(t, path)) {
			t.Fatalf("partial state commit not restored: %s", path)
		}
	}
	if f.container == nil || !f.container.State.Running {
		t.Fatal("state failure lost running instance")
	}
}

func TestTLSCertificateResetPreservesUUID(t *testing.T) {
	m, _ := prepared(t)
	if err := m.Generate(context.Background(), model.GenerateOptions{Security: "tls", Server: "example.com", Port: 8443}, true); err != nil {
		t.Fatal(err)
	}
	before, err := m.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	if err = m.Reset(context.Background(), model.ResetOptions{RotateCertificate: true}); err != nil {
		t.Fatal(err)
	}
	after, err := m.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	if after.Node.UUID != before.Node.UUID || after.Node.TLSCertificate.CertificateSHA256 == before.Node.TLSCertificate.CertificateSHA256 {
		t.Fatal("certificate-only reset changed UUID or retained old certificate")
	}
	if _, err = os.Stat(before.Node.TLSCertificate.KeyFile); err != nil {
		t.Fatal("certificate rotation erased backup reference")
	}
}
