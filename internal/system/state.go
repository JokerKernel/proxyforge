package system

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"

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
	if n.SchemaVersion > domain.StateSchemaVersion {
		return n, fmt.Errorf("状态文件版本 %d 高于当前支持版本 %d: %s", n.SchemaVersion, domain.StateSchemaVersion, path)
	}
	security := domain.NormalizeLandingSecurity(n.Security)
	if security != domain.LandingSecurityReality && security != domain.LandingSecurityTLS {
		return n, fmt.Errorf("状态文件安全协议无效: %s", path)
	}
	n.AccessPolicy, err = (AccessPolicyStore{Layout: s.Layout}).Load(core)
	if err != nil {
		return n, err
	}
	n.LandingAccesses, n.RelayLinks, err = (LinkStore{Layout: s.Layout}).Load(core)
	if err != nil {
		return n, err
	}
	return n, nil
}

func (s StateStore) Save(n domain.NodeSpec) error {
	nodeFile, err := preparePrivateJSON(s.Layout.StatePath(n.Core), n)
	if err != nil {
		return err
	}
	policyFile, err := (AccessPolicyStore{Layout: s.Layout}).prepare(n.Core, n.AccessPolicy)
	if err != nil {
		return err
	}
	linkFiles, err := (LinkStore{Layout: s.Layout}).prepare(n.Core, n.LandingAccesses, n.RelayLinks)
	if err != nil {
		return err
	}
	files := append([]privateJSONFile{policyFile}, linkFiles...)
	return writePrivateFiles(append(files, nodeFile))
}

func (s StateStore) Delete(core string) error {
	return errors.Join(
		removeOptionalFile(s.Layout.StatePath(core)),
		(AccessPolicyStore{Layout: s.Layout}).Delete(core),
		(LinkStore{Layout: s.Layout}).Delete(core),
	)
}
