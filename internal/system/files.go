package system

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"time"
)

const (
	backupRetention       = 3
	backupTimestampLayout = "20060102T150405.000000000Z"
)

func AtomicWrite(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".proxyforge-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	ok := false
	defer func() {
		if !ok {
			_ = os.Remove(tmp)
		}
	}()
	if err = f.Chmod(mode); err == nil {
		_, err = f.Write(data)
	}
	if err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err = os.Rename(tmp, path); err != nil {
		return err
	}
	ok = true
	if d, openErr := os.Open(dir); openErr == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}

func SHA256(data []byte) string {
	s := sha256.Sum256(data)
	return hex.EncodeToString(s[:])
}

func BackupFile(source, backupRoot string, now time.Time) (string, error) {
	in, err := os.Open(source)
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	defer in.Close()
	if err := os.MkdirAll(backupRoot, 0700); err != nil {
		return "", err
	}
	if err := os.Chmod(backupRoot, 0700); err != nil {
		return "", err
	}
	dir := filepath.Join(backupRoot, now.UTC().Format(backupTimestampLayout))
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", err
	}
	dest := filepath.Join(dir, filepath.Base(source))
	out, err := os.OpenFile(dest, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return "", err
	}
	_, copyErr := io.Copy(out, in)
	if syncErr := out.Sync(); copyErr == nil {
		copyErr = syncErr
	}
	if closeErr := out.Close(); copyErr == nil {
		copyErr = closeErr
	}
	if copyErr != nil {
		return "", fmt.Errorf("备份 %s: %w", source, copyErr)
	}
	if err := pruneBackupDirectories(backupRoot, backupRetention); err != nil {
		return "", fmt.Errorf("清理旧备份: %w", err)
	}
	return dest, nil
}

func pruneBackupDirectories(backupRoot string, keep int) error {
	entries, err := os.ReadDir(backupRoot)
	if err != nil {
		return err
	}
	var names []string
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		if _, err := time.Parse(backupTimestampLayout, entry.Name()); err == nil {
			names = append(names, entry.Name())
		}
	}
	if len(names) <= keep {
		return nil
	}
	sort.Strings(names)
	for _, name := range names[:len(names)-keep] {
		if err := os.RemoveAll(filepath.Join(backupRoot, name)); err != nil {
			return err
		}
	}
	return nil
}
