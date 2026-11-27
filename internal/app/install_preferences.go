package app

import (
	"proxyforge/internal/install"
	"proxyforge/internal/system"
)

func (a *App) PreferredInstallChannel(core string) (string, error) {
	if a == nil {
		return system.InstallChannelStable, nil
	}
	return (system.InstallPreferencesStore{Layout: a.Layout}).LoadChannel(core)
}

func (a *App) rememberInstallChannel(core string, opts install.Options) error {
	// A pinned version applies to this operation only; it must not replace
	// the channel to follow on the next visit to the installation page.
	if opts.Version != "" {
		return nil
	}
	channel := system.InstallChannelStable
	if opts.Beta {
		channel = system.InstallChannelBeta
	}
	return (system.InstallPreferencesStore{Layout: a.Layout}).SaveChannel(core, channel)
}
