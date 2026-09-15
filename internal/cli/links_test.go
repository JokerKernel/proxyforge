package cli

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"proxyforge/internal/app"
	"proxyforge/internal/domain"
	"proxyforge/internal/system"
)

const validLandingText = `{
  "managed_by": "proxyforge",
  "schema_version": 1,
  "kind": "proxyforge-landing",
  "peer": {
    "name": "lan-test",
    "core": "xray",
    "server": "192.168.1.20",
    "port": 443,
    "sni": "exit.example.com",
    "uuid": "123e4567-e89b-42d3-a456-426614174000",
    "public_key": "test-public-key",
    "short_id": "0123456789abcdef",
    "flow": "xtls-rprx-vision"
  }
}`

func TestPasteLandingBundleUsesPrivateTemporaryFileAndDeletesIt(t *testing.T) {
	var out bytes.Buffer
	var openedPath string
	c := &commandSet{
		out: &out,
		lookPath: func(name string) (string, error) {
			return "/usr/bin/" + name, nil
		},
		runEditor: func(editor, path string) error {
			openedPath = path
			info, err := os.Stat(path)
			if err != nil {
				return err
			}
			if got := info.Mode().Perm(); got != 0600 {
				t.Fatalf("temporary file mode=%#o, want 0600", got)
			}
			return os.WriteFile(path, []byte(validLandingText), 0600)
		},
	}

	peer, err := c.pasteLandingBundle()
	if err != nil {
		t.Fatal(err)
	}
	if peer.Server != "192.168.1.20" || peer.Port != 443 || peer.Name != "lan-test" {
		t.Fatalf("unexpected peer: %+v", peer)
	}
	if openedPath == "" {
		t.Fatal("editor was not opened")
	}
	if _, err := os.Stat(openedPath); !os.IsNotExist(err) {
		t.Fatalf("temporary file still exists after import: %q, error=%v", openedPath, err)
	}
	for _, want := range []string{"粘贴落地服务器生成的完整 JSON", "临时文件将在导入后自动删除"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("output missing %q: %q", want, out.String())
		}
	}
}

func TestReadLandingPeerInputFromStdin(t *testing.T) {
	c := &commandSet{in: strings.NewReader(validLandingText)}
	peer, err := c.readLandingPeerInput("", true)
	if err != nil {
		t.Fatal(err)
	}
	if peer.Core != "xray" || peer.Server != "192.168.1.20" {
		t.Fatalf("unexpected peer: %+v", peer)
	}
}

func TestReadLandingPeerInputRejectsAmbiguousOrMissingSource(t *testing.T) {
	c := &commandSet{in: strings.NewReader(validLandingText)}
	if _, err := c.readLandingPeerInput("landing.json", true); err == nil || !strings.Contains(err.Error(), "不能同时使用") {
		t.Fatalf("ambiguous source error=%v", err)
	}
	if _, err := c.readLandingPeerInput("", false); err == nil || !strings.Contains(err.Error(), "--upstream-stdin") {
		t.Fatalf("missing source error=%v", err)
	}
}

func TestCreateLandingMenuOffersCurrentRealityAndIndependentTLS(t *testing.T) {
	store := system.StateStore{Layout: system.Layout{Root: t.TempDir()}}
	if err := store.Save(domain.NodeSpec{ManagedBy: "proxyforge", Core: domain.CoreXray, Port: 443}); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	c := &commandSet{
		app: &app.App{Store: store}, reader: bufio.NewReader(strings.NewReader("q\n")), out: &out,
	}
	err := c.addLandingInteractive(context.Background(), domain.CoreXray)
	if !errors.Is(err, errReturnToMenu) {
		t.Fatalf("error=%v", err)
	}
	for _, want := range []string{
		"使用当前协议", "VLESS + RAW + REALITY + Vision，复用端口 443",
		"创建 VLESS + RAW + TLS + Vision", "随机高位独立端口",
	} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("create landing menu missing %q: %q", want, out.String())
		}
	}
}

func TestTLSLandingCreationDoesNotAskForPortDomainOrCertificatePaths(t *testing.T) {
	store := system.StateStore{Layout: system.Layout{Root: t.TempDir()}}
	if err := store.Save(domain.NodeSpec{
		ManagedBy: "proxyforge", Core: domain.CoreXray, Port: 443, InboundTag: "xray-one",
		UUID: "123e4567-e89b-42d3-a456-426614174000",
	}); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	c := &commandSet{
		app:    &app.App{Store: store, RootCheck: func() error { return nil }},
		reader: bufio.NewReader(strings.NewReader("2\nauto-tls\nn\n")), out: &out,
	}
	err := c.addLandingInteractive(context.Background(), domain.CoreXray)
	if !errors.Is(err, errReturnToMenu) {
		t.Fatalf("error=%v", err)
	}
	for _, unwanted := range []string{"独立 TLS 监听端口（", "TLS 证书域名", "TLS 证书链文件", "TLS 私钥文件"} {
		if strings.Contains(out.String(), unwanted) {
			t.Fatalf("automatic TLS flow still prompted for %q: %s", unwanted, out.String())
		}
	}
	if !strings.Contains(out.String(), "自动选择 30000–65000") || !strings.Contains(out.String(), "自签证书") {
		t.Fatalf("automatic TLS confirmation missing: %s", out.String())
	}
}

func TestTLSLandingCLIHasNoManualCertificateFlags(t *testing.T) {
	c := &commandSet{}
	cmd := c.landingAddCommand()
	for _, name := range []string{"port", "server-name", "cert-file", "key-file"} {
		if cmd.Flags().Lookup(name) != nil {
			t.Fatalf("obsolete TLS flag --%s remains", name)
		}
	}
}

func TestNextLandingAccessNameUsesFirstAvailableSequence(t *testing.T) {
	node := domain.NodeSpec{
		LandingAccesses: []domain.LandingAccess{{Name: "relay-1"}, {UserName: "RELAY-3"}},
		RelayLinks:      []domain.RelayLink{{Name: "relay-2"}},
	}
	if got := nextLandingAccessName(node); got != "relay-4" {
		t.Fatalf("next landing name=%q, want relay-4", got)
	}
	node.LandingAccesses = []domain.LandingAccess{{Name: "relay-2"}}
	node.RelayLinks = nil
	if got := nextLandingAccessName(node); got != "relay-1" {
		t.Fatalf("first gap name=%q, want relay-1", got)
	}
}

func TestManualTLSLandingPeerDoesNotRequestRealityKeys(t *testing.T) {
	input := strings.Join([]string{
		"tls-exit", "1", "2", "192.168.1.20", "8443", "tls.example.com",
		"123e4567-e89b-42d3-a456-426614174000",
		strings.Repeat("a", 64), "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=",
	}, "\n") + "\n"
	var out bytes.Buffer
	c := &commandSet{reader: bufio.NewReader(strings.NewReader(input)), out: &out}
	peer, err := c.askLandingPeer()
	if err != nil {
		t.Fatal(err)
	}
	if peer.Security != domain.LandingSecurityTLS || peer.PublicKey != "" || peer.ShortID != "" || peer.Port != 8443 || peer.CertificateSHA256 == "" || peer.CertificatePublicKeySHA256 == "" {
		t.Fatalf("peer=%#v", peer)
	}
	if strings.Contains(out.String(), "REALITY 公钥") || strings.Contains(out.String(), "short ID") {
		t.Fatalf("TLS manual flow requested REALITY credentials: %q", out.String())
	}
}

func TestManageRelaySelectsLongLineNameByNumber(t *testing.T) {
	store := system.StateStore{Layout: system.Layout{Root: t.TempDir()}}
	if err := store.Save(domain.NodeSpec{
		ManagedBy: "proxyforge", Core: domain.CoreXray,
		RelayLinks: []domain.RelayLink{
			{Name: "short", Enabled: true, Upstream: domain.LandingPeer{Server: "192.168.1.10", Port: 443}},
			{Name: "very-long-relay-line-name", Enabled: false, Upstream: domain.LandingPeer{Server: "192.168.1.20", Port: 8443}},
		},
	}); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	c := &commandSet{
		app:    &app.App{Store: store},
		reader: bufio.NewReader(strings.NewReader("2\n0\n")),
		out:    &out,
	}
	err := c.manageRelayInteractive(context.Background(), domain.CoreXray)
	if !errors.Is(err, errReturnToMenu) {
		t.Fatalf("error=%v", err)
	}
	for _, want := range []string{
		"1   short · short-out · 192.168.1.10:443", "2   very-long-relay-line-name · very-long-relay-line-name-out · 192.168.1.20:8443",
		"[已启用]", "[已停用]", "管理中转线路  ›  very-long-relay-line-name-out",
		"线路名称：very-long-relay-line-name · 客户端用户：very-long-relay-line-name · 出站 tag：very-long-relay-line-name-out",
		"当前落地：192.168.1.20:8443 · REALITY",
	} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("relay management output missing %q: %q", want, out.String())
		}
	}
}

func TestManageLandingSelectsLongAccessNameByNumber(t *testing.T) {
	store := system.StateStore{Layout: system.Layout{Root: t.TempDir()}}
	if err := store.Save(domain.NodeSpec{
		ManagedBy: "proxyforge", Core: domain.CoreSingBox,
		LandingAccesses: []domain.LandingAccess{
			{Name: "first", UserName: "first", Enabled: true},
			{Name: "very-long-landing-access-name", UserName: "very-long-landing-access-name", Enabled: false},
		},
	}); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	c := &commandSet{
		app:    &app.App{Store: store},
		reader: bufio.NewReader(strings.NewReader("2\n0\n")),
		out:    &out,
	}
	err := c.manageLandingInteractive(context.Background(), domain.CoreSingBox)
	if !errors.Is(err, errReturnToMenu) {
		t.Fatalf("error=%v", err)
	}
	for _, want := range []string{
		"1   first", "2   very-long-landing-access-name",
		"管理落地接入  ›  very-long-landing-access-name", "当前状态：已停用",
	} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("landing management output missing %q: %q", want, out.String())
		}
	}
}
