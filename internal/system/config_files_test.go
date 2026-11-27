package system

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestPrivateConfigTransactionRestoresFilesAfterLateFailure(t *testing.T) {
	root := t.TempDir()
	existing := filepath.Join(root, "existing.json")
	before := []byte("{\"mode\":\"default\"}\n")
	if err := AtomicWrite(existing, before, 0400); err != nil {
		t.Fatal(err)
	}
	blocker := filepath.Join(root, "blocker")
	files := []privateJSONFile{
		{path: existing, data: []byte(`{"mode":"whitelist"}`)},
		{path: blocker, data: []byte(`[]`)},
		// The second write creates a file where the third needs a directory.
		{path: filepath.Join(blocker, "later.json"), data: []byte(`[]`)},
	}
	if err := writePrivateFiles(files); err == nil {
		t.Fatal("expected late write failure")
	}
	after, err := os.ReadFile(existing)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("completed write was not restored: %q err=%v", after, err)
	}
	info, err := os.Stat(existing)
	if err != nil || info.Mode().Perm() != 0400 {
		t.Fatalf("original file permissions were not restored: info=%v err=%v", info, err)
	}
	if _, err := os.Stat(blocker); !os.IsNotExist(err) {
		t.Fatalf("failed transaction left a new file: %v", err)
	}
}
