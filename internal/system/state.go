package system

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"proxyforge/internal/domain"
)

var ErrNoState = errors.New("尚未生成受管节点")

type StateStore struct{ Layout Layout }

func (s StateStore) Load(core string) (domain.NodeSpec, error) {
	path := s.Layout.StatePath(core)
	if info, statErr := os.Stat(path); statErr == nil && info.Mode().Perm()&0077 != 0 {
		return domain.NodeSpec{}, fmt.Errorf("状态文件权限不安全 %s（当前 %o，要求 0600）", path, info.Mode().Perm())
	}
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return domain.NodeSpec{}, ErrNoState
	}
	if err != nil {
		return domain.NodeSpec{}, err
	}
	var n domain.NodeSpec
	if err := json.Unmarshal(b, &n); err != nil {
		return n, fmt.Errorf("状态文件损坏 %s: %w", path, err)
	}
	if n.ManagedBy != "proxyforge" || n.Core != core {
		return n, fmt.Errorf("状态文件标识无效: %s", path)
	}
	return n, nil
}

func (s StateStore) Save(n domain.NodeSpec) error {
	b, err := json.MarshalIndent(n, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	path := s.Layout.StatePath(n.Core)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	if err := os.Chmod(filepath.Dir(path), 0700); err != nil {
		return err
	}
	return AtomicWrite(path, b, 0600)
}

func (s StateStore) Delete(core string) error {
	err := os.Remove(s.Layout.StatePath(core))
	if os.IsNotExist(err) {
		return nil
	}
	return err
}
