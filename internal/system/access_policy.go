package system

import (
	"fmt"

	"proxyforge/internal/domain"
)

type AccessPolicyStore struct{ Layout Layout }

func (s AccessPolicyStore) Load(core string) (domain.AccessPolicy, error) {
	path := s.Layout.AccessPolicyPath(core)
	// Accept the retired marker when reading existing files, then omit it
	// on save. Only mode and the current whitelist determine behavior.
	var policy *struct {
		domain.AccessPolicy
		LegacyAutoWhitelist bool `json:"auto_whitelist,omitempty"`
	}
	exists, err := loadPrivateJSON(path, &policy)
	if err != nil {
		return domain.AccessPolicy{}, err
	}
	if !exists {
		return domain.AccessPolicy{}, nil
	}
	if policy == nil {
		return domain.AccessPolicy{}, fmt.Errorf("访问控制配置必须是 JSON 对象: %s", path)
	}
	normalized, err := policy.AccessPolicy.Normalize()
	if err != nil {
		return domain.AccessPolicy{}, fmt.Errorf("访问控制配置无效 %s: %w", path, err)
	}
	return normalized, nil
}

func (s AccessPolicyStore) Save(core string, policy domain.AccessPolicy) error {
	file, err := s.prepare(core, policy)
	if err != nil {
		return err
	}
	return writePrivateFiles([]privateJSONFile{file})
}

func (s AccessPolicyStore) prepare(core string, policy domain.AccessPolicy) (privateJSONFile, error) {
	normalized, err := policy.Normalize()
	if err != nil {
		return privateJSONFile{}, err
	}
	// Keep the switch, mode and both lists visible, including empty lists.
	config := struct {
		Disabled  bool                 `json:"disabled"`
		Mode      string               `json:"mode"`
		Whitelist []domain.AccessEntry `json:"whitelist"`
		Blacklist []domain.AccessEntry `json:"blacklist"`
	}{
		Disabled:  normalized.Disabled,
		Mode:      normalized.Mode,
		Whitelist: append([]domain.AccessEntry{}, normalized.Whitelist...),
		Blacklist: append([]domain.AccessEntry{}, normalized.Blacklist...),
	}
	return preparePrivateJSON(s.Layout.AccessPolicyPath(core), config)
}

func (s AccessPolicyStore) Delete(core string) error {
	return removeOptionalFile(s.Layout.AccessPolicyPath(core))
}
