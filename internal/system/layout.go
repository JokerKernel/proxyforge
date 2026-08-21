package system

import (
	"path/filepath"
	"strings"
)

type Layout struct{ Root string }

func (l Layout) Resolve(path string) string {
	if l.Root == "" || l.Root == "/" {
		return path
	}
	return filepath.Join(l.Root, strings.TrimPrefix(path, "/"))
}

func (l Layout) StatePath(core string) string {
	return l.Resolve("/var/lib/proxyforge/state/" + core + ".json")
}
func (l Layout) TrustPath(core string) string {
	return l.Resolve("/var/lib/proxyforge/trust/" + core + ".sha256")
}
func (l Layout) BackupRoot(core string) string {
	return l.Resolve("/var/lib/proxyforge/backups/" + core)
}

func (l Layout) XrayServiceAccountMarkerPath() string {
	return l.Resolve("/var/lib/proxyforge/state/xray-service-account.json")
}
