package system

import (
	"errors"
	"fmt"

	"proxyforge/internal/domain"
)

type LinkStore struct{ Layout Layout }

func (s LinkStore) Load(core string) ([]domain.LandingAccess, []domain.RelayLink, error) {
	landings, err := loadLinkEntries[domain.LandingAccess](s.Layout.LandingPath(core))
	if err != nil {
		return nil, nil, err
	}
	relays, err := loadLinkEntries[domain.RelayLink](s.Layout.RelayPath(core))
	return landings, relays, err
}

func (s LinkStore) Save(core string, landings []domain.LandingAccess, relays []domain.RelayLink) error {
	files, err := s.prepare(core, landings, relays)
	if err != nil {
		return err
	}
	return writePrivateFiles(files)
}

func (s LinkStore) prepare(core string, landings []domain.LandingAccess, relays []domain.RelayLink) ([]privateJSONFile, error) {
	landingFile, err := preparePrivateJSON(s.Layout.LandingPath(core), append([]domain.LandingAccess{}, landings...))
	if err != nil {
		return nil, err
	}
	relayFile, err := preparePrivateJSON(s.Layout.RelayPath(core), append([]domain.RelayLink{}, relays...))
	if err != nil {
		return nil, err
	}
	return []privateJSONFile{landingFile, relayFile}, nil
}

func (s LinkStore) Delete(core string) error {
	return errors.Join(removeOptionalFile(s.Layout.LandingPath(core)), removeOptionalFile(s.Layout.RelayPath(core)))
}

func loadLinkEntries[T any](path string) ([]T, error) {
	var entries *[]*T
	exists, err := loadPrivateJSON(path, &entries)
	if err != nil || !exists {
		return nil, err
	}
	if entries == nil {
		return nil, fmt.Errorf("线路配置必须是 JSON 数组: %s", path)
	}
	if len(*entries) == 0 {
		return nil, nil
	}
	result := make([]T, len(*entries))
	for i, entry := range *entries {
		if entry == nil {
			return nil, fmt.Errorf("线路配置第 %d 条记录不能为 null: %s", i+1, path)
		}
		result[i] = *entry
	}
	return result, nil
}
