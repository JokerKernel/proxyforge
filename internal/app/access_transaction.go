package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"proxyforge/internal/provider"
	"proxyforge/internal/system"
)

// A journal contains the original bytes, including files absent before the
// update. It is removed only after both the configuration and state are saved.
type accessSnapshot struct {
	Data   []byte      `json:"data"`
	Exists bool        `json:"exists"`
	Mode   os.FileMode `json:"mode"`
	UID    int         `json:"uid"`
	GID    int         `json:"gid"`
}

type accessTransaction struct {
	Version int              `json:"version"`
	Running bool             `json:"running"`
	Files   []accessSnapshot `json:"files"`
}

func (a *App) accessTransactionPath(core string) string {
	return a.Layout.Resolve("/var/lib/proxyforge/transactions/access-" + core + ".json")
}

func (a *App) accessTransactionFiles(p provider.CoreProvider, core string) []string {
	return []string{a.Layout.Resolve(p.ConfigPath()), a.Layout.AccessPolicyPath(core),
		a.Layout.LandingPath(core), a.Layout.RelayPath(core), a.Layout.StatePath(core)}
}

func (a *App) lockAccessPolicy(ctx context.Context, core string) (func(), error) {
	path := a.accessTransactionPath(core) + ".lock"
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	if err := os.Chmod(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	for {
		err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return func() { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN); _ = f.Close() }, nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EAGAIN) {
			_ = f.Close()
			return nil, err
		}
		select {
		case <-ctx.Done():
			_ = f.Close()
			return nil, ctx.Err()
		case <-time.After(25 * time.Millisecond):
		}
	}
}

func (a *App) beginAccessTransaction(ctx context.Context, p provider.CoreProvider, core string) error {
	tx := accessTransaction{Version: 1}
	status, err := a.Services.IsActive(ctx, p.ServiceName())
	if tx.Running, err = installedServiceRunning(status, err); err != nil {
		return err
	}
	for _, path := range a.accessTransactionFiles(p, core) {
		data, err := os.ReadFile(path)
		exists := err == nil
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		meta, err := readMetadata(path, exists)
		if err != nil {
			return err
		}
		tx.Files = append(tx.Files, accessSnapshot{data, exists, meta.mode, meta.uid, meta.gid})
	}
	data, err := json.Marshal(tx)
	if err != nil {
		return err
	}
	return system.AtomicWrite(a.accessTransactionPath(core), data, 0600)
}

// Call only under the access lock, before reading state or starting an update.
// Paths come from the layout, never from journal contents.
func (a *App) recoverAccessTransaction(ctx context.Context, p provider.CoreProvider, core string) error {
	path := a.accessTransactionPath(core)
	if info, err := os.Stat(path); err == nil && info.Mode().Perm()&0077 != 0 {
		return fmt.Errorf("访问控制事务权限不安全: %s（要求 0600）", path)
	}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var tx accessTransaction
	paths := a.accessTransactionFiles(p, core)
	if err := json.Unmarshal(data, &tx); err != nil {
		return fmt.Errorf("读取访问控制事务: %w", err)
	}
	if tx.Version != 1 || len(tx.Files) != len(paths) || !tx.Files[0].Exists {
		return fmt.Errorf("访问控制事务格式无效: %s", path)
	}
	for i, name := range paths {
		before := tx.Files[i]
		if err := restoreFile(name, before.Data, before.Exists, fileMetadata{before.Mode, before.UID, before.GID}); err != nil {
			return fmt.Errorf("恢复访问控制事务 %s: %w", name, err)
		}
	}
	status, statusErr := a.Services.IsActive(ctx, p.ServiceName())
	running, err := installedServiceRunning(status, statusErr)
	if err != nil {
		return err
	}
	if tx.Running {
		if err := a.restartForRecovery(ctx, p.ServiceName()); err != nil {
			return err
		}
	} else if !tx.Running && running {
		if _, err := a.Services.Action(ctx, p.ServiceName(), "stop"); err != nil {
			return err
		}
	}
	return os.Remove(path)
}
