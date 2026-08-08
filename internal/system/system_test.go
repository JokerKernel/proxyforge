package system

import (
	"bytes"
	"context"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"proxyforge/internal/domain"
)

type recordingRunner struct {
	name string
	args []string
}

func (r *recordingRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	r.name = name
	r.args = append([]string(nil), args...)
	return nil, nil
}

func TestExecRunnerStreamsStdoutAndStderr(t *testing.T) {
	var stdout, stderr bytes.Buffer
	err := (ExecRunner{}).RunStreaming(
		context.Background(),
		&stdout,
		&stderr,
		"bash",
		"-c",
		"printf stdout-data; printf stderr-data >&2",
	)
	if err != nil {
		t.Fatal(err)
	}
	if stdout.String() != "stdout-data" {
		t.Fatalf("stdout=%q", stdout.String())
	}
	if stderr.String() != "stderr-data" {
		t.Fatalf("stderr=%q", stderr.String())
	}
}

func TestRandomCredentialFormats(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 64; i++ {
		u, err := UUID()
		if err != nil {
			t.Fatal(err)
		}
		if !regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`).MatchString(u) {
			t.Fatalf("bad UUID %q", u)
		}
		s, err := ShortID()
		if err != nil {
			t.Fatal(err)
		}
		if !regexp.MustCompile(`^[0-9a-f]{16}$`).MatchString(s) {
			t.Fatalf("bad short ID %q", s)
		}
		if seen[s] {
			t.Fatalf("duplicate short ID %q", s)
		}
		seen[s] = true
	}
}

func TestValidation(t *testing.T) {
	for _, tt := range []struct {
		name string
		err  error
		want bool
	}{
		{"port zero", ValidatePort(0), true}, {"port max", ValidatePort(65535), false},
		{"sni ip", ValidateSNI("1.1.1.1"), true}, {"sni domain", ValidateSNI("www.example.com"), false},
		{"target missing port", ValidateTarget("example.com"), true}, {"target good", ValidateTarget("example.com:443"), false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if (tt.err != nil) != tt.want {
				t.Fatalf("error=%v wantErr=%v", tt.err, tt.want)
			}
		})
	}
}

func TestStatePermissionsAndIsolation(t *testing.T) {
	root := t.TempDir()
	store := StateStore{Layout: Layout{Root: root}}
	n := domain.NodeSpec{ManagedBy: "proxyforge", Core: "sing-box", Server: "example.com", UpdatedAt: time.Now()}
	if err := store.Save(n); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(store.Layout.StatePath("sing-box"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("state mode=%o", info.Mode().Perm())
	}
	if _, err := store.Load("xray"); err != ErrNoState {
		t.Fatalf("xray state leaked: %v", err)
	}
	got, err := store.Load("sing-box")
	if err != nil {
		t.Fatal(err)
	}
	if got.Server != n.Server {
		t.Fatalf("got %#v", got)
	}
}

func TestStateRejectsUnsafePermissions(t *testing.T) {
	root := t.TempDir()
	store := StateStore{Layout: Layout{Root: root}}
	n := domain.NodeSpec{ManagedBy: "proxyforge", Core: "sing-box"}
	if err := store.Save(n); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(store.Layout.StatePath("sing-box"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load("sing-box"); err == nil {
		t.Fatal("expected unsafe permission error")
	}
}

func TestAtomicWriteAndBackupPermissions(t *testing.T) {
	root := t.TempDir()
	path := root + "/config/config.json"
	if err := AtomicWrite(path, []byte("old"), 0640); err != nil {
		t.Fatal(err)
	}
	backup, err := BackupFile(path, root+"/backups", time.Unix(1, 0))
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(backup)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "old" {
		t.Fatalf("backup=%q", b)
	}
	info, _ := os.Stat(backup)
	if info.Mode().Perm() != 0600 {
		t.Fatalf("backup mode=%o", info.Mode().Perm())
	}
	if err := AtomicWrite(path, []byte("new"), 0600); err != nil {
		t.Fatal(err)
	}
	b, _ = os.ReadFile(path)
	if string(b) != "new" {
		t.Fatalf("atomic result=%q", b)
	}
}

func TestRemovePackageUsesDistributionNativeDatabase(t *testing.T) {
	for _, tt := range []struct {
		name, osRelease, command string
		args                     []string
	}{
		{name: "debian", osRelease: "ID=ubuntu\nID_LIKE=debian\n", command: "dpkg", args: []string{"--remove", "sing-box"}},
		{name: "rhel", osRelease: "ID=rocky\nID_LIKE=\"rhel centos fedora\"\n", command: "rpm", args: []string{"-e", "sing-box"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			if err := os.MkdirAll(root+"/etc", 0755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(root+"/etc/os-release", []byte(tt.osRelease), 0644); err != nil {
				t.Fatal(err)
			}
			runner := &recordingRunner{}
			if err := RemovePackage(context.Background(), runner, Layout{Root: root}, "sing-box"); err != nil {
				t.Fatal(err)
			}
			if runner.name != tt.command || strings.Join(runner.args, " ") != strings.Join(tt.args, " ") {
				t.Fatalf("command=%q args=%v, want %q %v", runner.name, runner.args, tt.command, tt.args)
			}
		})
	}
}
