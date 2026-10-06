package system

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"proxyforge/internal/domain"
)

const (
	InstallChannelStable = "stable"
	InstallChannelBeta   = "beta"
)

type InstallPreferencesStore struct{ Layout Layout }

type installPreferences struct {
	Channel string `json:"install_channel"`
}

func validatePreferencesCore(core string) error {
	if core != domain.CoreSingBox && core != domain.CoreXray {
		return fmt.Errorf("不支持的内核 %q", core)
	}
	return nil
}

func (s InstallPreferencesStore) LoadChannel(core string) (string, error) {
	if err := validatePreferencesCore(core); err != nil {
		return "", err
	}
	path := s.Layout.InstallPreferencesPath(core)
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return InstallChannelStable, nil
	}
	if err != nil {
		return "", fmt.Errorf("读取安装偏好 %s: %w", path, err)
	}
	var prefs installPreferences
	if err := json.Unmarshal(b, &prefs); err != nil {
		return "", fmt.Errorf("安装偏好文件损坏 %s: %w", path, err)
	}
	if prefs.Channel != InstallChannelStable && prefs.Channel != InstallChannelBeta {
		return "", fmt.Errorf("安装偏好渠道无效 %s: %q", path, prefs.Channel)
	}
	return prefs.Channel, nil
}

func (s InstallPreferencesStore) SaveChannel(core, channel string) error {
	if err := validatePreferencesCore(core); err != nil {
		return err
	}
	if channel != InstallChannelStable && channel != InstallChannelBeta {
		return fmt.Errorf("安装偏好渠道无效: %q", channel)
	}
	b, err := json.MarshalIndent(installPreferences{Channel: channel}, "", "  ")
	if err != nil {
		return err
	}
	path := s.Layout.InstallPreferencesPath(core)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	if err := os.Chmod(filepath.Dir(path), 0700); err != nil {
		return err
	}
	return AtomicWrite(path, append(b, '\n'), 0600)
}
