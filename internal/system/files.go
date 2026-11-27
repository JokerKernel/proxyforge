package system

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
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

func AtomicWrite(path string, data []byte, mode os.FileMode) (result error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("创建配置目录 %s: %w", dir, err)
	}
	f, err := os.CreateTemp(dir, ".proxyforge-*")
	if err != nil {
		return fmt.Errorf("创建临时配置 %s: %w", path, err)
	}
	tmp := f.Name()
	ok := false
	defer func() {
		if !ok {
			if err := os.Remove(tmp); err != nil && !os.IsNotExist(err) {
				result = errors.Join(result, fmt.Errorf("清理临时配置 %s: %w", tmp, err))
			}
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
		return fmt.Errorf("写入临时配置 %s: %w", tmp, err)
	}
	if err = os.Rename(tmp, path); err != nil {
		return fmt.Errorf("替换配置 %s: %w", path, err)
	}
	ok = true
	// After rename the write is committed. Directory sync remains best effort
	// so callers never mistake a committed write for a failed replacement.
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

func BackupFile(source, backupRoot string, now time.Time) (path string, result error) {
	in, err := os.Open(source)
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("打开备份源 %s: %w", source, err)
	}
	defer in.Close()
	if err := os.MkdirAll(backupRoot, 0700); err != nil {
		return "", fmt.Errorf("创建备份根目录 %s: %w", backupRoot, err)
	}
	if err := os.Chmod(backupRoot, 0700); err != nil {
		return "", fmt.Errorf("设置备份目录权限 %s: %w", backupRoot, err)
	}
	dir := filepath.Join(backupRoot, now.UTC().Format(backupTimestampLayout))
	err = os.Mkdir(dir, 0700)
	createdDir := err == nil
	if err != nil && !os.IsExist(err) {
		return "", fmt.Errorf("创建备份目录 %s: %w", dir, err)
	}
	complete := false
	defer func() {
		if createdDir && !complete {
			if err := os.Remove(dir); err != nil {
				result = errors.Join(result, fmt.Errorf("清理失败的备份目录 %s: %w", dir, err))
			}
		}
	}()
	dest := filepath.Join(dir, filepath.Base(source))
	out, err := os.OpenFile(dest, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return "", fmt.Errorf("创建备份文件 %s: %w", dest, err)
	}
	_, copyErr := io.Copy(out, in)
	if copyErr == nil {
		copyErr = out.Sync()
	}
	if closeErr := out.Close(); copyErr == nil {
		copyErr = closeErr
	}
	if copyErr != nil {
		failures := []error{fmt.Errorf("备份 %s: %w", source, copyErr)}
		if err := os.Remove(dest); err != nil {
			failures = append(failures, fmt.Errorf("清理不完整备份 %s: %w", dest, err))
		}
		return "", errors.Join(failures...)
	}
	complete = true
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
