package system

import (
	"os"
	"path/filepath"
	"testing"

	"proxyforge/internal/domain"
)

func TestInstallPreferencesPersistIndependently(t *testing.T) {
	layout := Layout{Root: t.TempDir()}
	store := InstallPreferencesStore{Layout: layout}
	for _, core := range []string{domain.CoreSingBox, domain.CoreXray} {
		if got, err := store.LoadChannel(core); err != nil || got != InstallChannelStable {
			t.Fatalf("new core=%s channel=%q err=%v", core, got, err)
		}
	}
	if err := store.SaveChannel(domain.CoreSingBox, InstallChannelBeta); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveChannel(domain.CoreXray, InstallChannelStable); err != nil {
		t.Fatal(err)
	}
	// A new store represents reopening the application; defaults must come
	// from disk rather than an in-memory menu choice.
	reopened := InstallPreferencesStore{Layout: layout}
	for _, tt := range []struct{ core, channel string }{
		{domain.CoreSingBox, InstallChannelBeta}, {domain.CoreXray, InstallChannelStable},
	} {
		got, err := reopened.LoadChannel(tt.core)
		if err != nil || got != tt.channel {
			t.Fatalf("core=%s channel=%q err=%v", tt.core, got, err)
		}
		path := layout.InstallPreferencesPath(tt.core)
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != 0600 {
			t.Fatalf("preference file is not private: %v %v", info, err)
		}
		info, err = os.Stat(filepath.Dir(path))
		if err != nil || info.Mode().Perm() != 0700 {
			t.Fatalf("preference directory is not private: %v %v", info, err)
		}
	}
	if err := reopened.SaveChannel(domain.CoreSingBox, "invalid"); err == nil {
		t.Fatal("invalid channel accepted")
	}
	if got, err := reopened.LoadChannel(domain.CoreSingBox); err != nil || got != InstallChannelBeta {
		t.Fatalf("invalid write changed the saved selection: %q %v", got, err)
	}
}

func TestInstallPreferencesRejectInvalidRecordsAndCores(t *testing.T) {
	store := InstallPreferencesStore{Layout: Layout{Root: t.TempDir()}}
	for _, data := range []string{"{", `{}`, `{"install_channel":"unknown"}`} {
		if err := AtomicWrite(store.Layout.InstallPreferencesPath(domain.CoreXray), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := store.LoadChannel(domain.CoreXray); err == nil {
			t.Fatalf("invalid record %q silently accepted", data)
		}
	}
	if _, err := store.LoadChannel("../other"); err == nil {
		t.Fatal("invalid core accepted for reading")
	}
	if err := store.SaveChannel("../other", InstallChannelBeta); err == nil {
		t.Fatal("invalid core accepted for writing")
	}
}
