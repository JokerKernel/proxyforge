package app

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"proxyforge/internal/provider"
	"proxyforge/internal/provider/singbox"
	"proxyforge/internal/provider/xray"
)

type configValidationRunner func(context.Context, string, ...string) ([]byte, error)

func (f configValidationRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	return f(ctx, name, args...)
}

func TestConfigValidationUsesPrivateTemporaryFiles(t *testing.T) {
	for _, p := range []provider.CoreProvider{xray.New(), singbox.New()} {
		for _, temporary := range []bool{false, true} {
			for _, rejected := range []bool{false, true} {
				name := p.Name() + "/rendered/success"
				if temporary {
					name = p.Name() + "/capability/success"
				}
				if rejected {
					name += "/rejected"
				}
				t.Run(name, func(t *testing.T) {
					dir := t.TempDir()
					t.Setenv("TMPDIR", dir)
					configPath := filepath.Join(dir, "server", "config.json")
					config := []byte(`{"manual":"<preserved>"}`)
					if err := os.MkdirAll(filepath.Dir(configPath), 0750); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(configPath, config, 0600); err != nil {
						t.Fatal(err)
					}
					validationErr := errors.New("configuration rejected")
					var validationPath string
					runner := configValidationRunner(func(_ context.Context, binary string, args ...string) ([]byte, error) {
						if binary != p.Binary() {
							t.Fatalf("binary = %s; want %s", binary, p.Binary())
						}
						validationPath = args[len(args)-1]
						wantDir, wantPrefix := filepath.Dir(configPath), ".proxyforge-validate-"
						if temporary {
							wantDir, wantPrefix = dir, "proxyforge-capability-"
						}
						if filepath.Dir(validationPath) != wantDir || !strings.HasPrefix(filepath.Base(validationPath), wantPrefix) {
							t.Fatalf("unexpected validation path: %s", validationPath)
						}
						info, err := os.Stat(validationPath)
						if err != nil {
							t.Fatal(err)
						}
						if info.Mode().Perm() != 0600 {
							t.Fatalf("temporary file mode = %o; want 0600", info.Mode().Perm())
						}
						got, err := os.ReadFile(validationPath)
						if err != nil || !bytes.Equal(got, config) {
							t.Fatalf("temporary content = %s, error = %v", got, err)
						}
						if rejected {
							return nil, validationErr
						}
						return nil, nil
					})
					var err error
					if temporary {
						err = validateTemporary(context.Background(), p, runner, config)
					} else {
						err = validateRendered(context.Background(), p, runner, configPath, config)
					}
					if (err != nil) != rejected || (rejected && !errors.Is(err, validationErr)) {
						t.Fatalf("validation error = %v; rejected = %v", err, rejected)
					}
					if validationPath == "" {
						t.Fatal("provider validation was not called")
					}
					if _, err := os.Stat(validationPath); !os.IsNotExist(err) {
						t.Fatalf("temporary file was not removed: %v", err)
					}
					got, err := os.ReadFile(configPath)
					if err != nil || !bytes.Equal(got, config) {
						t.Fatalf("original configuration changed: %s, error = %v", got, err)
					}
				})
			}
		}
	}
}

func TestConfigValidationStopsWhenTemporaryFileCannotBeCreated(t *testing.T) {
	dir := t.TempDir()
	blocked := filepath.Join(dir, "not-a-directory")
	if err := os.WriteFile(blocked, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	runner := configValidationRunner(func(context.Context, string, ...string) ([]byte, error) {
		t.Fatal("provider validation called without a temporary configuration")
		return nil, nil
	})
	p := xray.New()
	if err := validateRendered(context.Background(), p, runner, filepath.Join(blocked, "config.json"), nil); err == nil {
		t.Fatal("expected configuration directory error")
	}
	t.Setenv("TMPDIR", blocked)
	if err := validateTemporary(context.Background(), p, runner, nil); err == nil {
		t.Fatal("expected temporary file creation error")
	}
	got, err := os.ReadFile(blocked)
	if err != nil || string(got) != "keep" {
		t.Fatalf("existing file changed: %s, error = %v", got, err)
	}
}
