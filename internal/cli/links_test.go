package cli

import (
	"bytes"
	"os"
	"strings"
	"testing"
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
