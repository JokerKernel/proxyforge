package system

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"proxyforge/internal/domain"
)

type AccessPolicyStore struct{ Layout Layout }

func (s AccessPolicyStore) Load(core string) (domain.AccessPolicy, error) {
	path := s.Layout.AccessPolicyPath(core)
	b, exists, err := readAccessPolicyFile(path)
	if err != nil {
		return domain.AccessPolicy{}, err
	}
	if !exists {
		return domain.AccessPolicy{Mode: domain.AccessModeDefault}, nil
	}
	var policy *domain.AccessPolicy
	decoder := json.NewDecoder(bytes.NewReader(b))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&policy); err != nil {
		return domain.AccessPolicy{}, fmt.Errorf("访问控制配置损坏 %s: %w", path, err)
	}
	if policy == nil {
		return domain.AccessPolicy{}, fmt.Errorf("访问控制配置必须是 JSON 对象: %s", path)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return domain.AccessPolicy{}, fmt.Errorf("访问控制配置包含多余或无效内容: %s", path)
	}
	normalized, err := policy.Normalize()
	if err != nil {
		return domain.AccessPolicy{}, fmt.Errorf("访问控制配置无效 %s: %w", path, err)
	}
	normalized.Mode = normalized.EffectiveMode()
	return normalized, nil
}

func (s AccessPolicyStore) Save(core string, policy domain.AccessPolicy) error {
	normalized, err := policy.Normalize()
	if err != nil {
		return err
	}
	// Keep all three settings visible, including empty lists.
	config := struct {
		Mode      string               `json:"mode"`
		Whitelist []domain.AccessEntry `json:"whitelist"`
		Blacklist []domain.AccessEntry `json:"blacklist"`
	}{
		Mode:      normalized.EffectiveMode(),
		Whitelist: append([]domain.AccessEntry{}, normalized.Whitelist...),
		Blacklist: append([]domain.AccessEntry{}, normalized.Blacklist...),
	}
	b, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return err
	}
	path := s.Layout.AccessPolicyPath(core)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	if err := os.Chmod(filepath.Dir(path), 0700); err != nil {
		return err
	}
	return AtomicWrite(path, append(b, '\n'), 0600)
}

func (s AccessPolicyStore) Delete(core string) error {
	return removeAccessPolicyFile(s.Layout.AccessPolicyPath(core))
}

func readAccessPolicyFile(path string) ([]byte, bool, error) {
	info, err := os.Stat(path)
	if os.IsNotExist(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if info.Mode().Perm()&0077 != 0 {
		return nil, false, fmt.Errorf("访问控制配置权限不安全 %s（当前 %o，要求 0600）", path, info.Mode().Perm())
	}
	b, err := os.ReadFile(path)
	return b, true, err
}

func removeAccessPolicyFile(path string) error {
	err := os.Remove(path)
	if os.IsNotExist(err) {
		return nil
	}
	return err
}
