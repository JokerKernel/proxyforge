package docker

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"
)

const maxConfigSize = 8 << 20

func digest(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }

// Reject symlinked managed paths, including symlinked parents before creating or
// deleting anything. This also prevents a relocated mount escaping ownership.
func safePath(path string) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) == "/" {
		return fmt.Errorf("不安全路径: %q", path)
	}
	for p := filepath.Clean(path); p != "/"; p = filepath.Dir(p) {
		info, err := os.Lstat(p)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("受管路径不能是符号链接: %s", p)
		}
	}
	return nil
}
func privateDir(path string) error {
	if err := safePath(path); err != nil {
		return err
	}
	if err := os.MkdirAll(path, 0700); err != nil {
		return err
	}
	return os.Chmod(path, 0700)
}
func atomicWrite(path string, data []byte, mode os.FileMode) error {
	if err := safePath(path); err != nil {
		return err
	}
	if err := privateDir(filepath.Dir(path)); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".docker-write-")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if err = f.Chmod(mode); err == nil {
		_, err = f.Write(data)
	}
	if err == nil {
		err = f.Sync()
	}
	if e := f.Close(); err == nil {
		err = e
	}
	if err != nil {
		return err
	}
	if err = os.Rename(tmp, path); err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
func readPrivate(path string) ([]byte, error) {
	if err := safePath(path); err != nil {
		return nil, err
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return nil, fmt.Errorf("受管文件需要普通文件及 0600 权限: %s", path)
	}
	if info.Size() > maxConfigSize {
		return nil, fmt.Errorf("文件超过大小限制: %s", path)
	}
	return os.ReadFile(path)
}
func loadJSON(path string, v any) error {
	b, err := readPrivate(path)
	if err != nil {
		return err
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if err = d.Decode(v); err != nil {
		return fmt.Errorf("管理数据损坏 %s: %w", path, err)
	}
	var extra any
	if err = d.Decode(&extra); !errors.Is(err, io.EOF) {
		return fmt.Errorf("管理数据包含多余内容: %s", path)
	}
	return nil
}
func jsonBytes(v any) ([]byte, error) {
	b, err := json.MarshalIndent(v, "", "  ")
	return append(b, '\n'), err
}
func saveJSON(path string, v any) error {
	b, err := jsonBytes(v)
	if err != nil {
		return err
	}
	return atomicWrite(path, b, 0600)
}

func decodeStrict(b []byte, v any) error {
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		return err
	}
	var extra any
	if err := d.Decode(&extra); !errors.Is(err, io.EOF) {
		return fmt.Errorf("JSON 包含多余内容")
	}
	return nil
}

type fileSnapshot struct {
	Path   string `json:"path"`
	Exists bool   `json:"exists"`
	Data   []byte `json:"data,omitempty"`
	Mode   uint32 `json:"mode"`
	UID    int    `json:"uid"`
	GID    int    `json:"gid"`
}

func snapshot(path string) (fileSnapshot, error) {
	s := fileSnapshot{Path: path}
	if err := safePath(path); err != nil {
		return s, err
	}
	info, err := os.Stat(path)
	if os.IsNotExist(err) {
		return s, nil
	}
	if err != nil {
		return s, err
	}
	if !info.Mode().IsRegular() {
		return s, fmt.Errorf("受管文件不是普通文件: %s", path)
	}
	s.Exists = true
	s.Mode = uint32(info.Mode().Perm())
	if stat, ok := info.Sys().(*syscall.Stat_t); ok {
		s.UID = int(stat.Uid)
		s.GID = int(stat.Gid)
	}
	s.Data, err = readPrivate(path)
	return s, err
}
func restore(s fileSnapshot) error {
	if err := safePath(s.Path); err != nil {
		return err
	}
	if !s.Exists {
		err := os.Remove(s.Path)
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if err := atomicWrite(s.Path, s.Data, os.FileMode(s.Mode)); err != nil {
		return err
	}
	if os.Geteuid() == 0 {
		if err := os.Chown(s.Path, s.UID, s.GID); err != nil {
			return err
		}
	}
	return os.Chmod(s.Path, os.FileMode(s.Mode))
}
