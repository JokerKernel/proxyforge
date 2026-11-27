package cli

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestLandingPasteCleansAbandonedDirectoriesAndPreservesActiveSessions(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	active, err := newLandingPasteWorkspace(nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = active.close() })
	stale, err := newLandingPasteWorkspace(nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stale.close() })
	if err := os.WriteFile(filepath.Join(stale.dir, "landing.json~"), []byte("credentials"), 0600); err != nil {
		t.Fatal(err)
	}
	// Releasing the lease without deleting files simulates an interrupted process.
	if err := stale.lock.Close(); err != nil {
		t.Fatal(err)
	}
	stale.lock = nil
	root := filepath.Dir(active.dir)
	unrelated := filepath.Join(root, "keep-me")
	if err := os.Mkdir(unrelated, 0700); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	outsideFile := filepath.Join(outside, "keep.json")
	if err := os.WriteFile(outsideFile, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "session-symlink")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(stale.dir, "editor-backup-link")); err != nil {
		t.Fatal(err)
	}
	next, err := newLandingPasteWorkspace(func(path string, err error) { t.Errorf("cleanup %s: %v", path, err) })
	if err != nil {
		t.Fatal(err)
	}
	defer next.close()
	if _, err := os.Stat(stale.dir); !os.IsNotExist(err) {
		t.Fatalf("abandoned session was not removed: %v", err)
	}
	for _, path := range []string{active.path(), unrelated, outsideFile} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("unrelated or active data removed: %s: %v", path, err)
		}
	}
}

func TestLandingPasteConcurrentCreationAndCleanupRemovesEmptyRoot(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	var workers sync.WaitGroup
	for i := 0; i < 8; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for j := 0; j < 12; j++ {
				w, err := newLandingPasteWorkspace(func(path string, err error) { t.Errorf("stale cleanup %s: %v", path, err) })
				if err != nil {
					t.Errorf("concurrent creation: %v", err)
					return
				}
				if _, err := os.Stat(w.path()); err != nil {
					t.Errorf("active paste file removed: %v", err)
				}
				if err := w.close(); err != nil {
					t.Errorf("concurrent cleanup: %v", err)
					return
				}
			}
		}()
	}
	workers.Wait()
	root := filepath.Join(os.TempDir(), fmt.Sprintf("proxyforge-landing-paste-%d", os.Geteuid()))
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatalf("empty outer directory survived concurrent cleanup: %v", err)
	}
}

func TestLandingPasteLeaseSurvivesInChildProcess(t *testing.T) {
	const helperEnv = "PROXYFORGE_PASTE_LEASE_TEST"
	if os.Getenv(helperEnv) == "1" {
		lease := os.NewFile(3, "paste-directory")
		defer lease.Close()
		fmt.Fprintln(os.Stdout, "ready")
		_, _ = io.Copy(io.Discard, os.Stdin)
		return
	}
	t.Setenv("TMPDIR", t.TempDir())
	w, err := newLandingPasteWorkspace(nil)
	if err != nil {
		t.Fatal(err)
	}
	defer w.close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestLandingPasteLeaseSurvivesInChildProcess$")
	cmd.Env = append(os.Environ(), helperEnv+"=1")
	cmd.ExtraFiles = []*os.File{w.lock}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	defer stdin.Close()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = stdin.Close()
		_ = cmd.Wait()
	}()
	if ready, err := bufio.NewReader(stdout).ReadString('\n'); err != nil || ready != "ready\n" {
		t.Fatalf("child readiness = %q, error=%v", ready, err)
	}
	if err := w.lock.Close(); err != nil {
		t.Fatal(err)
	}
	w.lock = nil
	next, err := newLandingPasteWorkspace(nil)
	if err != nil {
		t.Fatal(err)
	}
	defer next.close()
	if _, err := os.Stat(w.path()); err != nil {
		t.Fatalf("session still in use by editor child was removed: %v", err)
	}
	_ = stdin.Close()
	if err := cmd.Wait(); err != nil {
		t.Fatal(err)
	}
	afterChild, err := newLandingPasteWorkspace(nil)
	if err != nil {
		t.Fatal(err)
	}
	defer afterChild.close()
	if _, err := os.Stat(w.dir); !os.IsNotExist(err) {
		t.Fatalf("editor's abandoned session was not removed after exit: %v", err)
	}
}

func TestPasteLandingBundleCleansDirectoryOnFailure(t *testing.T) {
	for _, test := range []struct {
		name, text string
		editorErr  error
	}{
		{name: "editor failed", editorErr: errors.New("editor failed")},
		{name: "empty text"},
		{name: "invalid text", text: "invalid JSON"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("TMPDIR", t.TempDir())
			var path string
			c := &commandSet{out: io.Discard,
				lookPath: func(string) (string, error) { return "/usr/bin/vi", nil },
				runEditor: func(_ string, filename string) error {
					path = filename
					if err := os.WriteFile(filename+"~", []byte("backup"), 0600); err != nil {
						return err
					}
					if test.editorErr != nil {
						return test.editorErr
					}
					return os.WriteFile(filename, []byte(test.text), 0600)
				},
			}
			if _, err := c.pasteLandingBundle(); err == nil {
				t.Fatal("expected import failure")
			}
			if path == "" {
				t.Fatal("editor did not open")
			}
			if _, err := os.Stat(filepath.Dir(path)); !os.IsNotExist(err) {
				t.Fatalf("temporary directory survived failed import: %v", err)
			}
			if _, err := os.Stat(filepath.Dir(filepath.Dir(path))); !os.IsNotExist(err) {
				t.Fatalf("empty outer directory survived failed import: %v", err)
			}
		})
	}
}

func TestLandingPasteRefusesUnsafeRootDirectory(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	root := filepath.Join(os.TempDir(), fmt.Sprintf("proxyforge-landing-paste-%d", os.Geteuid()))
	outside := t.TempDir()
	if err := os.Symlink(outside, root); err != nil {
		t.Fatal(err)
	}
	if _, err := newLandingPasteWorkspace(nil); err == nil {
		t.Fatal("accepted a symlink as the private workspace root")
	}
	entries, err := os.ReadDir(outside)
	if err != nil || len(entries) != 0 {
		t.Fatalf("symlink target was modified: %v, error=%v", entries, err)
	}
}

func TestPasteCleanupFailureReportsResidualPathAndRetries(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses file permission failures")
	}
	t.Setenv("TMPDIR", t.TempDir())
	var output, warnings bytes.Buffer
	var dir, protected string
	c := &commandSet{
		out: &output, errOut: &warnings,
		lookPath: func(string) (string, error) { return "/usr/bin/vi", nil },
		runEditor: func(_ string, path string) error {
			dir = filepath.Dir(path)
			protected = filepath.Join(dir, "editor-backups")
			if err := os.Mkdir(protected, 0700); err != nil {
				return err
			}
			t.Cleanup(func() { _ = os.Chmod(protected, 0700) })
			if err := os.WriteFile(filepath.Join(protected, "landing.json~"), []byte("backup"), 0600); err != nil {
				return err
			}
			if err := os.Chmod(protected, 0000); err != nil {
				return err
			}
			return os.WriteFile(path, []byte(validLandingText), 0600)
		},
	}
	peer, err := c.pasteLandingBundle()
	if err != nil || peer.Name != "lan-test" {
		t.Fatalf("valid import was lost after cleanup failure: peer=%+v error=%v", peer, err)
	}
	for _, want := range []string{"临时目录清理失败", dir, "下次使用"} {
		if want == "" || !strings.Contains(warnings.String(), want) {
			t.Fatalf("cleanup failure lost diagnostics: %q", warnings.String())
		}
	}
	if strings.Contains(output.String(), "已删除") {
		t.Fatalf("cleanup was incorrectly reported as successful: %s", output.String())
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("expected a residual directory: %v", err)
	}
	if err := os.Chmod(protected, 0700); err != nil {
		t.Fatal(err)
	}
	next, err := newLandingPasteWorkspace(func(path string, err error) { t.Errorf("retry %s: %v", path, err) })
	if err != nil {
		t.Fatal(err)
	}
	defer next.close()
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("residual directory was not removed on retry: %v", err)
	}
}
