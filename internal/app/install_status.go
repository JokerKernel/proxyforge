package app

import "context"

type CoreInstallStatus struct {
	Installed bool
	Version   string
}

// CoreInstallStatus reads only the local binary, without a network request.
// The version saved with a node configuration may be stale after an upgrade.
func (a *App) CoreInstallStatus(ctx context.Context, core string) CoreInstallStatus {
	status := CoreInstallStatus{Version: a.CoreVersion(ctx, core)}
	if status.Version != "" {
		status.Installed = true
		return status
	}
	if a != nil && a.Registry != nil {
		if p, err := a.Registry.Get(core); err == nil {
			_, err = a.lookPath(p.Binary())
			status.Installed = err == nil
		}
	}
	return status
}
