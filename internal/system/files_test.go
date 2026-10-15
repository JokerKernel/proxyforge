package system

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestFailedBackupRemovesPartialFileWithoutPruning(t *testing.T) {
	root := t.TempDir()
	backupRoot := filepath.Join(root, "backups")
	source := filepath.Join(root, "config.json")
	if err := os.WriteFile(source, []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}
	var goodBackups []string
	for i := range backupRetention {
		path, err := BackupFile(source, backupRoot, time.Unix(int64(i), 0))
		if err != nil {
			t.Fatal(err)
		}
		goodBackups = append(goodBackups, path)
	}
	// Reading a directory fails during io.Copy, after the backup file is created.
	badSource := filepath.Join(root, "directory")
	if err := os.Mkdir(badSource, 0700); err != nil {
		t.Fatal(err)
	}
	when := time.Unix(100, 0)
	path, err := BackupFile(badSource, backupRoot, when)
	var pathErr *os.PathError
	if path != "" || !errors.As(err, &pathErr) {
		t.Fatalf("expected wrapped read failure: path=%s error=%v", path, err)
	}
	failedDir := filepath.Join(backupRoot, when.UTC().Format(backupTimestampLayout))
	if _, err := os.Stat(failedDir); !os.IsNotExist(err) {
		t.Fatalf("failed backup directory remains: %v", err)
	}
	for _, path := range goodBackups {
		got, err := os.ReadFile(path)
		if err != nil || string(got) != "original" {
			t.Fatalf("good backup changed: %s, %v", got, err)
		}
	}
}

func TestFailedBackupPreservesExistingTimestampDirectory(t *testing.T) {
	root := t.TempDir()
	backupRoot := filepath.Join(root, "backups")
	when := time.Unix(1, 0)
	dir := filepath.Join(backupRoot, when.UTC().Format(backupTimestampLayout))
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	keep := filepath.Join(dir, "keep.json")
	if err := os.WriteFile(keep, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(root, "directory")
	if err := os.Mkdir(source, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := BackupFile(source, backupRoot, when); err == nil {
		t.Fatal("expected copy failure")
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 || entries[0].Name() != "keep.json" {
		t.Fatalf("existing backup directory changed: %v, %v", entries, err)
	}
}

func TestAtomicWriteRenameFailureCleansTemporaryFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	var pathErr *os.LinkError
	if err := AtomicWrite(path, []byte("config"), 0600); !errors.As(err, &pathErr) {
		t.Fatalf("expected wrapped rename error: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 || entries[0].Name() != "config.json" || !entries[0].IsDir() {
		t.Fatalf("unexpected files after rename failure: %v, %v", entries, err)
	}
}
